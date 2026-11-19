package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.3.7：订阅令牌加密存下来，随时能重新复制 ----------

// callSubIP137 按指定 IP 调 /sub/（限流按 IP 计，测试里换 IP 用）。
func callSubIP137(t *testing.T, s *Server, tok, ip string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/sub/"+tok, nil)
	req.SetPathValue("token", tok)
	req.RemoteAddr = ip + ":12345"
	rec := httptest.NewRecorder()
	s.handleSub(rec, req)
	return rec
}

// callLink137 直接调取回链接接口（测试里不走 requireAuth 中间件，
// 认证另有 TestSubLinkRequiresAuth137 走完整 mux 覆盖）。
func callLink137(t *testing.T, s *Server, kind, id string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/subscriptions/link?kind=" + kind
	if id != "" {
		target += "&id=" + id
	}
	req := httptest.NewRequest("GET", target, nil)
	req.Host = "203.0.113.1:11111"
	rec := httptest.NewRecorder()
	s.handleSubLink(rec, req)
	return rec
}

// callJSONPath137 直接调带路径参数的 handler（如轮换接口），先 SetPathValue。
func callJSONPath137(t *testing.T, h func(w http.ResponseWriter, r *http.Request), method, target, body, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "203.0.113.1:11111"
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// linkResp137 取回链接接口的响应体（okResp 信封，字段在顶层）。
func linkResp137(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return m
}

func TestSubTokenCrypto137(t *testing.T) {
	s := newTestSubServer(t)
	key, err := s.subTokenKey()
	if err != nil {
		t.Fatalf("生成密钥失败：%v", err)
	}
	if len(key) != 32 {
		t.Fatalf("密钥应为 32 字节，实际 %d", len(key))
	}
	// 密钥文件 600 权限，内容就是 32 字节原值
	p := filepath.Join(s.env.PanelHome, subTokenKeyFile)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("密钥文件不存在：%v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 600，实际 %o", fi.Mode().Perm())
	}
	// 第二次加载拿到同一把密钥
	key2, err := s.subTokenKey()
	if err != nil || string(key2) != string(key) {
		t.Fatalf("第二次加载应拿到同一把密钥：%v", err)
	}
	// 加解密往返
	tok := generateSubToken()
	enc, err := encryptSubToken(key, tok)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if enc == tok || strings.Contains(enc, tok) {
		t.Fatalf("密文不应含明文令牌：%s", enc)
	}
	dec, err := decryptSubToken(key, enc)
	if err != nil || dec != tok {
		t.Fatalf("解密应还原令牌：%v", err)
	}
	// 错密钥解不开
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(i + 1)
	}
	if _, err := decryptSubToken(other, enc); err == nil {
		t.Fatal("错密钥应解不开")
	}
	// 篡改密文解不开
	bad, _ := base64.StdEncoding.DecodeString(enc)
	bad[len(bad)-1] ^= 0x01
	if _, err := decryptSubToken(key, base64.StdEncoding.EncodeToString(bad)); err == nil {
		t.Fatal("篡改过的密文应解不开")
	}
	// 密钥文件损坏时重新生成，旧密文随之解不开（走旧链接提示）
	if err := os.WriteFile(p, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	s2 := newTestSubServer(t)
	if _, err := s2.subTokenKey(); err != nil {
		t.Fatalf("密钥文件损坏时应重新生成：%v", err)
	}
	if _, err := decryptSubToken(mustSubKey137(t, s2), enc); err == nil {
		t.Fatal("密钥换了之后旧密文应解不开")
	}
}

func mustSubKey137(t *testing.T, s *Server) []byte {
	t.Helper()
	k, err := s.subTokenKey()
	if err != nil {
		t.Fatalf("取密钥失败：%v", err)
	}
	return k
}

func TestSubLinkEndpoint137(t *testing.T) {
	s := newTestSubServer(t)

	// ① 通用订阅：生成后随时能取回同一条链接
	rec := callJSON130(t, s.handleSubTokenGenerate, "POST", "/api/v1/settings/subscription/generate", "")
	if rec.Code != 200 {
		t.Fatalf("生成通用订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	genURL := respMap130(t, rec)["url"].(string)
	genTok := tokenFromSubURL130(t, genURL)
	if s.cfg.SubTokenEnc == "" {
		t.Fatal("生成后应存了密文")
	}
	rec = callLink137(t, s, "generic", "")
	if rec.Code != 200 {
		t.Fatalf("取回通用链接应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	m := linkResp137(t, rec)
	if m["legacy"] != false || m["url"] != genURL {
		t.Fatalf("取回的通用链接应与生成时一致：%v", m)
	}
	// 轮换后取回的是新链接，旧链接打不开
	rec = callJSON130(t, s.handleSubTokenGenerate, "POST", "/api/v1/settings/subscription/generate", "")
	if rec.Code != 200 {
		t.Fatalf("轮换通用订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	newURL := respMap130(t, rec)["url"].(string)
	if newURL == genURL {
		t.Fatal("轮换后链接应变了")
	}
	m = linkResp137(t, callLink137(t, s, "generic", ""))
	if m["url"] != newURL {
		t.Fatalf("轮换后取回的应是新链接：%v", m)
	}
	if r := callSubIP137(t, s, genTok, "203.0.113.9"); r.Code != 404 {
		t.Fatalf("旧链接轮换后应 404，实际 %d", r.Code)
	}
	// 旧数据（只有哈希、没有密文）→ legacy 提示，不编链接
	s.cfg.SubTokenEnc = ""
	m = linkResp137(t, callLink137(t, s, "generic", ""))
	if m["legacy"] != true || m["message"] == nil || m["message"] == "" {
		t.Fatalf("旧链接应返回 legacy 提示：%v", m)
	}
	if _, ok := m["url"]; ok {
		t.Fatalf("旧链接不应返回 url：%v", m)
	}
	// 撤销后取回 404
	s.cfg.SubTokenEnc = mustSubEnc137(t, s, newURL)
	rec = callJSON130(t, s.handleSubTokenRevoke, "POST", "/api/v1/settings/subscription/revoke", "")
	if rec.Code != 200 {
		t.Fatalf("撤销通用订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if s.cfg.SubTokenEnc != "" {
		t.Fatal("撤销后密文应清掉")
	}
	if rec := callLink137(t, s, "generic", ""); rec.Code != 404 {
		t.Fatalf("撤销后取回应 404，实际 %d", rec.Code)
	}

	// ② 订阅用户：新建后取回，轮换后取回新链接，旧数据 legacy
	rec = callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三的手机"}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	uid := respMap130(t, rec)["id"].(string)
	userURL := respMap130(t, rec)["url"].(string)
	m = linkResp137(t, callLink137(t, s, "user", uid))
	if m["legacy"] != false || m["url"] != userURL {
		t.Fatalf("取回的每人链接应与新建时一致：%v", m)
	}
	rec = callJSONPath137(t, s.handleSubUsersRotate, "POST", "/api/v1/subscriptions/users/"+uid+"/rotate", "", uid)
	if rec.Code != 200 {
		t.Fatalf("轮换订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	newUserURL := respMap130(t, rec)["url"].(string)
	m = linkResp137(t, callLink137(t, s, "user", uid))
	if m["url"] != newUserURL {
		t.Fatalf("轮换后取回的应是新链接：%v", m)
	}
	s.cfg.SubUsers[0].TokenEnc = ""
	m = linkResp137(t, callLink137(t, s, "user", uid))
	if m["legacy"] != true {
		t.Fatalf("旧的每人链接应返回 legacy：%v", m)
	}
	if rec := callLink137(t, s, "user", "su-不存在"); rec.Code != 404 {
		t.Fatalf("没有的订阅用户应 404，实际 %d", rec.Code)
	}

	// ③ 合并订阅：生成后取回；含远程落地的勾选同样取回主面板的链接
	rec = callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged", `{"selection":[{"panel":"local","landing":1}]}`)
	if rec.Code != 200 {
		t.Fatalf("生成合并订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	mid := respMap130(t, rec)["id"].(string)
	mergedURL := respMap130(t, rec)["url"].(string)
	m = linkResp137(t, callLink137(t, s, "merged", mid))
	if m["legacy"] != false || m["url"] != mergedURL {
		t.Fatalf("取回的合并链接应与生成时一致：%v", m)
	}
	// 远程勾选的合并订阅：令牌仍是主面板的，取回不受远程影响
	remoteTok := generateSubToken()
	remoteEnc, err := s.encryptSubTokenNow(remoteTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-remote137", Name: "远程测试", TokenHash: hashSubToken(remoteTok), TokenEnc: remoteEnc, Selection: []SubSelection{{Panel: "remote-id-x", Landing: 3}}})
	m = linkResp137(t, callLink137(t, s, "merged", "ms-remote137"))
	if m["legacy"] != false || !strings.Contains(m["url"].(string), remoteTok) {
		t.Fatalf("含远程落地的合并订阅也应取回主面板链接：%v", m)
	}
	// 合并订阅轮换后取回新链接，旧数据 legacy
	rec = callJSONPath137(t, s.handleMergedSubsRotate, "POST", "/api/v1/subscriptions/merged/"+mid+"/rotate", "", mid)
	if rec.Code != 200 {
		t.Fatalf("轮换合并订阅应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	newMergedURL := respMap130(t, rec)["url"].(string)
	m = linkResp137(t, callLink137(t, s, "merged", mid))
	if m["url"] != newMergedURL {
		t.Fatalf("轮换后取回的合并链接应是新链接：%v", m)
	}
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].ID == mid {
			s.cfg.MergedSubs[i].TokenEnc = ""
		}
	}
	m = linkResp137(t, callLink137(t, s, "merged", mid))
	if m["legacy"] != true {
		t.Fatalf("旧的合并链接应返回 legacy：%v", m)
	}
	if rec := callLink137(t, s, "merged", "ms-不存在"); rec.Code != 404 {
		t.Fatalf("没有的合并订阅应 404，实际 %d", rec.Code)
	}
	if rec := callLink137(t, s, "bogus", ""); rec.Code != 400 {
		t.Fatalf("非法 kind 应 400，实际 %d", rec.Code)
	}
	// 列表带 has_link：有密文 true，没有 false
	rec = callJSON130(t, s.handleMergedSubsList, "GET", "/api/v1/subscriptions/merged", "")
	if rec.Code != 200 {
		t.Fatalf("列合并订阅应 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"has_link":true`) || !strings.Contains(rec.Body.String(), `"has_link":false`) {
		t.Fatalf("合并列表应同时有 has_link true/false：%s", rec.Body.String())
	}
}

// mustSubEnc137 给一条 URL 重新算密文（测试里补回被清空的密文用）。
func mustSubEnc137(t *testing.T, s *Server, url string) string {
	t.Helper()
	enc, err := s.encryptSubTokenNow(tokenFromSubURL130(t, url))
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	return enc
}

// ---------- 1.3.7：/sub/ 限流 ----------

func TestSubRateLimit137(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	wrongTok := "sub_0000000000000000000000000000000000000000000000000000000000000000"
	ipA, ipB, ipC, ipD := "203.0.113.9", "203.0.113.10", "203.0.113.11", "203.0.113.12"

	// 格式不对的令牌直接 404，不查库（用 ipB 探一次，不占 ipA 的失败计数）
	if r := callSubIP137(t, s, "garbage", ipB); r.Code != 404 {
		t.Fatalf("非法格式令牌应 404，实际 %d", r.Code)
	}
	// 同一 IP 10 次错令牌后，第 11 次返回 429（带 Retry-After）
	for i := 0; i < 10; i++ {
		if r := callSubIP137(t, s, wrongTok, ipA); r.Code != 404 {
			t.Fatalf("第 %d 次错令牌应 404，实际 %d", i+1, r.Code)
		}
	}
	if r := callSubIP137(t, s, wrongTok, ipA); r.Code != 429 {
		t.Fatalf("第 11 次错令牌应 429，实际 %d", r.Code)
	} else if r.Header().Get("Retry-After") == "" {
		t.Fatal("429 应带 Retry-After")
	}
	// 换 IP 不受影响
	if r := callSubIP137(t, s, wrongTok, ipB); r.Code != 404 {
		t.Fatalf("换 IP 的错令牌应 404（不受锁影响），实际 %d", r.Code)
	}
	// 正确令牌始终 200，即使这个 IP 已被锁
	if r := callSubIP137(t, s, tok, ipA); r.Code != 200 {
		t.Fatalf("正确令牌即使 IP 被锁也应 200，实际 %d", r.Code)
	}
	// 成功拉取清掉失败计数：9 次失败后拿对令牌，再错一次仍是 404 不是 429
	for i := 0; i < 9; i++ {
		if r := callSubIP137(t, s, wrongTok, ipC); r.Code != 404 {
			t.Fatalf("IP C 第 %d 次错令牌应 404，实际 %d", i+1, r.Code)
		}
	}
	if r := callSubIP137(t, s, tok, ipC); r.Code != 200 {
		t.Fatalf("IP C 正确令牌应 200，实际 %d", r.Code)
	}
	if r := callSubIP137(t, s, wrongTok, ipC); r.Code != 404 {
		t.Fatalf("成功后计数清零，错一次应 404 不是 429，实际 %d", r.Code)
	}
	// 非法格式也计数：10 次非法格式后同样锁
	for i := 0; i < 10; i++ {
		if r := callSubIP137(t, s, "garbage", ipD); r.Code != 404 {
			t.Fatalf("IP D 第 %d 次非法格式应 404，实际 %d", i+1, r.Code)
		}
	}
	if r := callSubIP137(t, s, "garbage", ipD); r.Code != 429 {
		t.Fatalf("IP D 第 11 次非法格式应 429，实际 %d", r.Code)
	}
	// 审计日志：失败记 IP 不记令牌，成功记哈希前 8 位不记明文
	b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("审计日志不存在：%v", err)
	}
	audit := string(b)
	if strings.Contains(audit, wrongTok) || strings.Contains(audit, tok) {
		t.Fatal("审计日志里不应出现错令牌或真令牌明文")
	}
	if !strings.Contains(audit, ipA) {
		t.Fatal("审计日志里应有失败的 IP")
	}
	sum := sha256.Sum256([]byte(tok))
	prefix := hex.EncodeToString(sum[:])[:8]
	if !strings.Contains(audit, prefix) {
		t.Fatalf("审计日志里应有成功拉取的令牌哈希前 8 位 %s", prefix)
	}
}

// ---------- 1.3.7：取回链接接口要登录；明文令牌不进审计与导出 ----------

func TestSubLinkRequiresAuth137(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	// 没登录 → 401
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/link?kind=generic", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("没登录调取回链接应 401，实际 %d", rec.Code)
	}
	// 登录后 → 200（还没生成是 404，不是 401）
	cookie := loginTestCookie(t, h)
	req = httptest.NewRequest("GET", "/api/v1/subscriptions/link?kind=generic", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("登录后没生成时取回应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestSubPlaintextNotInAuditOrExport137(t *testing.T) {
	s := newTestSubServer(t)
	// 生成三类令牌并轮换一次、取回一次
	rec := callJSON130(t, s.handleSubTokenGenerate, "POST", "/api/v1/settings/subscription/generate", "")
	genTok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	rec = callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三"}`)
	userTok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	uid := respMap130(t, rec)["id"].(string)
	rec = callJSON130(t, s.handleMergedSubsCreate, "POST", "/api/v1/subscriptions/merged", `{"selection":[{"panel":"local","landing":1}]}`)
	mergedTok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	mid := respMap130(t, rec)["id"].(string)
	_ = callLink137(t, s, "generic", "")
	_ = callLink137(t, s, "user", uid)
	_ = callLink137(t, s, "merged", mid)
	// 错令牌拉取一次（失败也记审计）
	_ = callSubIP137(t, s, "sub_0000000000000000000000000000000000000000000000000000000000000000", "203.0.113.9")
	// 审计日志里没有任何一条明文令牌
	b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("审计日志不存在：%v", err)
	}
	for _, tok := range []string{genTok, userTok, mergedTok} {
		if strings.Contains(string(b), tok) {
			t.Fatalf("审计日志里不应出现明文令牌 %s…", tok[:12])
		}
	}
	// 导出里没有密文与哈希，也没有明文令牌
	rec = callJSON130(t, s.handleExport, "GET", "/api/v1/export", "")
	if rec.Code != 200 {
		t.Fatalf("导出应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, bad := range []string{"token_enc", "token_hash", genTok, userTok, mergedTok} {
		if strings.Contains(body, bad) {
			t.Fatalf("导出里不应出现 %q", bad)
		}
	}
}

// ---------- 1.3.7：二维码用取回的链接生成（入口 ⑤） ----------

func TestSubQRFromRelink137(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubTokenGenerate, "POST", "/api/v1/settings/subscription/generate", "")
	genURL := respMap130(t, rec)["url"].(string)
	// 先取回链接，再拿这条链接要二维码
	m := linkResp137(t, callLink137(t, s, "generic", ""))
	if m["url"] != genURL {
		t.Fatalf("取回的链接应与生成时一致：%v", m)
	}
	qrBody, _ := json.Marshal(map[string]string{"url": m["url"].(string)})
	req := httptest.NewRequest("POST", "/api/v1/subscription/qr", strings.NewReader(string(qrBody)))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	s.handleSubQR(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("用取回的链接生成二维码应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
	}
	var qr map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &qr); err != nil {
		t.Fatalf("二维码响应不是 JSON：%v", err)
	}
	if q, _ := qr["qr"].(string); !strings.HasPrefix(q, "data:image/png;base64,") {
		t.Fatal("二维码响应里应有 data:image/png;base64,...")
	}
}

// ---------- 1.3.7：定时备份恢复时 TokenEnc 与 TokenHash 一起保留 ----------

func TestBackupRestoreKeepsTokenEnc137(t *testing.T) {
	s := newTestSubServer(t)
	validToml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(validToml), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.SubUsers = []SubUser{
		{ID: "u1", Name: "张三", TokenHash: "hash-aaa", TokenEnc: "enc-aaa", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "u3", Name: "李四", TokenHash: "hash-ddd", TokenEnc: "enc-ddd", CreatedAt: "2026-01-01T00:00:00Z"},
	}
	s.cfg.MergedSubs = []MergedSub{{ID: "m1", Name: "合并", TokenHash: "hash-bbb", TokenEnc: "enc-bbb", CreatedAt: "2026-01-01T00:00:00Z", Selection: []SubSelection{{Landing: 1}}}}
	id, err := s.runScheduledBackup("恢复密文测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	// 备份文件里不含任何令牌哈希与密文（sanitize 结构体本来就没有 token 字段）
	raw, _ := os.ReadFile(filepath.Join(s.scheduledBackupDir(), "backup-"+id+".json"))
	if strings.Contains(string(raw), "token_enc") || strings.Contains(string(raw), "token_hash") ||
		strings.Contains(string(raw), "enc-aaa") || strings.Contains(string(raw), "hash-ddd") {
		t.Fatalf("备份文件里不应有令牌哈希/密文，实际：%s", raw)
	}
	// 改掉现状：u1 删了（恢复后应回来，但密文为空、须轮换）；u3 留着；备份后新增 u2、m2
	s.cfg.SubUsers = []SubUser{
		{ID: "u3", Name: "李四", TokenHash: "hash-ddd", TokenEnc: "enc-ddd", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "u2", Name: "备份后新增", TokenHash: "hash-ccc", TokenEnc: "enc-ccc"},
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "m2", Name: "备份后新增", TokenHash: "hash-eee", TokenEnc: "enc-eee"})
	req := httptest.NewRequest("POST", "/api/v1/scheduled-backups/"+id+"/restore", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.handleScheduledBackupRestore(rec, req)
	if rec.Code != 200 {
		t.Fatalf("恢复应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	byID := map[string]SubUser{}
	for _, u := range s.cfg.SubUsers {
		byID[u.ID] = u
	}
	if u1, ok := byID["u1"]; !ok || u1.TokenHash != "" || u1.TokenEnc != "" {
		t.Fatalf("u1 应恢复回来且哈希/密文都为空（须轮换），实际 %+v", u1)
	}
	if u3, ok := byID["u3"]; !ok || u3.TokenHash != "hash-ddd" || u3.TokenEnc != "enc-ddd" {
		t.Fatalf("还在的 u3 应保留原哈希与密文（恢复后仍能重新复制），实际 %+v", u3)
	}
	if _, ok := byID["u2"]; ok {
		t.Fatalf("备份后新增的 u2 应被移除，实际 %+v", s.cfg.SubUsers)
	}
	if len(s.cfg.MergedSubs) != 1 || s.cfg.MergedSubs[0].ID != "m1" ||
		s.cfg.MergedSubs[0].TokenHash != "hash-bbb" || s.cfg.MergedSubs[0].TokenEnc != "enc-bbb" {
		t.Fatalf("合并订阅应恢复成 m1 且保留原哈希与密文，实际 %+v", s.cfg.MergedSubs)
	}
}
