package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 探测目标选择 ----------

func TestProbeTargetSelection(t *testing.T) {
	// 优先 TCP 443 的落地端口
	l := Landing{Landing: "203.0.113.1", Ports: parsePorts("tcp:80:80 tcp:443:443 tcp:8388:8388")}
	h, p, ok := probeTarget(l)
	if !ok || h != "203.0.113.1" || p != 443 {
		t.Fatalf("应优先 443：host=%s port=%d ok=%v", h, p, ok)
	}
	// 没有 443 时取第一个 TCP 端口（用落地端口 remote，不是监听端口 listen）
	l2 := Landing{Landing: "203.0.113.1", Ports: parsePorts("tcp:10443:443 tcp:18080:8080")}
	_, p2, ok2 := probeTarget(l2)
	if !ok2 || p2 != 443 {
		t.Fatalf("应取第一个 TCP 的落地端口 443：port=%d ok=%v", p2, ok2)
	}
	// 只有 UDP 端口的落地不探测（TCP 测不了 UDP，硬测只会误报离线）
	l3 := Landing{Landing: "203.0.113.1", Ports: parsePorts("udp:8443:8443 udp:8444:8444")}
	if _, _, ok3 := probeTarget(l3); ok3 {
		t.Fatalf("纯 UDP 落地不应有探测目标")
	}
	// 没有任何端口也不探测
	l4 := Landing{Landing: "203.0.113.1"}
	if _, _, ok4 := probeTarget(l4); ok4 {
		t.Fatalf("无端口落地不应有探测目标")
	}
	// 任意门模式同样按落地端口探测（目标仍是落地地址:落地端口）
	l5 := Landing{Landing: "203.0.113.1", Mode: "door", Ports: parsePorts("tcp:20443:443")}
	_, p5, ok5 := probeTarget(l5)
	if !ok5 || p5 != 443 {
		t.Fatalf("任意门落地应探测落地端口 443：port=%d ok=%v", p5, ok5)
	}
}

// ---------- 失败原因分类 ----------

type fakeTimeoutErr struct{ msg string }

func (e fakeTimeoutErr) Error() string   { return e.msg }
func (e fakeTimeoutErr) Timeout() bool   { return true }
func (e fakeTimeoutErr) Temporary() bool { return true }

func TestClassifyProbeError(t *testing.T) {
	// 超时
	if kind, msg := classifyProbeError(fakeTimeoutErr{"i/o timeout"}, 3*time.Second); kind != "timeout" || !strings.Contains(msg, "超时") {
		t.Fatalf("超时应分到 timeout：kind=%s msg=%s", kind, msg)
	}
	// DNS 解析失败
	dnsErr := &net.DNSError{Err: "no such host", Name: "node.example.com", IsNotFound: true}
	if kind, _ := classifyProbeError(dnsErr, 3*time.Second); kind != "dns" {
		t.Fatalf("DNS 错误应分到 dns：kind=%s", kind)
	}
	// 拒绝连接
	if kind, msg := classifyProbeError(errors.New("dial tcp 127.0.0.1:443: connect: connection refused"), 3*time.Second); kind != "refused" || !strings.Contains(msg, "拒绝") {
		t.Fatalf("拒绝连接应分到 refused：kind=%s msg=%s", kind, msg)
	}
	// 其他连接失败
	if kind, _ := classifyProbeError(errors.New("dial tcp 10.0.0.1:443: connect: operation not permitted"), 3*time.Second); kind != "failed" {
		t.Fatalf("其他错误应分到 failed：kind=%s", kind)
	}
}

// ---------- 在线 / 离线实测（本地 TCP 服务） ----------

func TestProbeOneOnlineLocal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	l := Landing{Landing: "127.0.0.1", Ports: []PortSpec{{Proto: "tcp", Listen: port, Remote: port}}}
	r := probeOne(l, 3*time.Second)
	if r == nil {
		t.Fatalf("有 TCP 端口的落地应有探测结果")
	}
	if !r.Online {
		t.Fatalf("本地监听端口应在线：%+v", r)
	}
	if r.LatencyMs == nil || *r.LatencyMs < 0 {
		t.Fatalf("在线时延迟应为非负毫秒数：%+v", r)
	}
	if r.Error != "" || r.ErrorKind != "" {
		t.Fatalf("在线时不应有失败原因：%+v", r)
	}
	if r.Target != fmt.Sprintf("127.0.0.1:%d", port) {
		t.Fatalf("探测目标应为落地地址:落地端口：%s", r.Target)
	}
	if r.CheckedAt == "" {
		t.Fatalf("上次探测时间不应为空")
	}
}

func TestProbeOneRefused(t *testing.T) {
	// 起一个监听再关掉，拿到一个确定没人听的本地端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	l := Landing{Landing: "127.0.0.1", Ports: []PortSpec{{Proto: "tcp", Listen: port, Remote: port}}}
	r := probeOne(l, 3*time.Second)
	if r == nil || r.Online {
		t.Fatalf("关掉的端口应离线：%+v", r)
	}
	if r.ErrorKind != "refused" {
		t.Fatalf("关掉的端口应分到 refused，实际 kind=%s msg=%s", r.ErrorKind, r.Error)
	}
	if r.LatencyMs != nil {
		t.Fatalf("离线时延迟应为 null：%+v", r)
	}
}

func TestProbeOneTimeoutInjectedDialer(t *testing.T) {
	// 本 VM 沙箱会拦截外网拨号，真实超时测不准：用注入的拨号函数返回超时错误
	old := probeDial
	defer func() { probeDial = old }()
	probeDial = func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, fakeTimeoutErr{"dial tcp: i/o timeout"}
	}
	l := Landing{Landing: "203.0.113.1", Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}}
	r := probeOne(l, 3*time.Second)
	if r == nil || r.Online {
		t.Fatalf("超时应离线：%+v", r)
	}
	if r.ErrorKind != "timeout" {
		t.Fatalf("超时应分到 timeout，实际 kind=%s msg=%s", r.ErrorKind, r.Error)
	}
}

func TestProbeOneDNSInjectedDialer(t *testing.T) {
	old := probeDial
	defer func() { probeDial = old }()
	probeDial = func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "node.example.com", IsNotFound: true}
	}
	l := Landing{Landing: "node.example.com", Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}}
	r := probeOne(l, 3*time.Second)
	if r == nil || r.Online {
		t.Fatalf("DNS 失败应离线：%+v", r)
	}
	if r.ErrorKind != "dns" {
		t.Fatalf("DNS 失败应分到 dns，实际 kind=%s msg=%s", r.ErrorKind, r.Error)
	}
}

// ---------- 并发探测总耗时有上限 ----------

func TestProbeAllBounded(t *testing.T) {
	// 10 个落地全是关掉的本地端口（每个单独拨号都立刻失败，用注入拨号模拟慢超时）：
	// 注入的拨号每次睡 300ms 再报超时；串行要 3 秒，并发应在 2 秒内跑完。
	old := probeDial
	defer func() { probeDial = old }()
	probeDial = func(network, address string, timeout time.Duration) (net.Conn, error) {
		time.Sleep(300 * time.Millisecond)
		return nil, fakeTimeoutErr{"dial tcp: i/o timeout"}
	}
	var landings []Landing
	for i := 0; i < 10; i++ {
		landings = append(landings, Landing{Landing: fmt.Sprintf("203.0.113.%d", i+1), Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}})
	}
	start := time.Now()
	res := probeAllLandings(landings, 3*time.Second)
	elapsed := time.Since(start)
	if len(res) != 10 {
		t.Fatalf("应返回 10 个结果，实际 %d", len(res))
	}
	for _, r := range res {
		if r.Online || r.ErrorKind != "timeout" {
			t.Fatalf("注入超时应全离线且 kind=timeout：%+v", r)
		}
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("10 个落地并发探测耗时 %v，应 < 2s（串行要 3 秒）", elapsed)
	}
}

// ---------- 持久化 round-trip ----------

func TestLatencyStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "latency.json")
	st := newLatencyStore(path)
	ms := 42
	st.set("203.0.113.1|203.0.113.1:443", &LatencyResult{Landing: "203.0.113.1", Target: "203.0.113.1:443", Online: true, LatencyMs: &ms, CheckedAt: "2026-11-18T11:00:00Z"})
	st.set("203.0.113.2|203.0.113.2:443", &LatencyResult{Landing: "203.0.113.2", Target: "203.0.113.2:443", Online: false, ErrorKind: "refused", Error: "连接被拒绝", CheckedAt: "2026-11-18T11:00:00Z"})
	// 重启面板：新 store 从同一文件读回
	st2 := newLatencyStore(path)
	st2.load()
	r1 := st2.get("203.0.113.1|203.0.113.1:443")
	if r1 == nil || !r1.Online || r1.LatencyMs == nil || *r1.LatencyMs != 42 {
		t.Fatalf("重启后在线结果应读回：%+v", r1)
	}
	r2 := st2.get("203.0.113.2|203.0.113.2:443")
	if r2 == nil || r2.Online || r2.ErrorKind != "refused" || r2.Error != "连接被拒绝" {
		t.Fatalf("重启后离线结果应读回：%+v", r2)
	}
	// 与流量统计分开存：latency.json 里不应出现流量字段，traffic.json 不动
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "up_total") || strings.Contains(string(b), "traffic") {
		t.Fatalf("latency.json 不应混入流量数据：%s", b)
	}
	// 读回的拷贝改了不影响库存
	r1.LatencyMs = new(int)
	*r1.LatencyMs = 999
	if got := st2.get("203.0.113.1|203.0.113.1:443"); got.LatencyMs == nil || *got.LatencyMs != 42 {
		t.Fatalf("get 应返回拷贝，改拷贝不应污染库存：%+v", got)
	}
	// 同一 IP、不同端口的两个落地键不同，各有各的结果
	k1 := latencyStoreKey(Landing{Landing: "203.0.113.1", Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}})
	k2 := latencyStoreKey(Landing{Landing: "203.0.113.1", Ports: []PortSpec{{Proto: "tcp", Listen: 8443, Remote: 8443}}})
	if k1 == k2 || k1 != "203.0.113.1|203.0.113.1:443" || k2 != "203.0.113.1|203.0.113.1:8443" {
		t.Fatalf("同 IP 不同端口的键应不同：k1=%s k2=%s", k1, k2)
	}
	if k3 := latencyStoreKey(Landing{Landing: "203.0.113.1", Ports: []PortSpec{{Proto: "udp", Listen: 8443, Remote: 8443}}}); k3 != "" {
		t.Fatalf("纯 UDP 落地的键应为空：%s", k3)
	}
}

// ---------- 配置 ----------

func TestLatencyCfgValidate(t *testing.T) {
	d := defaultLatencyCfg()
	if !d.Enabled || d.IntervalMin != 5 || d.TimeoutSec != 3 {
		t.Fatalf("默认值应为开启/5 分钟/3 秒：%+v", d)
	}
	c := defaultLatencyCfg()
	c.IntervalMin = 120
	c.validate()
	if c.IntervalMin != 60 {
		t.Fatalf("间隔应钳制到 60，实际 %d", c.IntervalMin)
	}
	c2 := defaultLatencyCfg()
	c2.IntervalMin = 0
	c2.validate()
	if c2.IntervalMin != 1 {
		t.Fatalf("间隔应钳制到 1，实际 %d", c2.IntervalMin)
	}
	// 旧 config.json 没有 latency 一节（全 0）时补默认值（开启）
	var empty LatencyCfg
	empty.applyDefaults()
	if empty != defaultLatencyCfg() {
		t.Fatalf("缺失一节应补默认值：%+v", empty)
	}
	// 用户明确关掉探测（IntervalMin 已保存为 5）时不被默认值覆盖
	off := LatencyCfg{Enabled: false, IntervalMin: 5, TimeoutSec: 3}
	off.applyDefaults()
	if off.Enabled {
		t.Fatalf("用户关掉的探测不应被默认值重新开启：%+v", off)
	}
}

// ---------- 告警去重 / 节流 / 恢复 ----------

func latencyResult(landing string, online bool, ms int, errKind, errMsg string) *LatencyResult {
	r := &LatencyResult{Landing: landing, Target: landing + ":443", Online: online, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if online {
		r.LatencyMs = &ms
	} else {
		r.ErrorKind = errKind
		r.Error = errMsg
	}
	return r
}

func TestEvaluateLatencyDedupThrottleRecover(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	base := time.Now()
	off := latencyResult("203.0.113.1", false, 0, "refused", "连接被拒绝")
	// 首次离线：记 1 条审计
	s.evaluateLatency([]*LatencyResult{off}, base)
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("首次离线后审计=%d want 1", n)
	}
	// 30 分钟后仍离线：同一落地、未满 1 小时，不重复记审计
	s.evaluateLatency([]*LatencyResult{off}, base.Add(30*time.Minute))
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("30 分钟内重复离线审计=%d want 1（去重）", n)
	}
	// 61 分钟后仍离线：只发 Telegram 提醒（没配 Telegram 是空操作），仍不记审计
	s.evaluateLatency([]*LatencyResult{off}, base.Add(61*time.Minute))
	if n := countAudit(t, s); n != 1 {
		t.Fatalf("1 小时提醒审计=%d want 1（提醒不记审计）", n)
	}
	// 恢复在线：再记 1 条
	s.evaluateLatency([]*LatencyResult{latencyResult("203.0.113.1", true, 42, "", "")}, base.Add(62*time.Minute))
	if n := countAudit(t, s); n != 2 {
		t.Fatalf("恢复后审计=%d want 2", n)
	}
	// 恢复后再次离线：重新触发，再记 1 条
	s.evaluateLatency([]*LatencyResult{off}, base.Add(63*time.Minute))
	if n := countAudit(t, s); n != 3 {
		t.Fatalf("再次离线后审计=%d want 3", n)
	}
	// 审计内容应包含离线与恢复两种事件
	entries, _ := s.readAuditPage(1, 1000, "")
	var sawOff, sawRecover bool
	for _, e := range entries {
		if e.Action != "延迟探测" || e.Target != "203.0.113.1" {
			continue
		}
		switch e.Result {
		case "离线":
			sawOff = true
		case "恢复":
			sawRecover = true
		}
	}
	if !sawOff || !sawRecover {
		t.Fatalf("审计缺事件：离线=%v 恢复=%v（%+v）", sawOff, sawRecover, entries)
	}
}

// ---------- 接口 ----------

func TestHandleLatencySettings(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	post := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/settings/latency", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.handleLatencySettings(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	// 间隔 120：钳制到 60；超时不传时保持 3 秒
	code, resp := post(`{"enabled":true,"interval_min":120}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("合法设置应 200：code=%d resp=%v", code, resp)
	}
	if s.cfg.Latency.IntervalMin != 60 || s.cfg.Latency.TimeoutSec != 3 {
		t.Fatalf("间隔应钳制到 60、超时保持 3：%+v", s.cfg.Latency)
	}
	// 关闭探测：保存成功，不触发探测
	code, _ = post(`{"enabled":false,"interval_min":5}`)
	if code != 200 || s.cfg.Latency.Enabled {
		t.Fatalf("关闭探测应保存成功：code=%d cfg=%+v", code, s.cfg.Latency)
	}
	// 请求格式不对：400
	code, _ = post(`{bad json`)
	if code != 400 {
		t.Fatalf("非法 JSON 应 400，实际 %d", code)
	}
}

func TestHandleLatency(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	realmDir := s.env.RealmDir
	conf := "LANDING=127.0.0.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node（Xray）\"\nPORTS=\"tcp:80:80 tcp:443:443\"\nLANDING_COUNT=1\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	// 预存一个探测结果：接口应原样返回
	ms := 37
	s.latency.set("127.0.0.1|127.0.0.1:443", &LatencyResult{Landing: "127.0.0.1", Target: "127.0.0.1:443", Online: true, LatencyMs: &ms, CheckedAt: "2026-11-18T11:00:00Z"})
	req := httptest.NewRequest("GET", "/api/v1/latency", nil)
	rec := httptest.NewRecorder()
	s.handleLatency(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK      bool `json:"ok"`
		Latency LatencyCfg
		Results []LatencyResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !resp.OK || len(resp.Results) != 1 {
		t.Fatalf("应返回 1 个落地结果：%+v", resp)
	}
	r := resp.Results[0]
	if !r.Online || r.LatencyMs == nil || *r.LatencyMs != 37 || r.Target != "127.0.0.1:443" {
		t.Fatalf("结果不对：%+v", r)
	}
	if !resp.Latency.Enabled || resp.Latency.IntervalMin != 5 || resp.Latency.TimeoutSec != 3 {
		t.Fatalf("配置不对：%+v", resp.Latency)
	}
}

// ---------- 总览用探测结果（不再测中转机本地监听端口） ----------

func TestHandleStatusUsesLatency(t *testing.T) {
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	realmDir := s.env.RealmDir
	// 落地 127.0.0.1:443：本地 443 没人听，探测目标是落地地址:落地端口 → 应判离线且原因“拒绝连接”
	conf := "LANDING=127.0.0.1\nLANDING_IS_DOMAIN=0\nDOMAIN=\"\"\nCERT_DOMAIN=\"example.com\"\nPRESET=1\nMODE_NOTE=\"tiktok-node（Xray）\"\nPORTS=\"tcp:80:80 tcp:443:443\"\nLANDING_COUNT=1\n"
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK       bool `json:"ok"`
		Landings []struct {
			Landing        string `json:"landing"`
			Online         *bool  `json:"online"`
			LatencyMs      *int   `json:"latency_ms"`
			LatencyError   string `json:"latency_error"`
			LatencyChecked string `json:"latency_checked_at"`
			LatencyTarget  string `json:"latency_target"`
		} `json:"landings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if len(resp.Landings) != 1 {
		t.Fatalf("应返回 1 个落地：%+v", resp)
	}
	l := resp.Landings[0]
	if l.Online == nil || *l.Online {
		t.Fatalf("本地 443 无人监听应判离线：online=%v", l.Online)
	}
	if l.LatencyMs != nil {
		t.Fatalf("离线时延迟应为 null：%v", *l.LatencyMs)
	}
	if !strings.Contains(l.LatencyError, "拒绝") {
		t.Fatalf("离线原因应写“拒绝连接”：%s", l.LatencyError)
	}
	if l.LatencyTarget != "127.0.0.1:443" {
		t.Fatalf("探测目标应为落地地址:落地端口：%s", l.LatencyTarget)
	}
	if l.LatencyChecked == "" {
		t.Fatalf("上次探测时间不应为空")
	}
	// 探测结果应已持久化到 latency.json（重启后还在）
	if got := s.latency.get("127.0.0.1|127.0.0.1:443"); got == nil || got.Online {
		t.Fatalf("探测结果应存进 latency.json：%+v", got)
	}
}
