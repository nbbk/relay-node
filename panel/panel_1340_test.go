package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- v1.3.40：按落地设流量倍率 ----------
// 倍率落在落地记录上（relay.conf 的 MULTIPLIER[_N]，默认 1，上限 100）；
// 计量与限流按倍率折算：实际流量 × 倍率计入订阅用量，userinfo 头与
// 80%/100% 限流都按折算后的用量算。落点只有三处累计/执法入口，
// 下游（userinfo、80%/100% 执法、集中管理聚合）读同一张已折算的表。

func TestNormalizeTrafficMultiplier1340(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 1},  // 没填 = 默认 1
		{-1, 1}, // 非法 = 1
		{math.NaN(), 1},
		{math.Inf(1), 1},
		{1, 1},
		{0.5, 0.5}, // 折扣也允许
		{2, 2},
		{2.5, 2.5},
		{100, 100}, // 上限
		{101, 100}, // 超上限钳到 100
		{200, 100},
	}
	for _, c := range cases {
		if got := normalizeTrafficMultiplier(c.in); got != c.want {
			t.Fatalf("normalizeTrafficMultiplier(%v) 应为 %v，实际 %v", c.in, c.want, got)
		}
	}
	// 零值 Landing（旧落地无该字段）按 1 算。
	if got := (Landing{}).multiplier(); got != 1 {
		t.Fatalf("旧落地（无倍率字段）应按 1 算，实际 %v", got)
	}
	if got := (Landing{TrafficMultiplier: 2}).multiplier(); got != 2 {
		t.Fatalf("倍率 2 的落地应为 2，实际 %v", got)
	}
	if got := formatMultiplier(0); got != "×1" {
		t.Fatalf("formatMultiplier(0) 应为 ×1，实际 %q", got)
	}
	if got := formatMultiplier(2.5); got != "×2.5" {
		t.Fatalf("formatMultiplier(2.5) 应为 ×2.5，实际 %q", got)
	}
}

func TestParseRelayConfMultiplier1340(t *testing.T) {
	conf := "LANDING=203.0.113.1\nPORTS=\"tcp:443:443\"\nMULTIPLIER=2\n" +
		"LANDING_2=203.0.113.2\nPORTS_2=\"tcp:443:443\"\nMULTIPLIER_2=2.5\n" +
		"LANDING_3=203.0.113.3\nPORTS_3=\"tcp:443:443\"\nMULTIPLIER_3=abc\n" +
		"LANDING_COUNT=3\n"
	rc := parseRelayConf([]byte(conf))
	if len(rc.Landings) != 3 {
		t.Fatalf("应解析出 3 个落地，实际 %d", len(rc.Landings))
	}
	if rc.Landings[0].TrafficMultiplier != 2 || rc.Landings[0].multiplier() != 2 {
		t.Fatalf("落地 1 倍率应为 2，实际 %v", rc.Landings[0].TrafficMultiplier)
	}
	if rc.Landings[1].TrafficMultiplier != 2.5 || rc.Landings[1].multiplier() != 2.5 {
		t.Fatalf("落地 2 倍率应为 2.5，实际 %v", rc.Landings[1].TrafficMultiplier)
	}
	// 非法值不报错、不拦解析，解析后按 1 算。
	if rc.Landings[2].multiplier() != 1 {
		t.Fatalf("倍率非法（abc）的落地应按 1 算，实际 %v", rc.Landings[2].multiplier())
	}
	// 旧 relay.conf 没 MULTIPLIER 键：按 1 算。
	rc2 := parseRelayConf([]byte(subTestRelayConf))
	for _, l := range rc2.Landings {
		if l.TrafficMultiplier != 0 || l.multiplier() != 1 {
			t.Fatalf("旧落地（无 MULTIPLIER 键）应按 1 算，实际字段 %v、有效 %v", l.TrafficMultiplier, l.multiplier())
		}
	}
}

func TestRebuildRelayConfMultiplier1340(t *testing.T) {
	rc := parseRelayConf([]byte(subTestRelayConf))
	nl := Landing{Index: 2, Landing: "203.0.113.2", CertDomain: "node2.example.com", Preset: "2", Mode: "standard",
		Ports: parsePorts("tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388"), TrafficMultiplier: 2.5}
	lines := rebuildRelayConfForEdit(rc, 2, nl, false, true, "")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `MULTIPLIER_2="2.5"`) {
		t.Fatalf("倍率 2.5 应写进 relay.conf（MULTIPLIER_2），实际：\n%s", joined)
	}
	// 倍率改回 1：键删掉（默认 1 不存键），其他落地行不动。
	nl.TrafficMultiplier = 1
	lines = rebuildRelayConfForEdit(rc, 2, nl, false, true, "")
	joined = strings.Join(lines, "\n")
	if strings.Contains(joined, "MULTIPLIER") {
		t.Fatalf("倍率 1 不应存 MULTIPLIER 键，实际：\n%s", joined)
	}
	if !strings.Contains(joined, `LANDING_2="203.0.113.2"`) {
		t.Fatalf("重建后落地 2 的地址行应还在，实际：\n%s", joined)
	}
	// 写进去再读回来：往返一致。
	rc2 := parseRelayConf([]byte(strings.Join(rebuildRelayConfForEdit(rc, 2, Landing{Index: 2, Landing: "203.0.113.2", CertDomain: "node2.example.com", Preset: "2", Mode: "standard", Ports: parsePorts("tcp:443:443"), TrafficMultiplier: 3}, false, true, ""), "\n")))
	if len(rc2.Landings) < 2 || rc2.Landings[1].multiplier() != 3 {
		t.Fatalf("写进再读回倍率应为 3，实际 %+v", rc2.Landings)
	}
}

func TestValidateLandingMultiplier1340(t *testing.T) {
	base := addLandingReq{Landing: "203.0.113.1", Preset: "1"}
	if msg := validateLandingReqFields(base); msg != "" {
		t.Fatalf("合法请求应通过，实际 %q", msg)
	}
	for _, m := range []float64{0.5, 1, 2.5, 100} {
		req := base
		req.TrafficMultiplier = m
		if msg := validateLandingReqFields(req); msg != "" {
			t.Fatalf("倍率 %v 应通过，实际 %q", m, msg)
		}
	}
	for _, m := range []float64{-1, 101, 1000} {
		req := base
		req.TrafficMultiplier = m
		if msg := validateLandingReqFields(req); msg == "" {
			t.Fatalf("倍率 %v 应被拒，实际通过了", m)
		}
	}
}

func TestAddDeltaScaled1340(t *testing.T) {
	dir := t.TempDir()
	st := newUserTrafficStore(filepath.Join(dir, "user_traffic.json"))
	key := "subuser:su-1"
	// 倍率 2：首见 1000/2000 → 记 2000/4000。
	st.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {1000, 2000}}, map[string]float64{"tcp:30000": 2})
	up, down, upP, downP, ok := st.totals(key)
	if !ok || up != 2000 || down != 4000 || upP != 2000 || downP != 4000 {
		t.Fatalf("倍率 2 时首见 1000/2000 应记 2000/4000，实际 up=%d down=%d upP=%d downP=%d ok=%v", up, down, upP, downP, ok)
	}
	// 第二次采样：增量 500/500 × 2 = 1000/1000。
	st.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {1500, 2500}}, map[string]float64{"tcp:30000": 2})
	up, down, _, _, _ = st.totals(key)
	if up != 3000 || down != 5000 {
		t.Fatalf("第二次采样后应为 3000/5000，实际 up=%d down=%d", up, down)
	}
	// 倍率中途改成 1：基线仍是原始计数，增量 500/500 × 1 = 500/500，
	// 不会因为上一次按 2 存了基线就错。
	st.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {2000, 3000}}, map[string]float64{"tcp:30000": 1})
	up, down, _, _, _ = st.totals(key)
	if up != 3500 || down != 5500 {
		t.Fatalf("倍率改回 1 后增量应按 1 算，实际 up=%d down=%d", up, down)
	}
	// 缺倍率项 = 1（addDelta 老入口行为不变）。
	st.addDelta(key, map[string][2]int64{"tcp:30000": {2100, 3100}})
	up, down, _, _, _ = st.totals(key)
	if up != 3600 || down != 5600 {
		t.Fatalf("addDelta（无倍率）应按 1 累计，实际 up=%d down=%d", up, down)
	}
	// 小数倍率四舍五入：增量 1 × 1.5 = 1.5 → 2。
	st.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {2101, 3101}}, map[string]float64{"tcp:30000": 1.5})
	up, down, _, _, _ = st.totals(key)
	if up != 3602 || down != 5602 {
		t.Fatalf("增量 1 × 1.5 应四舍五入记 2，实际 up=%d down=%d", up, down)
	}
	// 多端口多倍率：同一订阅跨两个落地，各乘各的再汇总。
	st2 := newUserTrafficStore(filepath.Join(dir, "user_traffic2.json"))
	st2.addDeltaScaled(key,
		map[string][2]int64{"tcp:30000": {1000, 0}, "tcp:30001": {1000, 0}},
		map[string]float64{"tcp:30000": 2, "tcp:30001": 3})
	up, _, _, _, _ = st2.totals(key)
	if up != 5000 {
		t.Fatalf("两端口 1000×2 + 1000×3 应为 5000，实际 %d", up)
	}
}

func TestUserQuotaRemaining1340(t *testing.T) {
	cases := []struct {
		limit, used int64
		mult        float64
		want        int64
	}{
		{10000, 4000, 1, 6000},    // 倍率 1 不变
		{10000, 4000, 2, 3000},    // 折算后剩余 6000，换回原始字节 3000
		{10000, 0, 2, 5000},       // 刚开始：内核一分钟内最多放 5000 原始字节
		{10000, 10000, 2, 0},      // 已用完
		{10000, 12000, 2, 0},      // 超了钳到 0
		{10000, 4000, 0, 6000},    // 倍率 0 按 1 算
		{10000, 4000, 0.5, 12000}, // 折扣倍率：剩余量换回原始字节变大
	}
	for _, c := range cases {
		if got := userQuotaRemaining(c.limit, c.used, c.mult); got != c.want {
			t.Fatalf("userQuotaRemaining(%d,%d,%v) 应为 %d，实际 %d", c.limit, c.used, c.mult, c.want, got)
		}
	}
}

// ---------- agent 推送与采样侧的倍率 ----------

const multTestRelayConf1340 = `LANDING=203.0.113.1
LANDING_IS_DOMAIN=0
DOMAIN=""
CERT_DOMAIN="example.com"
PRESET=1
MODE_NOTE="tiktok-node（Xray）"
PORTS="tcp:80:80 tcp:443:443"
MULTIPLIER=2
LANDING_2=203.0.113.2
CERT_DOMAIN_2="node2.example.com"
PRESET_2=2
MODE_NOTE_2="sing-box-node TLS 模式"
PORTS_2="tcp:80:80 tcp:443:443 tcp:8388:8388 udp:8388:8388"
MULTIPLIER_2=2.5
LANDING_COUNT=2
ALL_PORTS="tcp:80:80 tcp:443:443"
SHARE80=0
SHARE443=1
`

// newMultiplierServer1340 造一台倍率面板：落地 1 倍率 2、落地 2 倍率 2.5，
// 订阅用户 su-1（限流 100MB total），rpt_ 令牌已设好。
func newMultiplierServer1340(t *testing.T) (*Server, string) {
	t.Helper()
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	panelHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(subTestNodesTxt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "relay.conf"), []byte(multTestRelayConf1340), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realmDir, "config.toml"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: panelHome, RealmDir: realmDir, KitDir: kitDir, Port: 11111})
	if err != nil {
		t.Fatal(err)
	}
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	s.cfg.SubUsers = []SubUser{{
		ID: "su-1", Name: "小明", TokenHash: "hash-su-1",
		CreatedAt: "2026-11-20T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}},
		Mode: "both", ExpiresAt: "2027-05-01T23:59:59Z",
		TrafficLimitBytes: 100 * 1024 * 1024, TrafficLimitMode: "total",
		ResetMode: "monthly", ResetDay: 1,
	}}
	if err := s.saveConfig(); err != nil {
		t.Fatal(err)
	}
	return s, tok
}

func TestNodePushMultiplier1340(t *testing.T) {
	s, tok := newMultiplierServer1340(t)
	// landingMultiplier：按地址取倍率，找不到按 1。
	if got := s.landingMultiplier("203.0.113.1"); got != 2 {
		t.Fatalf("落地 1 倍率应为 2，实际 %v", got)
	}
	if got := s.landingMultiplier("203.0.113.2"); got != 2.5 {
		t.Fatalf("落地 2 倍率应为 2.5，实际 %v", got)
	}
	if got := s.landingMultiplier("203.0.113.9"); got != 1 {
		t.Fatalf("不存在的落地应按 1 算，实际 %v", got)
	}
	push := func(body string) map[string]any {
		rec := callNode1330(t, s, "POST", "/api/v1/node/push", body, tok)
		if rec.Code != 200 {
			t.Fatalf("推送应 200，实际 %d：%s", rec.Code, rec.Body.String())
		}
		return decodeNode1330(t, rec)
	}
	// 落地 1（倍率 2）：按人条目 1000/2000 → 记 2000/4000。
	resp := push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":1000,"down_bytes":2000}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, _ := s.userTraffic.totals("subuser:su-1")
	if up != 2000 || down != 4000 {
		t.Fatalf("倍率 2 的推送 1000/2000 应记 2000/4000，实际 up=%d down=%d", up, down)
	}
	// generic 节点总量不乘：7000/8000 原样记（落地卡片仍显示原始流量）。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"generic","period_id":0,"seq":1,"up_bytes":7000,"down_bytes":8000}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("generic 推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, ok := s.userTraffic.totals("generic")
	if !ok || up != 7000 || down != 8000 {
		t.Fatalf("generic 用量应原样 7000/8000，实际 up=%d down=%d ok=%v", up, down, ok)
	}
	// 落地 2（倍率 2.5）：100/100 → 250/250，累计到同一订阅上。
	resp = push(`{"node_id":"203.0.113.2","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":2,"up_bytes":100,"down_bytes":100}]}`)
	if resp["accepted"] != float64(1) {
		t.Fatalf("落地 2 推送应 accepted=1，实际 %v", resp)
	}
	up, down, _, _, _ = s.userTraffic.totals("subuser:su-1")
	if up != 2250 || down != 4250 {
		t.Fatalf("两落地累计后应为 2250/4250，实际 up=%d down=%d", up, down)
	}
	// 重推第一条（同 seq）：去重，不重复乘。
	resp = push(`{"node_id":"203.0.113.1","entries":[{"user_id":"subuser:su-1","period_id":0,"seq":1,"up_bytes":1000,"down_bytes":2000}]}`)
	if resp["duplicates"] != float64(1) {
		t.Fatalf("重推应 duplicates=1，实际 %v", resp)
	}
	up, down, _, _, _ = s.userTraffic.totals("subuser:su-1")
	if up != 2250 || down != 4250 {
		t.Fatalf("重推后用量不应变，实际 up=%d down=%d", up, down)
	}
}

func TestUserTrafficPortMultipliers1340(t *testing.T) {
	s, _ := newMultiplierServer1340(t)
	s.cfg.UserForwards = []UserForward{
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30000},
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.2", ListenPort: 30001},
		{Key: "subuser:su-1", SubKind: "subuser", SubID: "su-1", Panel: "local", LandingAddr: "203.0.113.9", ListenPort: 30002},
	}
	m := s.userTrafficPortMultipliers()
	if m[30000] != 2 {
		t.Fatalf("落地 1 的转发端口 30000 倍率应为 2，实际 %v", m[30000])
	}
	if m[30001] != 2.5 {
		t.Fatalf("落地 2 的转发端口 30001 倍率应为 2.5，实际 %v", m[30001])
	}
	if _, ok := m[30002]; ok {
		t.Fatalf("不存在的落地对应的端口不应有倍率，实际 %v", m)
	}
}

func TestSubUserinfoWithMultiplier1340(t *testing.T) {
	s := newTestSubServer(t)
	key := "subuser:su-abc123"
	limit := int64(10 * 1024 * 1024) // 10MB
	// 倍率 2 下采样 3MB 原始流量 → 记 6MB，userinfo 头里就是 6MB，
	// 80% 判定按 6MB 算（未到），100% 也按 6MB 算（未到）。
	s.userTraffic.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {3 * 1024 * 1024, 0}}, map[string]float64{"tcp:30000": 2})
	h := s.subUserinfoHeaderForUser(key, limit, "total", "", map[int]bool{})
	if !strings.Contains(h, "upload=6291456") || !strings.Contains(h, "download=0") || !strings.Contains(h, "total=10485760") {
		t.Fatalf("userinfo 头应用折算后的 6MB，实际 %q", h)
	}
	rec := s.userTraffic.get(key)
	if userOverLimit(rec, limit, "total") {
		t.Fatalf("6MB/10MB 不应超量")
	}
	// 再采 2MB 原始流量 × 2 = 4MB，累计 10MB → 正好到上限。
	s.userTraffic.addDeltaScaled(key, map[string][2]int64{"tcp:30000": {5 * 1024 * 1024, 0}}, map[string]float64{"tcp:30000": 2})
	if !userOverLimit(rec, limit, "total") {
		t.Fatalf("累计 10MB/10MB 应超量")
	}
	h = s.subUserinfoHeaderForUser(key, limit, "total", "", map[int]bool{})
	if !strings.Contains(h, "upload=10485760") {
		t.Fatalf("userinfo 头应显示折算后的 10MB，实际 %q", h)
	}
}
