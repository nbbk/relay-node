package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 1.3.25：按订阅限流量 A 方案（每人一条转发） ----------
// 核心是把“按落地”统计细化到“按人×按落地”：每个开了限流的订阅
// 在每台中转机上有一条独立转发（独立监听端口 → 同一落地端口），
// 订阅里的节点链接改用该用户的端口，SNI 与域名不变，订阅链接本身不变。

// ---------- 限流字段校验与重置时间 ----------

func TestNormalizeUserLimit1325(t *testing.T) {
	// limit<=0 = 不限流，其余字段一律清掉
	limit, mode, rm, day, err := normalizeUserLimit(0, "monthly", "custom", 15)
	if err != nil || limit != 0 || mode != "" || rm != "" || day != 0 {
		t.Fatalf("limit=0 应全清，实际 limit=%d mode=%q reset=%q day=%d err=%v", limit, mode, rm, day, err)
	}
	// 正常：累计 + 自然月
	limit, mode, rm, day, err = normalizeUserLimit(10*1024*1024, "total", "monthly", 0)
	if err != nil || limit != 10*1024*1024 || mode != "total" || rm != "monthly" {
		t.Fatalf("累计模式归一化不对：limit=%d mode=%q reset=%q err=%v", limit, mode, rm, err)
	}
	// 空模式默认 total，空重置默认 monthly
	limit, mode, rm, _, err = normalizeUserLimit(100, "", "", 0)
	if err != nil || mode != "total" || rm != "monthly" {
		t.Fatalf("空模式应默认 total/monthly，实际 mode=%q reset=%q err=%v", mode, rm, err)
	}
	// 非法模式
	if _, _, _, _, err := normalizeUserLimit(100, "weekly", "monthly", 0); err == nil {
		t.Fatalf("非法上限模式应报错")
	}
	if _, _, _, _, err := normalizeUserLimit(100, "monthly", "weekly", 0); err == nil {
		t.Fatalf("非法重置模式应报错")
	}
	// custom 必须给 1–31 的日
	if _, _, _, _, err := normalizeUserLimit(100, "monthly", "custom", 0); err == nil {
		t.Fatalf("custom 没填日应报错")
	}
	if _, _, _, _, err := normalizeUserLimit(100, "monthly", "custom", 32); err == nil {
		t.Fatalf("custom 填 32 应报错")
	}
	if _, _, _, day, err := normalizeUserLimit(100, "monthly", "custom", 31); err != nil || day != 31 {
		t.Fatalf("custom 填 31 应通过，实际 day=%d err=%v", day, err)
	}
}

func TestResetOccurrence1325(t *testing.T) {
	utc := time.UTC
	// 自然月：1 月 15 日 → 下次是 2 月 1 日 00:00
	if got := nextResetOccurrence(1, time.Date(2026, 1, 15, 12, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, utc)) {
		t.Fatalf("1 月 15 日下次 1 号应是 2 月 1 日，实际 %v", got)
	}
	// 正好在重置时刻：不原地打转，去下个月
	if got := nextResetOccurrence(1, time.Date(2026, 2, 1, 0, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, utc)) {
		t.Fatalf("正好 2 月 1 日 00:00 时下次应是 3 月 1 日，实际 %v", got)
	}
	// 31 号钳制到月末：2026 年 2 月只有 28 天
	if got := nextResetOccurrence(31, time.Date(2026, 2, 10, 0, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, utc)) {
		t.Fatalf("2 月 10 日下次 31 号应钳制到 2 月 28 日，实际 %v", got)
	}
	if got := clampDay(2026, 2, 31); got != 28 {
		t.Fatalf("2026 年 2 月 31 号应钳制到 28，实际 %d", got)
	}
	if got := clampDay(2026, 1, 31); got != 31 {
		t.Fatalf("1 月 31 号不应钳制，实际 %d", got)
	}
	// 跟随生成日：建订阅那天是 1 月 15 日，1 月 20 日时下次是 2 月 15 日
	anchor := time.Date(2026, 1, 15, 10, 0, 0, 0, utc)
	if got := computeNextReset("anniversary", 0, anchor, time.Date(2026, 1, 20, 0, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 2, 15, 0, 0, 0, 0, utc)) {
		t.Fatalf("跟随生成日（15 号）下次应是 2 月 15 日，实际 %v", got)
	}
	// 跟随生成日遇小月钳制：建订阅 1 月 31 日，2 月 10 日时下次是 2 月 28 日
	anchor31 := time.Date(2026, 1, 31, 10, 0, 0, 0, utc)
	if got := computeNextReset("anniversary", 0, anchor31, time.Date(2026, 2, 10, 0, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 2, 28, 0, 0, 0, 0, utc)) {
		t.Fatalf("跟随生成日 31 号在 2 月应钳制到 2 月 28 日，实际 %v", got)
	}
	// 自定义每月 15 号
	if got := computeNextReset("custom", 15, anchor, time.Date(2026, 1, 20, 0, 0, 0, 0, utc)); !got.Equal(time.Date(2026, 2, 15, 0, 0, 0, 0, utc)) {
		t.Fatalf("自定义 15 号下次应是 2 月 15 日，实际 %v", got)
	}
	// normalizeUserLimitFields：monthly 算出 NextResetAt，total 为空
	_, _, _, _, next, err := normalizeUserLimitFields(10*1024*1024, "monthly", "monthly", 0, anchor, time.Date(2026, 1, 20, 0, 0, 0, 0, utc))
	if err != nil || next != "2026-02-01T00:00:00Z" {
		t.Fatalf("monthly 的 NextResetAt 应是 2026-02-01，实际 %q err=%v", next, err)
	}
	_, _, _, _, next, err = normalizeUserLimitFields(10*1024*1024, "total", "monthly", 0, anchor, time.Date(2026, 1, 20, 0, 0, 0, 0, utc))
	if err != nil || next != "" {
		t.Fatalf("total 模式的 NextResetAt 应为空，实际 %q err=%v", next, err)
	}
}

// ---------- 用户端口段与分配 ----------

func TestUserPortRangeAndAllocate1325(t *testing.T) {
	s := newTestSubServer(t)
	// 默认段 30000–39999
	start, end := s.userPortRange()
	if start != 30000 || end != 39999 {
		t.Fatalf("默认用户端口段应是 30000–39999，实际 %d–%d", start, end)
	}
	// 分配避让落地监听端口（80/443/8388）与面板端口（11111）
	used := s.usedListenPorts()
	if !used[80] || !used[443] || !used[11111] {
		t.Fatalf("usedListenPorts 应含落地监听端口与面板端口，实际 %v", used)
	}
	p, err := s.allocateUserPort(used)
	if err != nil || p != 30000 {
		t.Fatalf("首次分配应是 30000，实际 %d err=%v", p, err)
	}
	used[p] = true
	p2, err := s.allocateUserPort(used)
	if err != nil || p2 != 30001 {
		t.Fatalf("第二次分配应是 30001，实际 %d err=%v", p2, err)
	}
	// 段满报错
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 30000, 30001
	full := map[int]bool{30000: true, 30001: true}
	if _, err := s.allocateUserPort(full); err == nil {
		t.Fatalf("段满了应报错")
	}
	// 自定义段生效
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 40000, 40010
	start, end = s.userPortRange()
	if start != 40000 || end != 40010 {
		t.Fatalf("自定义段应是 40000–40010，实际 %d–%d", start, end)
	}
	p, err = s.allocateUserPort(map[int]bool{})
	if err != nil || p != 40000 {
		t.Fatalf("自定义段首次分配应是 40000，实际 %d err=%v", p, err)
	}
	// 非法段（起 > 止）回落默认
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 40000, 30000
	start, end = s.userPortRange()
	if start != 30000 || end != 39999 {
		t.Fatalf("非法段应回落默认 30000–39999，实际 %d–%d", start, end)
	}
}

// ---------- setLinkPort 四协议换端口 ----------

func TestSetLinkPort1325(t *testing.T) {
	// vless：只换端口，SNI/域名/路径不动
	vless := "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"
	got := setLinkPort(vless, 30001)
	if !strings.Contains(got, "example.com:30001") || !strings.Contains(got, "host=example.com") || !strings.Contains(got, "path=%2Fexample-path") {
		t.Fatalf("vless 换端口后应只变端口，实际 %s", got)
	}
	// trojan
	trojan := "trojan://pass123@example.com:443?security=tls&sni=example.com#智利-中转"
	if got := setLinkPort(trojan, 30002); !strings.Contains(got, "example.com:30002") {
		t.Fatalf("trojan 换端口不对，实际 %s", got)
	}
	// ss SIP002：ss://user@host:port
	ss := "ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@example.com:8388#智利-中转"
	if got := setLinkPort(ss, 30003); !strings.Contains(got, "example.com:30003") {
		t.Fatalf("ss SIP002 换端口不对，实际 %s", got)
	}
	// ss 传统 base64：ss://base64(method:password@host:port)
	legacy := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:password@example.com:8388")) + "#智利-中转"
	got = setLinkPort(legacy, 30004)
	raw, err := decodeSSOpaque(strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(got, "ss://"), "#", 2)[0], ""))
	if err != nil {
		t.Fatalf("ss 传统形式换端口后解不开：%v（%s）", err, got)
	}
	if !strings.Contains(string(raw), "example.com:30004") {
		t.Fatalf("ss 传统形式换端口后应是 example.com:30004，实际 %s", string(raw))
	}
	// vmess：JSON 里的 port 字段换掉，add 不动
	vmessJSON := `{"v":"2","ps":"智利-中转","add":"example.com","port":24443,"id":"11111111-2222-3333-4444-555555555555","aid":0,"net":"ws","type":"none","host":"example.com","path":"/example-path","tls":"tls"}`
	vmess := "vmess://" + base64.RawURLEncoding.EncodeToString([]byte(vmessJSON)) + "#智利-中转"
	got = setLinkPort(vmess, 30005)
	b64 := strings.TrimPrefix(strings.SplitN(strings.TrimPrefix(got, "vmess://"), "#", 2)[0], "")
	raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(b64, "="))
	if err != nil {
		t.Fatalf("vmess 换端口后解不开：%v", err)
	}
	var j map[string]any
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("vmess 换端口后 JSON 坏了：%v", err)
	}
	if j["port"] != float64(30005) || j["add"] != "example.com" {
		t.Fatalf("vmess 换端口后 port 应是 30005、add 不动，实际 %v", j)
	}
	// 端口相同：原样返回
	if got := setLinkPort(vless, 24443); got != vless {
		t.Fatalf("端口相同应原样返回，实际 %s", got)
	}
	// 坏链接：原样返回
	if got := setLinkPort("not-a-link", 30001); got != "not-a-link" {
		t.Fatalf("坏链接应原样返回，实际 %s", got)
	}
	// 非法端口：原样返回
	if got := setLinkPort(vless, 0); got != vless {
		t.Fatalf("端口 0 应原样返回，实际 %s", got)
	}
}

// ---------- 订阅改写：只改“-中转”，直连不动 ----------

func TestRewriteSubLinksForUser1325(t *testing.T) {
	links := []subLink{
		{Name: "智利-中转", LandingAddr: "203.0.113.1", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "智利-直连", LandingAddr: "203.0.113.1", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
		{Name: "测试-中转", LandingAddr: "203.0.113.2", Link: "vless://11111111-2222-3333-4444-555555555555@node2.example.com:443?encryption=none&security=tls&type=ws&host=node2.example.com&path=%2Fexample-path#测试-中转"},
	}
	portByAddr := map[string]int{"203.0.113.1": 30001}
	out := rewriteSubLinksForUser(links, portByAddr)
	if !strings.Contains(out[0].Link, "example.com:30001") {
		t.Fatalf("中转链接应改成用户端口 30001，实际 %s", out[0].Link)
	}
	if out[1].Link != links[1].Link {
		t.Fatalf("直连链接不应改，实际 %s", out[1].Link)
	}
	if out[2].Link != links[2].Link {
		t.Fatalf("没有用户转发的落地不应改，实际 %s", out[2].Link)
	}
	// 没有任何用户端口：原样返回
	if out := rewriteSubLinksForUser(links, map[string]int{}); out[0].Link != links[0].Link {
		t.Fatalf("无用户端口时应原样返回")
	}
}

// ---------- config.toml：只按监听端口删用户段，不碰共享落地 ----------

func TestRemoveUserEndpointsFromToml1325(t *testing.T) {
	toml := `[[endpoints]]
listen = "0.0.0.0:24443"
remote = "203.0.113.1:24443"

[[endpoints]]
listen = "0.0.0.0:30001"
remote = "203.0.113.1:24443"

[[endpoints]]
listen = "0.0.0.0:30002"
remote = "203.0.113.1:24443"

[[endpoints]]
listen = "0.0.0.0:443"
remote = "203.0.113.2:443"
`
	out := string(removeUserEndpointsFromToml([]byte(toml), map[int]bool{30001: true, 30002: true}))
	if strings.Contains(out, "30001") || strings.Contains(out, "30002") {
		t.Fatalf("用户端口段应被删掉，实际：\n%s", out)
	}
	if !strings.Contains(out, "0.0.0.0:24443") || !strings.Contains(out, "0.0.0.0:443") {
		t.Fatalf("共享落地段不应被碰，实际：\n%s", out)
	}
}

// ---------- 核对与回收 ----------

// 共享端点不被误删（v1.3.25 回归）：初始 toml 里有共享 24443 段，
// 连续核对多个用户后它必须还在，且各用户端口都在。旧代码的 join
// 让保留段与追加段之间没有空行，下次解析时两段合并、listen 取最后
// 一个，建第 2 个限流用户后共享 24443 段就被误删。
func TestApplyUserForwardsKeepsSharedEndpoint1325(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	shared := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(shared), 0o644); err != nil {
		t.Fatal(err)
	}
	limit := int64(10 * 1024 * 1024 * 1024)
	if err := s.reconcileUserForwards("subuser", "su-aaa111", "用户甲", nil, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("用户甲核对应成功，实际 err=%v", err)
	}
	if err := s.reconcileUserForwards("subuser", "su-bbb222", "用户乙", nil, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("用户乙核对应成功，实际 err=%v", err)
	}
	readToml := func() string {
		tomlB, err := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(tomlB)
	}
	toml := readToml()
	if !strings.Contains(toml, "0.0.0.0:24443") {
		t.Fatalf("两次核对后共享 24443 段不应被删，实际：\n%s", toml)
	}
	for _, p := range []string{"0.0.0.0:30000", "0.0.0.0:30001", "0.0.0.0:30002", "0.0.0.0:30003"} {
		if !strings.Contains(toml, p) {
			t.Fatalf("toml 里应有 %s，实际：\n%s", p, toml)
		}
	}
	// 第三次核对（再建一个用户）后共享段仍在
	if err := s.reconcileUserForwards("subuser", "su-ccc333", "用户丙", nil, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("用户丙核对应成功，实际 err=%v", err)
	}
	if toml = readToml(); !strings.Contains(toml, "0.0.0.0:24443") {
		t.Fatalf("第三次核对后共享 24443 段仍应存在，实际：\n%s", toml)
	}
}

// 落地级更新同根因回归：删掉最后一段落地再追加新段，保留段与新段
// 之间必须有空行，否则两段合并、下次删改会误删保留段。
func TestLandingUpdateKeepsOtherSegments1325(t *testing.T) {
	toml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:443\"\nremote = \"203.0.113.2:443\"\n"
	kept := ensureTomlNewline(removeEndpointsFromToml([]byte(toml), "203.0.113.2"))
	updated := Landing{Landing: "203.0.113.2", Ports: []PortSpec{{Proto: "tcp", Listen: 443, Remote: 443}}}
	out := appendEndpointsForLanding(kept, updated, false, false)
	for _, seg := range strings.Split(string(out), "\n\n") {
		if !strings.Contains(seg, "[[endpoints]]") {
			continue
		}
		if n := strings.Count(seg, "listen = "); n != 1 {
			t.Fatalf("每段应恰好 1 个 listen，实际 %d 段：\n%s", n, string(out))
		}
	}
	if !strings.Contains(string(out), "0.0.0.0:24443") || !strings.Contains(string(out), "0.0.0.0:443") {
		t.Fatalf("两落地段都应存在，实际：\n%s", string(out))
	}
	// 不加 ensureTomlNewline 时旧写法会把两段合并（回归测试应能抓到）。
	bad := removeEndpointsFromToml([]byte(toml), "203.0.113.2")
	badOut := appendEndpointsForLanding(bad, updated, false, false)
	merged := false
	for _, seg := range strings.Split(string(badOut), "\n\n") {
		if strings.Count(seg, "listen = ") > 1 {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("旧写法应复现两段合并，实际没合并，测试失效：\n%s", string(badOut))
	}
}

func TestReconcileUserForwards1325(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	limit := int64(10 * 1024 * 1024 * 1024) // 10GB
	// 空勾选 = 全部本地落地（测试机 2 个落地）→ 2 条转发
	if err := s.reconcileUserForwards("subuser", "su-abc123", "测试用户", nil, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("核对应成功，实际 err=%v", err)
	}
	fw := s.userForwardsFor("subuser:su-abc123")
	if len(fw) != 2 {
		t.Fatalf("2 个落地应有 2 条转发，实际 %d", len(fw))
	}
	if fw[0].ListenPort != 30000 || fw[1].ListenPort != 30001 {
		t.Fatalf("端口应从 30000 起按顺序分，实际 %d、%d", fw[0].ListenPort, fw[1].ListenPort)
	}
	for _, f := range fw {
		if f.Panel != "local" || f.LimitBytes != limit || f.LimitMode != "monthly" || f.NextResetAt != "2026-12-01T00:00:00Z" {
			t.Fatalf("转发记录字段不对：%+v", f)
		}
	}
	// config.toml 里应有用户端口段
	tomlB, err := os.ReadFile(filepath.Join(s.env.RealmDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tomlB), "0.0.0.0:30000") || !strings.Contains(string(tomlB), "0.0.0.0:30001") {
		t.Fatalf("config.toml 里应有用户端口段，实际：\n%s", string(tomlB))
	}
	// 再核对一次：不重复建（幂等）
	if err := s.reconcileUserForwards("subuser", "su-abc123", "测试用户", nil, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("重复核对应成功，实际 err=%v", err)
	}
	if fw := s.userForwardsFor("subuser:su-abc123"); len(fw) != 2 {
		t.Fatalf("重复核对不应重复建转发，实际 %d 条", len(fw))
	}
	// 改勾选只留落地 1：落地 2 的转发收掉，端口回收
	sel := []SubSelection{{Panel: "local", Landing: 1}}
	if err := s.reconcileUserForwards("subuser", "su-abc123", "测试用户", sel, limit, "monthly", "monthly", 1, "2026-12-01T00:00:00Z"); err != nil {
		t.Fatalf("改勾选核对应成功，实际 err=%v", err)
	}
	fw = s.userForwardsFor("subuser:su-abc123")
	if len(fw) != 1 || fw[0].LandingAddr != "203.0.113.1" {
		t.Fatalf("改勾选后应只剩落地 1 的转发，实际 %+v", fw)
	}
	// 取消限流（limit=0）：全部转发收掉，回共享端口
	if err := s.reconcileUserForwards("subuser", "su-abc123", "测试用户", sel, 0, "", "", 0, ""); err != nil {
		t.Fatalf("取消限流应成功，实际 err=%v", err)
	}
	if fw := s.userForwardsFor("subuser:su-abc123"); len(fw) != 0 {
		t.Fatalf("取消限流后应无转发，实际 %d 条", len(fw))
	}
}

// 端口段不够：预检报错，不留半截转发。
func TestReconcileUserForwardsPortFull1325(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	s.cfg.UserPortStart, s.cfg.UserPortEnd = 30000, 30000 // 只剩 1 个端口，2 个落地要 2 个
	err := s.reconcileUserForwards("subuser", "su-abc123", "测试用户", nil, 10*1024*1024*1024, "monthly", "monthly", 1, "2026-12-01T00:00:00Z")
	if err == nil {
		t.Fatalf("端口不够应报错")
	}
	if !strings.Contains(err.Error(), "只剩") {
		t.Fatalf("报错应写明剩几个端口，实际：%v", err)
	}
	if fw := s.userForwardsFor("subuser:su-abc123"); len(fw) != 0 {
		t.Fatalf("预检失败时不应留半截转发，实际 %d 条", len(fw))
	}
}

// ---------- 翻周期 ----------

func TestRollUserPeriods1325(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	utc := time.UTC
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, utc)
	// 按月订阅：已到重置日、已超量停用、本期有用量
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-abc123", Name: "测试用户", TrafficLimitBytes: 10 * 1024 * 1024 * 1024,
		TrafficLimitMode: "monthly", ResetMode: "monthly", ResetDay: 1,
		NextResetAt: "2026-11-01T00:00:00Z", Disabled: true, DisabledReason: "traffic",
		TrafficWarned80: true, TrafficWarned100: true,
	})
	rec := s.userTraffic.get("subuser:su-abc123")
	rec.UpPeriod, rec.DownPeriod = 5*1024*1024*1024, 5*1024*1024*1024
	rec.UpTotal, rec.DownTotal = 5*1024*1024*1024, 5*1024*1024*1024
	rec.PeriodID = 3
	// 累计订阅：NextResetAt 已过也不翻（total 没有周期）
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{
		ID: "su-total1", Name: "累计用户", TrafficLimitBytes: 10 * 1024 * 1024 * 1024,
		TrafficLimitMode: "total", NextResetAt: "2026-11-01T00:00:00Z",
	})
	// 远程面板上的转发（本面板没有对应订阅记录）：按转发自己的 NextResetAt 翻
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{
		Key: "merged:ms-remote1", SubKind: "merged", SubID: "ms-remote1", Panel: "remote-x",
		LandingAddr: "203.0.113.1", ListenPort: 30005, LimitMode: "monthly",
		ResetMode: "monthly", ResetDay: 1, NextResetAt: "2026-11-01T00:00:00Z", Disabled: true,
	})
	if !s.rollUserPeriods(now) {
		t.Fatalf("到了重置日应翻周期")
	}
	u := findSubUserByID(s.cfg.SubUsers, "su-abc123")
	if u.NextResetAt != "2026-12-01T00:00:00Z" {
		t.Fatalf("下次重置应推到 2026-12-01，实际 %s", u.NextResetAt)
	}
	if u.Disabled || u.DisabledReason != "" || u.TrafficWarned80 || u.TrafficWarned100 {
		t.Fatalf("翻周期后停用与提醒标志应清掉，实际 disabled=%v reason=%q w80=%v w100=%v", u.Disabled, u.DisabledReason, u.TrafficWarned80, u.TrafficWarned100)
	}
	if u.LastResetAt == "" {
		t.Fatalf("翻周期后 LastResetAt 应有值")
	}
	_, _, upP, downP, _ := s.userTraffic.totals("subuser:su-abc123")
	if upP != 0 || downP != 0 {
		t.Fatalf("翻周期后本期用量应清零，实际 up=%d down=%d", upP, downP)
	}
	ut := findSubUserByID(s.cfg.SubUsers, "su-total1")
	if ut.NextResetAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("累计订阅不应翻周期，实际 %s", ut.NextResetAt)
	}
	rf := s.findUserForward("merged:ms-remote1", "203.0.113.1")
	if rf == nil || rf.NextResetAt != "2026-12-01T00:00:00Z" || rf.Disabled {
		t.Fatalf("远程转发也应翻周期并恢复，实际 %+v", rf)
	}
	// 没到重置日：不翻
	if s.rollUserPeriods(time.Date(2026, 11, 20, 0, 0, 0, 0, utc)) {
		t.Fatalf("没到重置日不应翻周期")
	}
}

// ---------- userinfo 头：有限流用每用户用量，无用量记录回落 ----------

func TestSubUserinfoHeaderForUser1325(t *testing.T) {
	s := newTestSubServer(t)
	key := "subuser:su-abc123"
	limit := int64(10 * 1024 * 1024 * 1024) // 10GB
	// 没用量记录 → 仍有 total=上限（v1.3.27 改了契约：管理面板模式下本机
	// 永远没有用量记录，以前直接返回 ""，小火箭看不到上限与到期；现在
	// 不编用量，只拼上限与到期；三项全无时才返回 ""）。
	if h := s.subUserinfoHeaderForUser(key, limit, "total", "", map[int]bool{}); h != "total=10737418240" {
		t.Fatalf("无用量记录时头里应只有 total=上限，实际 %q", h)
	}
	if h := s.subUserinfoHeaderForUser(key, 0, "total", "", map[int]bool{}); h != "" {
		t.Fatalf("三项全无时应返回空串，实际 %q", h)
	}
	rec := s.userTraffic.get(key)
	rec.UpTotal, rec.DownTotal = 1*1024*1024*1024, 2*1024*1024*1024
	rec.UpPeriod, rec.DownPeriod = 100*1024*1024, 200*1024*1024
	// total 模式：用累计
	h := s.subUserinfoHeaderForUser(key, limit, "total", "", map[int]bool{})
	if !strings.Contains(h, "upload=1073741824") || !strings.Contains(h, "download=2147483648") || !strings.Contains(h, "total=10737418240") {
		t.Fatalf("total 模式头应用累计用量与上限，实际 %q", h)
	}
	// monthly 模式：用本期
	h = s.subUserinfoHeaderForUser(key, limit, "monthly", "", map[int]bool{})
	if !strings.Contains(h, "upload=104857600") || !strings.Contains(h, "download=209715200") || !strings.Contains(h, "total=10737418240") {
		t.Fatalf("monthly 模式头应用本期用量与上限，实际 %q", h)
	}
}

// ---------- 集中管理：下发替换 + 上报汇总 ----------

func callCentral1325(t *testing.T, s *Server, tok, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(path, "/api/v1/central/user-forwards") && method == "POST":
		s.requireCentralToken(s.handleCentralUserForwards)(rec, req)
	case strings.HasPrefix(path, "/api/v1/central/user-traffic"):
		s.requireCentralToken(s.handleCentralUserTraffic)(rec, req)
	default:
		t.Fatalf("未接的 central 路径 %s %s", method, path)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestCentralUserForwards1325(t *testing.T) {
	fakeSystemctl122(t)
	s := newTestSubServer(t)
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	// 没令牌 → 401
	req := httptest.NewRequest("POST", "/api/v1/central/user-forwards", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	s.requireCentralToken(s.handleCentralUserForwards)(rec, req)
	if rec.Code != 401 {
		t.Fatalf("没令牌应 401，实际 %d", rec.Code)
	}
	// 下发一条转发
	body := `{"key":"subuser:su-abc123","sub_name":"测试用户","forwards":[{"landing_addr":"203.0.113.1","listen_port":30001,"limit_bytes":10737418240,"limit_mode":"monthly","reset_mode":"monthly","reset_day":1,"next_reset_at":"2026-12-01T00:00:00Z"}]}`
	code, resp := callCentral1325(t, s, tok, "POST", "/api/v1/central/user-forwards", body)
	if code != 200 || resp["applied"] != float64(1) || resp["skipped"] != float64(0) {
		t.Fatalf("下发应 applied=1 skipped=0，实际 code=%d resp=%v", code, resp)
	}
	if resp["panel_version"] != Version {
		t.Fatalf("下发响应应带 panel_version，实际 %v", resp)
	}
	fw := s.userForwardsFor("subuser:su-abc123")
	if len(fw) != 1 || fw[0].ListenPort != 30001 || fw[0].Panel != "local" {
		t.Fatalf("下发后本面板应有 1 条转发，实际 %+v", fw)
	}
	// 冲突端口跳过：另一个订阅想用已被占的 30001
	body2 := `{"key":"merged:ms-xyz789","sub_name":"另一个","forwards":[{"landing_addr":"203.0.113.1","listen_port":30001,"limit_bytes":10737418240,"limit_mode":"monthly","reset_mode":"monthly","reset_day":1,"next_reset_at":"2026-12-01T00:00:00Z"},{"landing_addr":"203.0.113.2","listen_port":30002,"limit_bytes":10737418240,"limit_mode":"monthly","reset_mode":"monthly","reset_day":1,"next_reset_at":"2026-12-01T00:00:00Z"}]}`
	code, resp = callCentral1325(t, s, tok, "POST", "/api/v1/central/user-forwards", body2)
	if code != 200 || resp["applied"] != float64(1) || resp["skipped"] != float64(1) {
		t.Fatalf("冲突端口应跳过 1 条、应用 1 条，实际 code=%d resp=%v", code, resp)
	}
	// 不存在的落地跳过
	body3 := `{"key":"merged:ms-xyz789","sub_name":"另一个","forwards":[{"landing_addr":"198.51.100.99","listen_port":30003,"limit_bytes":10737418240,"limit_mode":"monthly","reset_mode":"monthly","reset_day":1,"next_reset_at":"2026-12-01T00:00:00Z"}]}`
	code, resp = callCentral1325(t, s, tok, "POST", "/api/v1/central/user-forwards", body3)
	if code != 200 || resp["applied"] != float64(0) || resp["skipped"] != float64(1) {
		t.Fatalf("不存在的落地应跳过，实际 code=%d resp=%v", code, resp)
	}
	// 空集 = 清掉该订阅在本面板上的全部转发
	code, resp = callCentral1325(t, s, tok, "POST", "/api/v1/central/user-forwards", `{"key":"subuser:su-abc123","sub_name":"测试用户","forwards":[]}`)
	if code != 200 || resp["applied"] != float64(0) {
		t.Fatalf("空集下发应清掉转发，实际 code=%d resp=%v", code, resp)
	}
	if fw := s.userForwardsFor("subuser:su-abc123"); len(fw) != 0 {
		t.Fatalf("空集下发后应无转发，实际 %d 条", len(fw))
	}
}

func TestCentralUserTraffic1325(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateRemoteToken()
	s.cfg.RemoteTokenHash = hashRemoteToken(tok)
	key := "subuser:su-abc123"
	rec := s.userTraffic.get(key)
	rec.UpTotal, rec.DownTotal = 1*1024*1024*1024, 2*1024*1024*1024
	rec.UpPeriod, rec.DownPeriod = 100*1024*1024, 200*1024*1024
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{
		Key: key, SubKind: "subuser", SubID: "su-abc123", Panel: "local",
		LandingAddr: "203.0.113.1", ListenPort: 30001, LimitBytes: 10 * 1024 * 1024 * 1024, LimitMode: "monthly",
	})
	code, resp := callCentral1325(t, s, tok, "GET", "/api/v1/central/user-traffic?key="+key, "")
	if code != 200 {
		t.Fatalf("上报接口应 200，实际 %d resp=%v", code, resp)
	}
	if resp["has_data"] != true || resp["up_total"] != float64(1*1024*1024*1024) || resp["down_total"] != float64(2*1024*1024*1024) {
		t.Fatalf("上报应含本面板累计用量，实际 %v", resp)
	}
	if resp["up_period"] != float64(100*1024*1024) || resp["down_period"] != float64(200*1024*1024) {
		t.Fatalf("上报应含本期用量，实际 %v", resp)
	}
	if resp["limit_bytes"] != float64(10*1024*1024*1024) || resp["limit_mode"] != "monthly" {
		t.Fatalf("上报应含转发上的配额，实际 %v", resp)
	}
	fw, _ := resp["forwards"].([]any)
	if len(fw) != 1 {
		t.Fatalf("上报应含 1 条转发，实际 %v", resp["forwards"])
	}
	if resp["panel_version"] != Version {
		t.Fatalf("上报应带 panel_version，实际 %v", resp)
	}
	// 没数据的订阅：has_data=false
	code, resp = callCentral1325(t, s, tok, "GET", "/api/v1/central/user-traffic?key=subuser:su-nodata", "")
	if code != 200 || resp["has_data"] != false {
		t.Fatalf("没数据的订阅应 has_data=false，实际 code=%d resp=%v", code, resp)
	}
	// 标识不对：400
	code, _ = callCentral1325(t, s, tok, "GET", "/api/v1/central/user-traffic?key=badkey", "")
	if code != 400 {
		t.Fatalf("标识不对应 400，实际 %d", code)
	}
}

// ---------- 检测域名：1.3.24 漏的 3 个补上 ----------

func TestIpCheckSuffixes1325(t *testing.T) {
	want := []string{
		// FlClash 8 个
		"www.cloudflare.com", "api.ip.sb", "ipinfo.io", "ipwho.is", "get.geojs.io", "api.country.is", "api.ipquery.io", "ident.me",
		// Verge 3 个
		"ipapi.co", "api.ipapi.is", "ip.api.skk.moe",
		// Clash Party 1 个
		"api.internetdata.io",
		// 1.3.25 补的 3 个：Hiddify 与 FlClash 旧版
		"ifconfig.co", "api.myip.com", "ip-api.com",
	}
	if len(ipCheckSuffixes) != len(want) {
		t.Fatalf("检测域名应是 %d 个，实际 %d 个：%v", len(want), len(ipCheckSuffixes), ipCheckSuffixes)
	}
	have := map[string]bool{}
	for _, d := range ipCheckSuffixes {
		have[d] = true
	}
	for _, d := range want {
		if !have[d] {
			t.Fatalf("检测域名缺 %s，实际 %v", d, ipCheckSuffixes)
		}
	}
	// ipapi.co 与 ip-api.com 是两家，都要有，别混淆
	if !have["ipapi.co"] || !have["ip-api.com"] {
		t.Fatalf("ipapi.co 与 ip-api.com 都要有，实际 %v", ipCheckSuffixes)
	}
}

// （版本号由 panel_1323_test.go 的 TestVersion1325 覆盖。）

// ---------- 集中管理版本门：远程低于 1.3.25 走共享端口、不限流 ----------

func TestPushUserForwardsVersionGate1325(t *testing.T) {
	s := newTestSubServer(t)
	fw := []centralUserForward{{
		LandingAddr: "203.0.113.1", ListenPort: 30001, LimitBytes: 10 * 1024 * 1024 * 1024,
		LimitMode: "monthly", ResetMode: "monthly", ResetDay: 1, NextResetAt: "2026-12-01T00:00:00Z",
	}}
	// 1.3.25 的远程：正常接受下发
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/central/user-forwards" || r.Method != "POST" {
			t.Errorf("下发应 POST /api/v1/central/user-forwards，实际 %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer rpt_test" {
			t.Errorf("下发应带远程管理令牌，实际 %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, 200, okResp(map[string]any{"applied": 1, "skipped": 0, "panel_version": Version}))
	}))
	defer srv.Close()

	// 1. 缓存里版本是 1.3.24 → 直接拒，连远程都不调
	s.centralMu.Lock()
	s.centralCache["rm-old"] = &RemoteStatusSnapshot{ID: "rm-old", Version: "1.3.24"}
	s.centralMu.Unlock()
	remOld := RemotePanel{ID: "rm-old", Name: "旧面板", BaseURL: srv.URL, Token: "rpt_test"}
	_, _, err := s.pushUserForwardsToRemote(remOld, "subuser:su-abc123", "测试", fw)
	if err == nil || !strings.Contains(err.Error(), "版本太旧") || !strings.Contains(err.Error(), "1.3.24") {
		t.Fatalf("缓存版本 1.3.24 应拒并写明版本，实际 err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("版本门拒了就不应调远程，实际调了 %d 次", calls)
	}

	// 2. 没缓存、远程 404（老面板没这个接口）→ 版本太旧（需要 1.3.25+）
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv404.Close()
	rem404 := RemotePanel{ID: "rm-404", Name: "老面板", BaseURL: srv404.URL, Token: "rpt_test"}
	_, _, err = s.pushUserForwardsToRemote(rem404, "subuser:su-abc123", "测试", fw)
	if err == nil || !strings.Contains(err.Error(), "版本太旧") || !strings.Contains(err.Error(), "1.3.25") {
		t.Fatalf("远程 404 应报版本太旧（需要 1.3.25+），实际 err=%v", err)
	}

	// 3. 没缓存、远程 1.3.25 正常接受 → applied/skipped 原样返回
	remNew := RemotePanel{ID: "rm-new", Name: "新面板", BaseURL: srv.URL, Token: "rpt_test"}
	applied, skipped, err := s.pushUserForwardsToRemote(remNew, "subuser:su-abc123", "测试", fw)
	if err != nil || applied != 1 || skipped != 0 {
		t.Fatalf("1.3.25 远程应接受下发，实际 applied=%d skipped=%d err=%v", applied, skipped, err)
	}
	if calls != 1 {
		t.Fatalf("新版远程应只调一次，实际 %d 次", calls)
	}

	// 4. 缓存里版本就是 1.3.25 → 放行
	s.centralMu.Lock()
	s.centralCache["rm-new"] = &RemoteStatusSnapshot{ID: "rm-new", Version: "1.3.25"}
	s.centralMu.Unlock()
	if ok, _ := s.remoteVersionOK("rm-new", "1.3.25"); !ok {
		t.Fatalf("缓存版本 1.3.25 应放行")
	}
	// 缓存里没版本（空串）→ 放行（靠远程 404 兜底）
	if ok, _ := s.remoteVersionOK("rm-unknown", "1.3.25"); !ok {
		t.Fatalf("没缓存版本时应放行")
	}
}
