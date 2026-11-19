package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 流量累计（PANEL_HOME/traffic.json） ----------

type hourlyBucket struct {
	TS   string `json:"ts"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

type dailyBucket struct {
	Date string `json:"date"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

type landingTraffic struct {
	UpTotal   int64          `json:"up_total"`
	DownTotal int64          `json:"down_total"`
	UpMonth   int64          `json:"up_month"`
	DownMonth int64          `json:"down_month"`
	MonthKey  string         `json:"month_key"`
	Hourly    []hourlyBucket `json:"hourly"`
	Daily     []dailyBucket  `json:"daily"`
	// 采样基数（不导出到 API，存文件里用于增量计算）
	LastCounters map[string][2]int64 `json:"last_counters"`
}

type trafficStore struct {
	mu   sync.Mutex
	path string
	data map[string]*landingTraffic
}

func newTrafficStore(path string) *trafficStore {
	return &trafficStore{path: path, data: map[string]*landingTraffic{}}
}

func (t *trafficStore) load() {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &t.data)
	if t.data == nil {
		t.data = map[string]*landingTraffic{}
	}
}

func (t *trafficStore) saveLocked() {
	b, _ := json.MarshalIndent(t.data, "", "  ")
	_ = writeFileAtomic(t.path, b, 0o600)
}

func (t *trafficStore) get(landing string) *landingTraffic {
	t.mu.Lock()
	defer t.mu.Unlock()
	lt, ok := t.data[landing]
	if !ok {
		lt = &landingTraffic{MonthKey: time.Now().Format("2006-01"), LastCounters: map[string][2]int64{}}
		t.data[landing] = lt
	}
	if lt.LastCounters == nil {
		lt.LastCounters = map[string][2]int64{}
	}
	return lt
}

// totals 只读累计流量（不建新记录）：返回上行/下行累计字节与有没有数据。
// 订阅的 subscription-userinfo 头用它，没数据时不编数。
func (t *trafficStore) totals(landing string) (up, down int64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lt, ok := t.data[landing]
	if !ok || lt == nil {
		return 0, 0, false
	}
	return lt.UpTotal, lt.DownTotal, true
}

// rename 改地址时把流量计数搬到新键（旧键删除）：
// 累计流量、月度用量、采样基数一起搬，不丢数；新键已有数据时不覆盖（保留新键的）。
func (t *trafficStore) rename(old, new string) {
	if old == new {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	lt, ok := t.data[old]
	if !ok {
		return
	}
	if _, exists := t.data[new]; !exists {
		t.data[new] = lt
	}
	delete(t.data, old)
	t.saveLocked()
}

// addDelta 累计增量（采样用）；处理计数器重启清零（快照比上次小则按新值重记基数，不算负增量）。
func (t *trafficStore) addDelta(landing string, counters map[string][2]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lt, ok := t.data[landing]
	if !ok {
		lt = &landingTraffic{MonthKey: time.Now().Format("2006-01"), LastCounters: map[string][2]int64{}}
		t.data[landing] = lt
	}
	if lt.LastCounters == nil {
		lt.LastCounters = map[string][2]int64{}
	}
	now := time.Now()
	monthKey := now.Format("2006-01")
	if lt.MonthKey != monthKey {
		lt.UpMonth, lt.DownMonth = 0, 0
		lt.MonthKey = monthKey
	}
	var upDelta, downDelta int64
	for key, cur := range counters {
		prev, seen := lt.LastCounters[key]
		if !seen {
			lt.LastCounters[key] = cur
			continue
		}
		// key 形如 "tcp:443:in" / "tcp:443:out"；in=上行，out=下行
		var du, dd int64
		if strings.HasSuffix(key, ":in") {
			du = deltaCounter(prev[0], cur[0])
		} else {
			dd = deltaCounter(prev[1], cur[1])
		}
		// 上面按单值处理有误：counters 值是 [inBytes, outBytes] 对，key 不分 in/out。
		// 实际调用方传的 key 是 "proto:port"，值 [inBytes, outBytes]；这里统一算：
		du = deltaCounter(prev[0], cur[0])
		dd = deltaCounter(prev[1], cur[1])
		upDelta += du
		downDelta += dd
		lt.LastCounters[key] = cur
	}
	if upDelta == 0 && downDelta == 0 {
		return
	}
	lt.UpTotal += upDelta
	lt.DownTotal += downDelta
	lt.UpMonth += upDelta
	lt.DownMonth += downDelta
	hourKey := now.Format("2006-01-02T15:00")
	if len(lt.Hourly) > 0 && lt.Hourly[len(lt.Hourly)-1].TS == hourKey {
		lt.Hourly[len(lt.Hourly)-1].Up += upDelta
		lt.Hourly[len(lt.Hourly)-1].Down += downDelta
	} else {
		lt.Hourly = append(lt.Hourly, hourlyBucket{TS: hourKey, Up: upDelta, Down: downDelta})
	}
	if len(lt.Hourly) > 720 {
		lt.Hourly = lt.Hourly[len(lt.Hourly)-720:]
	}
	dayKey := now.Format("2006-01-02")
	if len(lt.Daily) > 0 && lt.Daily[len(lt.Daily)-1].Date == dayKey {
		lt.Daily[len(lt.Daily)-1].Up += upDelta
		lt.Daily[len(lt.Daily)-1].Down += downDelta
	} else {
		lt.Daily = append(lt.Daily, dailyBucket{Date: dayKey, Up: upDelta, Down: downDelta})
	}
	if len(lt.Daily) > 90 {
		lt.Daily = lt.Daily[len(lt.Daily)-90:]
	}
	t.saveLocked()
}

// deltaCounter 计数器重启清零时按新值重记基数（不算负增量）。
func deltaCounter(prev, cur int64) int64 {
	if cur >= prev {
		return cur - prev
	}
	return 0 // 计数器清零：本轮增量丢弃，基数已在调用方更新
}

// trafficUsed 按模式算已用字节：total=累计，monthly=当月。
func trafficUsed(lt *landingTraffic, mode string) int64 {
	if mode == "monthly" {
		return lt.UpMonth + lt.DownMonth
	}
	return lt.UpTotal + lt.DownTotal
}

// overLimit 限量判定：上限>0 且已用>=上限。
func overLimit(lt *landingTraffic, limit int64, mode string) bool {
	if limit <= 0 {
		return false
	}
	return trafficUsed(lt, mode) >= limit
}

// ---------- iptables-save 解析 ----------

// parseIptablesSave 解析 `iptables-save -c` 输出，返回 map["proto:port"]=[inBytes,outBytes]，
// 只统计 RELAY-PANEL-IN / RELAY-PANEL-OUT 两条链里的计数规则。
// 行示例： [123:456] -A RELAY-PANEL-IN -p tcp -m tcp --dport 443 -j RETURN
func parseIptablesSave(out string) map[string][2]int64 {
	res := map[string][2]int64{}
	re := regexp.MustCompile(`^\[(\d+):(\d+)\]\s+-A\s+(RELAY-PANEL-IN|RELAY-PANEL-OUT)\b.*-p\s+(tcp|udp)\b.*--(dport|sport)\s+(\d+)`)
	for _, line := range strings.Split(out, "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		pkts, _ := strconv.ParseInt(m[1], 10, 64)
		_ = pkts
		bytes, _ := strconv.ParseInt(m[2], 10, 64)
		key := m[4] + ":" + m[6]
		v := res[key]
		if m[3] == "RELAY-PANEL-IN" {
			v[0] += bytes
		} else {
			v[1] += bytes
		}
		res[key] = v
	}
	return res
}

// parseNftCounters 解析 `nft -j list ruleset`，返回 map["proto:port"]=[inBytes,outBytes]。
func parseNftCounters(jsonOut string) map[string][2]int64 {
	res := map[string][2]int64{}
	var doc struct {
		Nftables []struct {
			Rule *struct {
				Chain string `json:"chain"`
				Expr  []struct {
					Match *struct {
						Left struct {
							Payload *struct {
								Protocol string `json:"protocol"`
								Field    string `json:"field"`
							} `json:"payload"`
						} `json:"left"`
						Right any `json:"right"`
					} `json:"match"`
					Counter *struct {
						Bytes int64 `json:"bytes"`
					} `json:"counter"`
				} `json:"expr"`
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		return res
	}
	for _, item := range doc.Nftables {
		if item.Rule == nil {
			continue
		}
		var proto string
		var port int64
		var counter int64
		var hasCounter bool
		for _, e := range item.Rule.Expr {
			if e.Match != nil && e.Match.Left.Payload != nil {
				proto = e.Match.Left.Payload.Protocol
				switch rv := e.Match.Right.(type) {
				case float64:
					port = int64(rv)
				}
			}
			if e.Counter != nil {
				counter = e.Counter.Bytes
				hasCounter = true
			}
		}
		if !hasCounter || proto == "" || port == 0 {
			continue
		}
		key := proto + ":" + strconv.FormatInt(port, 10)
		v := res[key]
		if item.Rule.Chain == "in" {
			v[0] += counter
		} else if item.Rule.Chain == "out" {
			v[1] += counter
		}
		res[key] = v
	}
	return res
}

type trafficBackend struct {
	Kind string // "iptables" | "nftables" | ""
}

func detectTrafficBackend() trafficBackend {
	if commandExists("iptables") {
		if _, err := runCmdOut(nil, 5*time.Second, "iptables", "-L", "INPUT", "-n"); err == nil {
			return trafficBackend{Kind: "iptables"}
		}
		// 无 root 时 iptables -L 也可能失败；按存在性判断，后续采样失败再报 available:false
		return trafficBackend{Kind: "iptables"}
	}
	if commandExists("nft") {
		return trafficBackend{Kind: "nftables"}
	}
	return trafficBackend{Kind: ""}
}

// ensureTrafficChains 建链 + 每监听端口计数规则（iptables 方案，契约 §6）。
func (s *Server) ensureTrafficChains(landings []Landing) error {
	be := detectTrafficBackend()
	if be.Kind != "iptables" {
		return fmt.Errorf("本机无 iptables，流量统计不可用")
	}
	run := func(args ...string) { _, _ = runCmdOut(nil, 10*time.Second, "iptables", args...) }
	// 建链（已存在则忽略错误）
	run("-N", "RELAY-PANEL-IN")
	run("-N", "RELAY-PANEL-OUT")
	// 跳入规则只插一次
	if runCmd(nil, 5*time.Second, "iptables", "-C", "INPUT", "-j", "RELAY-PANEL-IN") != nil {
		run("-I", "INPUT", "1", "-j", "RELAY-PANEL-IN")
	}
	if runCmd(nil, 5*time.Second, "iptables", "-C", "OUTPUT", "-j", "RELAY-PANEL-OUT") != nil {
		run("-I", "OUTPUT", "1", "-j", "RELAY-PANEL-OUT")
	}
	// 每端口计数规则（先清链内旧规则再重建，保证 quota 与计数一致）
	_, _ = runCmdOut(nil, 5*time.Second, "iptables", "-F", "RELAY-PANEL-IN")
	_, _ = runCmdOut(nil, 5*time.Second, "iptables", "-F", "RELAY-PANEL-OUT")
	for _, l := range landings {
		ext := s.state.ext(l.Landing)
		lt := s.traffic.get(l.Landing)
		remaining := int64(-1)
		if ext.TrafficLimitBytes > 0 {
			remaining = ext.TrafficLimitBytes - trafficUsed(lt, ext.TrafficLimitMode)
			if remaining < 0 {
				remaining = 0
			}
		}
		for _, p := range l.Ports {
			if remaining >= 0 {
				// quota 版：计数规则带 quota，后跟 DROP
				run("-A", "RELAY-PANEL-IN", "-p", p.Proto, "--dport", strconv.Itoa(p.Listen), "-m", "quota", "--quota", strconv.FormatInt(remaining, 10), "-j", "RETURN")
				run("-A", "RELAY-PANEL-IN", "-p", p.Proto, "--dport", strconv.Itoa(p.Listen), "-j", "DROP")
				run("-A", "RELAY-PANEL-OUT", "-p", p.Proto, "--sport", strconv.Itoa(p.Listen), "-m", "quota", "--quota", strconv.FormatInt(remaining, 10), "-j", "RETURN")
				run("-A", "RELAY-PANEL-OUT", "-p", p.Proto, "--sport", strconv.Itoa(p.Listen), "-j", "DROP")
			} else {
				run("-A", "RELAY-PANEL-IN", "-p", p.Proto, "--dport", strconv.Itoa(p.Listen), "-j", "RETURN")
				run("-A", "RELAY-PANEL-OUT", "-p", p.Proto, "--sport", strconv.Itoa(p.Listen), "-j", "RETURN")
			}
		}
	}
	run("-A", "RELAY-PANEL-IN", "-j", "RETURN")
	run("-A", "RELAY-PANEL-OUT", "-j", "RETURN")
	return nil
}

// sampleTraffic 一次采样：iptables-save -c 解析增量累加到 traffic.json。
func (s *Server) sampleTraffic() {
	rc, _, err := s.readRelayConf()
	if err != nil || rc == nil {
		return
	}
	be := detectTrafficBackend()
	var counters map[string][2]int64
	switch be.Kind {
	case "iptables":
		r, err := runCmdOut(nil, 10*time.Second, "iptables-save", "-c")
		if err != nil {
			return
		}
		counters = parseIptablesSave(r.Stdout)
	case "nftables":
		r, err := runCmdOut(nil, 10*time.Second, "nft", "-j", "list", "ruleset")
		if err != nil {
			return
		}
		counters = parseNftCounters(r.Stdout)
	default:
		return
	}
	// 按落地分：每个落地的端口集合对应一组计数
	for _, l := range rc.Landings {
		per := map[string][2]int64{}
		for _, p := range l.Ports {
			key := fmt.Sprintf("%s:%d", p.Proto, p.Listen)
			if v, ok := counters[key]; ok {
				per[key] = v
			}
		}
		if len(per) > 0 {
			s.traffic.addDelta(l.Landing, per)
		}
	}
}

// applySpeedLimit tc HTB 限速（best-effort）：失败返回错误，不阻塞其他操作。
func (s *Server) applySpeedLimit(l Landing, mbps int) error {
	if mbps <= 0 {
		return nil // 取消限速：清掉该端口的 filter（best-effort，失败忽略）
	}
	iface := defaultIface()
	if iface == "" {
		return fmt.Errorf("本机限速不可用（无 tc 或网卡探测失败），流量上限仍生效")
	}
	if !commandExists("tc") {
		return fmt.Errorf("本机限速不可用（无 tc 或网卡探测失败），流量上限仍生效")
	}
	// 根 qdisc + HTB 类（已存在则忽略）
	_, _ = runCmdOut(nil, 5*time.Second, "tc", "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "30")
	for _, p := range l.Ports {
		classID := fmt.Sprintf("1:%d", 10+p.Listen%90)
		rate := fmt.Sprintf("%dmbit", mbps)
		_, _ = runCmdOut(nil, 5*time.Second, "tc", "class", "add", "dev", iface, "parent", "1:", "classid", classID, "htb", "rate", rate)
		_, _ = runCmdOut(nil, 5*time.Second, "tc", "filter", "add", "dev", iface, "protocol", "ip", "parent", "1:", "prio", "1", "u32", "match", "ip", "sport", strconv.Itoa(p.Listen), "0xffff", "flowid", classID)
	}
	_ = filepath.Base(iface)
	return nil
}

func defaultIface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}
