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
)

// ---------- 1.4.40：旧合并订阅彻底移除（用户 2026-11-28 13:53 拍板：
// B 方案定的旧链接直接失效，失效的就直接移除，按最新的方案做） ----------
//
// 三件事：① 顶栏 .scrolled 磨砂 0.65→0.92（jsdom test-1440.js 覆盖）；
// ② 已退场的旧合并订阅记录连同展示区一起移除（后端、备份/恢复、前端
// 全清，只留独立体系 IndependentMergedSub）；③ 登录页品牌块去掉深藏青
// 底＋金字（jsdom test-1440.js 覆盖）。前端行为由 jsdom test-1440.js
// 与更新后的 test-1427/1429/1434/1436/1437/1438 覆盖。

func TestVersion1440(t *testing.T) {
	if Version != "1.4.42" {
		t.Fatalf("Version 应为 1.4.40，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.42" {
		t.Fatalf("VERSION 文件应为 1.4.40，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// TestOldMergedSubMigration1440：带 merged_subs 的旧 config.json 加载后，
// 文件里 merged_subs 键被清掉（落盘重写），其他字段原样保留。
func TestOldMergedSubMigration1440(t *testing.T) {
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	home := t.TempDir()
	raw := `{"username":"admin","password_hash":"x","port":11111,"merged_subs":[` +
		`{"id":"ms-old1","name":"旧合订一","token_hash":"hash-1"},` +
		`{"id":"ms-old2","name":"旧合订二","token_hash":"hash-2"}]}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := newServer(Env{PanelHome: home, RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("带 merged_subs 的旧配置应能启动：%v", err)
	}
	_ = s
	after, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "merged_subs") {
		t.Fatalf("迁移后 config.json 里不应再有 merged_subs 键：%s", string(after))
	}
	var cfg map[string]any
	if err := json.Unmarshal(after, &cfg); err != nil {
		t.Fatalf("迁移后 config.json 应仍是合法 JSON：%v", err)
	}
	if cfg["username"] != "admin" {
		t.Fatalf("迁移不应动其他字段，username 实际 %v", cfg["username"])
	}
}

// TestOldMergedRouteGone1440：旧合并订阅管理接口已撤——GET
// /api/v1/subscriptions/merged 不再返回列表 JSON，而是落到 SPA 兜底
// （text/html 首页，与其他未注册路径一致）；旧 sub_ 令牌拉取 /sub/
// 与 /m/ 都走 404（与随机错令牌一样，不再 410）。
func TestOldMergedRouteGone1440(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	req := httptest.NewRequest("GET", "/api/v1/subscriptions/merged", nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("旧合并订阅接口已撤，应落到 SPA 兜底（text/html），实际 Content-Type=%q 状态=%d 体=%q",
			rec.Header().Get("Content-Type"), rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if strings.Contains(rec.Body.String(), `"subs"`) {
		t.Fatalf("旧接口不应再返回列表 JSON：%q", rec.Body.String()[:min(200, rec.Body.Len())])
	}

	oldTok := "sub_" + strings.Repeat("a1", 32)
	if rec := callSub(t, s, oldTok, "/sub/"+oldTok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("旧令牌 /sub/ 应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if rec := fetchIndMerged1434(t, h, "/m/"+oldTok); rec.Code != http.StatusNotFound {
		t.Fatalf("旧令牌 /m/ 应 404，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// TestGroupRejectsMergedKind1440：分组不再接受 kind=merged（经新建分组
// 接口，400）；kind=user 的真实成员仍能建组（正向对照）。
func TestGroupRejectsMergedKind1440(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	code, resp := postJSON144(t, h, cookie, "/api/v1/subscriptions/groups",
		`{"name":"测试组","members":[{"kind":"merged","id":"ms-ok1440"}]}`)
	if code != 400 {
		t.Fatalf("kind=merged 建分组应 400，实际 %d：%v", code, resp)
	}
	if len(s.cfg.SubGroups) != 0 {
		t.Fatalf("400 后不应建出分组，实际 %+v", s.cfg.SubGroups)
	}

	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-1440", Name: "组员"})
	code, resp = postJSON144(t, h, cookie, "/api/v1/subscriptions/groups",
		`{"name":"测试组","members":[{"kind":"user","id":"su-1440"}]}`)
	if code != 200 {
		t.Fatalf("kind=user 的真实成员建分组应 200，实际 %d：%v", code, resp)
	}
	if len(s.cfg.SubGroups) != 1 || s.cfg.SubGroups[0].Name != "测试组" {
		t.Fatalf("分组应建出 1 条，实际 %+v", s.cfg.SubGroups)
	}
}

// TestBackupHasNoMergedSubs1440：新备份里不再有 merged_subs 键，独立
// 合并订阅仍在 independent_merged_subs 里。
func TestBackupHasNoMergedSubs1440(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1440", Name: "备份测试", TokenHash: "hash-1440"}}
	data := s.buildScheduledBackupData("测试备份")
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("备份序列化失败：%v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("备份应是合法 JSON：%v", err)
	}
	if _, ok := m["merged_subs"]; ok {
		t.Fatalf("备份里不应再有 merged_subs 键")
	}
	ims, _ := m["independent_merged_subs"].([]any)
	if len(ims) != 1 {
		t.Fatalf("备份里独立合并订阅应有 1 条，实际 %v", m["independent_merged_subs"])
	}
}

// TestOldBackupRestoreIgnoresMergedSubs1440：旧备份文件里残留 merged_subs
// 键时，恢复照常成功（200）、未知键被忽略，独立合并订阅原样回来。
func TestOldBackupRestoreIgnoresMergedSubs1440(t *testing.T) {
	s := newTestSubServer(t)
	validToml := "[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte(validToml), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", Name: "独立合并", TokenHash: "hash-bbb", CreatedAt: "2026-01-01T00:00:00Z", Selection: []SubSelection{{Panel: "local", Landing: 1}}}}
	id, err := s.runScheduledBackup("恢复测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	// 往备份文件里塞一条旧 merged_subs（模拟 1.4.39 及之前版本的备份）。
	path := filepath.Join(s.scheduledBackupDir(), id+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("备份文件应是合法 JSON：%v", err)
	}
	if _, ok := m["merged_subs"]; ok {
		t.Fatalf("新备份本来就不应有 merged_subs 键")
	}
	m["merged_subs"] = []any{map[string]any{"id": "ms-old1", "name": "旧合订", "token_hash": "hash-old"}}
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/scheduled-backups/"+id+"/restore", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.handleScheduledBackupRestore(rec, req)
	if rec.Code != 200 {
		t.Fatalf("带旧 merged_subs 键的备份恢复应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if len(s.cfg.IndependentMergedSubs) != 1 || s.cfg.IndependentMergedSubs[0].ID != "ims-1" {
		t.Fatalf("恢复后独立合并订阅应原样回来，实际 %+v", s.cfg.IndependentMergedSubs)
	}
}

// TestIndMergedCreateSelectFetch1440：核查 A 的后端部分——新建独立合并
// 订阅（空）→ 勾选本面板落地 1 → /m/ 链接拉到该落地的两条 vless 链接。
// 这是旧合并订阅移除后唯一的合并订阅入口，必须全程通。
func TestIndMergedCreateSelectFetch1440(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)

	id, url := createIndMerged1434(t, h, cookie, `{"name":"核查A"}`)
	// 勾选落地（新建弹窗第 2 步“去勾选落地”走同一接口）。
	code, resp := putJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id,
		`{"selection":[{"panel":"local","landing":1}]}`)
	if code != 200 {
		t.Fatalf("勾选落地状态=%d want 200：%v", code, resp)
	}
	if ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id); ims == nil || len(ims.Selection) != 1 || ims.Selection[0].Landing != 1 {
		t.Fatalf("勾选后记录里应有落地 1，实际 %+v", ims)
	}
	// 列表行不再是空订阅。
	code, resp = getJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged")
	if code != 200 {
		t.Fatalf("列表状态=%d want 200：%v", code, resp)
	}
	list, _ := resp["subs"].([]any)
	if len(list) != 1 {
		t.Fatalf("列表应有 1 条，实际 %d", len(list))
	}
	row, _ := list[0].(map[string]any)
	if row["empty"] != false || row["summary"] == "尚未勾选落地或后端节点" {
		t.Fatalf("勾选后列表行不应再是空订阅：%v", row)
	}
	// /m/ 拉取：base64 解码后是落地 1 的两条 vless 链接。
	rec := fetchIndMerged1434(t, h, url)
	if rec.Code != 200 {
		t.Fatalf("/m/ 拉取状态=%d want 200：%s", rec.Code, rec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("/m/ 内容应是 base64 列表：%v（实际 %q）", err, rec.Body.String())
	}
	lines := strings.Split(strings.TrimRight(string(decoded), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("/m/ 拉取应有落地 1 的 2 条链接，实际 %d：%v", len(lines), lines)
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "vless://") {
			t.Fatalf("拉到的链接应是 vless 链接：%s", ln)
		}
	}
	if !strings.Contains(lines[0], "example.com:24443") || !strings.Contains(lines[0], "中转") {
		t.Fatalf("第 1 条应是落地 1 的中转链接：%s", lines[0])
	}
	if !strings.Contains(lines[1], "203.0.113.1:24443") || !strings.Contains(lines[1], "直连") {
		t.Fatalf("第 2 条应是落地 1 的直连链接：%s", lines[1])
	}
}

// putJSON144 经 PUT 发 JSON（1440 新增；1434 的脚手架只有 POST/GET）。
func putJSON144(t *testing.T, h http.Handler, cookie, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}
