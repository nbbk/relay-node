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

	// 1.4.27：通用/合并订阅已退场，取链接接口里 generic 与 merged 一律
	// 410；kind=user 恢复解密返回（见 TestSubLinkUserRefresh137）。
	for _, kind := range []string{"generic", "merged"} {
		if rec := callLink137(t, s, kind, ""); rec.Code != 410 {
			t.Fatalf("kind=%s 的取链接应 410，实际 %d：%s", kind, rec.Code, rec.Body.String())
		}
	}
	if rec := callLink137(t, s, "bogus", ""); rec.Code != 400 {
		t.Fatalf("kind=bogus 的取链接应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 不存在的订阅用户 404；没给 id 400。
	if rec := callLink137(t, s, "user", "su-nope"); rec.Code != 404 {
		t.Fatalf("不存在的订阅用户取链接应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if rec := callLink137(t, s, "user", ""); rec.Code != 400 {
		t.Fatalf("不给 id 的取链接应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// 合并订阅列表仍可查（供核对），has_link 按密文有无区分。
	remoteTok := generateSubToken()
	remoteEnc, err := s.encryptSubTokenNow(remoteTok)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.MergedSubs = append(s.cfg.MergedSubs,
		MergedSub{ID: "ms-with137", Name: "有密文", TokenHash: hashSubToken(remoteTok), TokenEnc: remoteEnc, Selection: []SubSelection{{Panel: "local", Landing: 1}}},
		MergedSub{ID: "ms-no137", Name: "无密文", TokenHash: "hash-x", Selection: []SubSelection{{Panel: "local", Landing: 1}}},
	)
	rec := callJSON130(t, s.handleMergedSubsList, "GET", "/api/v1/subscriptions/merged", "")
	if rec.Code != 200 {
		t.Fatalf("列合并订阅应 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"has_link":true`) || !strings.Contains(rec.Body.String(), `"has_link":false`) {
		t.Fatalf("合并列表应同时有 has_link true/false：%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"retired":true`) {
		t.Fatalf("合并列表应标 retired:true：%s", rec.Body.String())
	}
}

// TestSubLinkUserRefresh137 刷新后重新复制/二维码：创建订阅用户后不依赖
// 创建回包，再调取链接接口能拿到同一条可用链接（令牌与创建时同一条，
// /sub/ 能拉到内容）；没密文的旧记录走 legacy 提示。
func TestSubLinkUserRefresh137(t *testing.T) {
	s := newTestSubServer(t)
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三"}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	created := respMap130(t, rec)
	createdURL, _ := created["url"].(string)
	uid, _ := created["id"].(string)
	if createdURL == "" || uid == "" {
		t.Fatalf("创建回包应有 url 与 id，实际 %v", created)
	}
	// 不用创建回包里的链接，重新取一次：应拿到同一条链接。
	rec2 := callLink137(t, s, "user", uid)
	if rec2.Code != 200 {
		t.Fatalf("重新取订阅用户链接应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
	}
	m := linkResp137(t, rec2)
	if m["legacy"] != false {
		t.Fatalf("有密文时 legacy 应为 false，实际 %v", m)
	}
	url, _ := m["url"].(string)
	if url != createdURL {
		t.Fatalf("重新取到的链接应与创建时同一条，创建 %q，取回 %q", createdURL, url)
	}
	// 取到的链接真能用：令牌进 /sub/ 应 200。
	tok := tokenFromSubURL130(t, url)
	if r := callSub(t, s, tok, "/sub/"+tok, ""); r.Code != 200 {
		t.Fatalf("取回的链接令牌拉订阅应 200，实际 %d", r.Code)
	}
	// 走完整路由 + 登录会话也能取到（只给管理员；未登录仍 401，
	// 见 TestSubLinkRequiresAuth137）。链接按请求 Host 拼，路由请求
	// 的 Host 与创建请求不同，只比令牌（同一条链接的核心）。
	h := s.routes()
	cookie := loginTestCookie(t, h)
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/link?kind=user&id="+uid, nil)
	req.Header.Set("Cookie", cookie)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req)
	if rec3.Code != 200 {
		t.Fatalf("登录后走路由取订阅用户链接应 200，实际 %d：%s", rec3.Code, rec3.Body.String())
	}
	if m3 := linkResp137(t, rec3); tokenFromSubURL130(t, m3["url"].(string)) != tok {
		t.Fatalf("路由取回的链接令牌应与创建时同一条，实际 %v", m3)
	}
	// 没密文的旧记录：legacy:true，带旧链接提示，不编链接。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-legacy", Name: "旧人", TokenHash: "hash-old", CreatedAt: "2026-01-01T00:00:00Z"})
	rec4 := callLink137(t, s, "user", "su-legacy")
	if rec4.Code != 200 {
		t.Fatalf("旧记录取链接应 200（legacy 提示），实际 %d：%s", rec4.Code, rec4.Body.String())
	}
	m4 := linkResp137(t, rec4)
	if m4["legacy"] != true {
		t.Fatalf("没密文时 legacy 应为 true，实际 %v", m4)
	}
	if msg, _ := m4["message"].(string); msg != legacySubLinkMsg {
		t.Fatalf("旧链接提示不对，实际 %q", msg)
	}
	if _, ok := m4["url"]; ok {
		t.Fatalf("旧链接不应返回 url，实际 %v", m4)
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
	// 1.4.27：正确令牌走 SubUser（通用订阅已退场）。
	subUserForToken1427(t, s, tok)
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
	// 登录后 → 410（取链接接口已随通用/合并订阅退场）。
	cookie := loginTestCookie(t, h)
	req = httptest.NewRequest("GET", "/api/v1/subscriptions/link?kind=generic", nil)
	req.Header.Set("Cookie", cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 410 {
		t.Fatalf("登录后取链接接口应 410（已退场），实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestSubPlaintextNotInAuditOrExport137(t *testing.T) {
	s := newTestSubServer(t)
	// 1.4.27：只剩订阅用户有令牌（通用/合并订阅已退场，不再生成令牌）。
	// 新建一个订阅用户并轮换一次、拉取一次。
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三"}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	userTok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	uid := respMap130(t, rec)["id"].(string)
	rec = callJSONPath137(t, s.handleSubUsersRotate, "POST", "/api/v1/subscriptions/users/"+uid+"/rotate", "", uid)
	if rec.Code != 200 {
		t.Fatalf("轮换订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	newUserTok := tokenFromSubURL130(t, respMap130(t, rec)["url"].(string))
	// 错令牌拉取一次（失败也记审计），对令牌拉取一次（成功记审计）。
	_ = callSubIP137(t, s, "sub_0000000000000000000000000000000000000000000000000000000000000000", "203.0.113.9")
	if r := callSubIP137(t, s, newUserTok, "203.0.113.10"); r.Code != 200 {
		t.Fatalf("新令牌拉取应 200，实际 %d", r.Code)
	}
	// 审计日志里没有任何一条明文令牌
	b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("审计日志不存在：%v", err)
	}
	for _, tok := range []string{userTok, newUserTok} {
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
	for _, bad := range []string{"token_enc", "token_hash", userTok, newUserTok} {
		if strings.Contains(body, bad) {
			t.Fatalf("导出里不应出现 %q", bad)
		}
	}
}

// ---------- 1.3.7：二维码用取回的链接生成（入口 ⑤） ----------

func TestSubQRFromRelink137(t *testing.T) {
	s := newTestSubServer(t)
	// 1.4.27：取链接接口已退场，二维码直接用新建订阅用户回包里的链接生成。
	rec := callJSON130(t, s.handleSubUsersCreate, "POST", "/api/v1/subscriptions/users", `{"name":"张三"}`)
	if rec.Code != 200 {
		t.Fatalf("新建订阅用户应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	userURL := respMap130(t, rec)["url"].(string)
	qrBody, _ := json.Marshal(map[string]string{"url": userURL})
	req := httptest.NewRequest("POST", "/api/v1/subscription/qr", strings.NewReader(string(qrBody)))
	req.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	s.handleSubQR(rec2, req)
	if rec2.Code != 200 {
		t.Fatalf("用订阅用户链接生成二维码应 200，实际 %d：%s", rec2.Code, rec2.Body.String())
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
