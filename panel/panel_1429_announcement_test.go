package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.4.29：面板公告（见 goals/goal-6/announcement-contract.md） ----------

// doJSON1429 走完整路由（带/不带会话 cookie），返回状态码与解析后的 JSON。
func doJSON1429(t *testing.T, h http.Handler, cookie, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
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

// announcements1429 公开拉取公告列表（不走会话），返回状态码与 announcements 数组。
func announcements1429(t *testing.T, h http.Handler) (int, []any) {
	t.Helper()
	code, resp := doJSON1429(t, h, "", "GET", "/api/v1/announcements", "")
	raw, _ := resp["announcements"].([]any)
	return code, raw
}

// createAnnouncement1429 调新建公告接口，返回状态码与响应体。
func createAnnouncement1429(t *testing.T, h http.Handler, cookie, body string) (int, map[string]any) {
	t.Helper()
	return doJSON1429(t, h, cookie, "POST", "/api/v1/announcements", body)
}

// announcementTitles1429 公告数组里的标题（按返回顺序）。
func announcementTitles1429(t *testing.T, list []any) []string {
	t.Helper()
	out := make([]string, 0, len(list))
	for _, r := range list {
		m, _ := r.(map[string]any)
		title, _ := m["title"].(string)
		out = append(out, title)
	}
	return out
}

// TestAnnouncementsPublicEmpty 公开拉取：空列表返回 []（不返回 null），
// 未登录可调（不走鉴权）。
func TestAnnouncementsPublicEmpty(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	code, list := announcements1429(t, h)
	if code != 200 {
		t.Fatalf("公开拉取应 200，实际 %d", code)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("空列表应返回 []，实际 %v", list)
	}
	// 原始响应体里 announcements 必须是 [] 而不是 null。
	req := httptest.NewRequest("GET", "/api/v1/announcements", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"announcements":[]`) {
		t.Fatalf("空列表响应体应含 \"announcements\":[]，实际 %s", rec.Body.String())
	}
}

// TestAnnouncementsCreateList 新建→列表可见：新建返回 id（an-＋6 位 hex）
// 与字段回显，公开列表与 public-info 读到同一条。
func TestAnnouncementsCreateList(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	code, resp := createAnnouncement1429(t, h, cookie, `{"title":"维护通知","body":"今晚 23:00–23:30 升级，期间节点可能短暂重连。","pinned":true}`)
	if code != 200 {
		t.Fatalf("新建公告应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	if len(id) != 9 || !strings.HasPrefix(id, "an-") {
		t.Fatalf("公告 id 应是 an-＋6 位 hex，实际 %q", id)
	}
	if resp["title"] != "维护通知" || resp["body"] != "今晚 23:00–23:30 升级，期间节点可能短暂重连。" || resp["pinned"] != true {
		t.Fatalf("新建回包字段不对：%v", resp)
	}
	if resp["created_at"] == "" || resp["updated_at"] == "" {
		t.Fatalf("新建回包应有 created_at/updated_at：%v", resp)
	}
	if len(s.cfg.Announcements) != 1 || s.cfg.Announcements[0].ID != id {
		t.Fatalf("公告应写进 config，实际 %+v", s.cfg.Announcements)
	}
	if code, list := announcements1429(t, h); code != 200 || len(list) != 1 {
		t.Fatalf("公开列表应有 1 条，实际 %d %v", code, list)
	}
	if code, resp := publicInfoGET(t, s); code != 200 {
		t.Fatalf("public-info 应 200，实际 %d", code)
	} else if raw, _ := resp["announcements"].([]any); len(raw) != 1 {
		t.Fatalf("public-info 里应带 1 条 announcements，实际 %v", resp["announcements"])
	} else if m, _ := raw[0].(map[string]any); m["id"] != id {
		t.Fatalf("public-info 里的公告应与新建的是同一条，实际 %v", raw[0])
	}
	// 审计只记标题不记正文。
	auditBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("读 audit.log 应成功：%v", err)
	}
	if !strings.Contains(string(auditBytes), "新建公告") || !strings.Contains(string(auditBytes), "维护通知") {
		t.Fatalf("审计应记“新建公告”与标题，实际 %s", string(auditBytes))
	}
	if strings.Contains(string(auditBytes), "期间节点可能短暂重连") {
		t.Fatalf("审计不应记公告正文，实际 %s", string(auditBytes))
	}
}

// TestAnnouncementsValidation 标题/内容超限与控制字符都 400；
// 边界值（60/2000 字、含换行/制表）合法；未知字段 400；
// 未登录新建 401。
func TestAnnouncementsValidation(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 未登录新建 401。
	if code, _ := createAnnouncement1429(t, h, "", `{"title":"x","body":"y"}`); code != 401 {
		t.Fatalf("未登录新建应 401，实际 %d", code)
	}
	// 标题空、超 60 字、含控制字符都 400。
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"","body":"正文"}`); code != 400 {
		t.Fatalf("空标题应 400，实际 %d", code)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"`+strings.Repeat("字", 61)+`","body":"正文"}`); code != 400 {
		t.Fatalf("61 字标题应 400，实际 %d", code)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"a`+"\u0001"+`b","body":"正文"}`); code != 400 {
		t.Fatalf("标题含控制字符应 400，实际 %d", code)
	}
	// 内容空、超 2000 字、含控制字符都 400。
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"标题","body":""}`); code != 400 {
		t.Fatalf("空内容应 400，实际 %d", code)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"标题","body":"`+strings.Repeat("字", 2001)+`"}`); code != 400 {
		t.Fatalf("2001 字内容应 400，实际 %d", code)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"标题","body":"a`+"\u0007"+`b"}`); code != 400 {
		t.Fatalf("内容含控制字符应 400，实际 %d", code)
	}
	// 未知字段 400。
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"标题","body":"正文","nope":1}`); code != 400 {
		t.Fatalf("未知字段应 400，实际 %d", code)
	}
	// 边界值合法：60 字标题、2000 字内容、换行/回车/制表都认。
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"`+strings.Repeat("字", 60)+`","body":"`+strings.Repeat("字", 2000)+`"}`); code != 200 {
		t.Fatalf("60 字标题＋2000 字内容应 200，实际 %d", code)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"标题","body":"第一行\n第二行\r第三行\t缩进"}`); code != 200 {
		t.Fatalf("含换行/回车/制表的内容应 200，实际 %d", code)
	}
}

// TestAnnouncementsUpdateDelete 编辑/删除：不存在 404；存在时全量替换
// （pinned 跟着换、created_at 不动、updated_at 刷新），删后列表不再有。
func TestAnnouncementsUpdateDelete(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 编辑/删除不存在的都 404。
	if code, _ := doJSON1429(t, h, cookie, "PUT", "/api/v1/announcements/an-000000", `{"title":"x","body":"y","pinned":false}`); code != 404 {
		t.Fatalf("编辑不存在的公告应 404，实际 %d", code)
	}
	if code, _ := doJSON1429(t, h, cookie, "DELETE", "/api/v1/announcements/an-000000", ""); code != 404 {
		t.Fatalf("删除不存在的公告应 404，实际 %d", code)
	}
	// 未登录编辑/删除 401。
	if code, _ := doJSON1429(t, h, "", "PUT", "/api/v1/announcements/an-000000", `{"title":"x","body":"y"}`); code != 401 {
		t.Fatalf("未登录编辑应 401，实际 %d", code)
	}
	if code, _ := doJSON1429(t, h, "", "DELETE", "/api/v1/announcements/an-000000", ""); code != 401 {
		t.Fatalf("未登录删除应 401，实际 %d", code)
	}
	// 新建一条再编辑：全量替换，created_at 不动。
	_, created := createAnnouncement1429(t, h, cookie, `{"title":"旧标题","body":"旧内容","pinned":false}`)
	id, _ := created["id"].(string)
	createdAt, _ := created["created_at"].(string)
	code, updated := doJSON1429(t, h, cookie, "PUT", "/api/v1/announcements/"+id, `{"title":"新标题","body":"新内容","pinned":true}`)
	if code != 200 {
		t.Fatalf("编辑应 200，实际 %d：%v", code, updated)
	}
	if updated["title"] != "新标题" || updated["body"] != "新内容" || updated["pinned"] != true {
		t.Fatalf("编辑应全量替换，实际 %v", updated)
	}
	if updated["created_at"] != createdAt {
		t.Fatalf("编辑不应改 created_at，实际 %v（新建时 %s）", updated["created_at"], createdAt)
	}
	// 编辑时标题超限同样 400。
	if code, _ := doJSON1429(t, h, cookie, "PUT", "/api/v1/announcements/"+id, `{"title":"`+strings.Repeat("字", 61)+`","body":"新内容"}`); code != 400 {
		t.Fatalf("编辑标题超限应 400，实际 %d", code)
	}
	// 删除后公开列表为空。
	if code, _ := doJSON1429(t, h, cookie, "DELETE", "/api/v1/announcements/"+id, ""); code != 200 {
		t.Fatalf("删除应 200，实际 %d", code)
	}
	if _, list := announcements1429(t, h); len(list) != 0 {
		t.Fatalf("删除后公开列表应为空，实际 %v", list)
	}
	// 审计记编辑/删除（只记标题）。
	auditBytes, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if !strings.Contains(string(auditBytes), "编辑公告") || !strings.Contains(string(auditBytes), "删除公告") {
		t.Fatalf("审计应记编辑公告与删除公告，实际 %s", string(auditBytes))
	}
}

// TestAnnouncementsSort 置顶排序：置顶在前（多条置顶按创建时间倒序），
// 其余按 created_at 倒序（新的在前）。
func TestAnnouncementsSort(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	s.cfg.Announcements = []Announcement{
		{ID: "an-000001", Title: "旧的普通", Body: "b1", CreatedAt: "2026-11-20T09:00:00Z", UpdatedAt: "2026-11-20T09:00:00Z"},
		{ID: "an-000002", Title: "旧的置顶", Body: "b2", Pinned: true, CreatedAt: "2026-11-20T09:00:00Z", UpdatedAt: "2026-11-20T09:00:00Z"},
		{ID: "an-000003", Title: "新的普通", Body: "b3", CreatedAt: "2026-11-26T09:00:00Z", UpdatedAt: "2026-11-26T09:00:00Z"},
		{ID: "an-000004", Title: "新的置顶", Body: "b4", Pinned: true, CreatedAt: "2026-11-26T09:00:00Z", UpdatedAt: "2026-11-26T09:00:00Z"},
	}
	if code, list := announcements1429(t, h); code != 200 {
		t.Fatalf("公开拉取应 200，实际 %d", code)
	} else if got := announcementTitles1429(t, list); len(got) != 4 || got[0] != "新的置顶" || got[1] != "旧的置顶" || got[2] != "新的普通" || got[3] != "旧的普通" {
		t.Fatalf("排序应是 新的置顶/旧的置顶/新的普通/旧的普通，实际 %v", got)
	}
}

// TestAnnouncementsBackupExportRestore 备份导出含 announcements，
// 恢复到全新面板后读回同一条（与 sub_groups 同款顶级字段口径）。
func TestAnnouncementsBackupExportRestore(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 真机上 config.toml 一直有 [[endpoints]]，测试夹具默认是空文件，先补一份最小配置。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"备份公告","body":"随备份走","pinned":true}`); code != 200 {
		t.Fatalf("新建公告应 200，实际 %d", code)
	}
	raw, _ := json.Marshal(s.exportData())
	var exported map[string]any
	_ = json.Unmarshal(raw, &exported)
	items, _ := exported["announcements"].([]any)
	if len(items) != 1 {
		t.Fatalf("备份导出应含 1 条 announcements，实际 %v", exported["announcements"])
	}
	if m, _ := items[0].(map[string]any); m["title"] != "备份公告" || m["body"] != "随备份走" || m["pinned"] != true {
		t.Fatalf("备份里的公告字段不对：%v", items[0])
	}
	var p importPayload
	_ = json.Unmarshal(raw, &p)
	dst := newTestSubServer(t)
	if _, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", auditDetail: "公告备份恢复测试",
		restartWarnPrefix: "导入成功，但重启失败：", user: "test",
	}); fail != nil {
		t.Fatalf("恢复应成功，实际失败：%v", fail)
	}
	if len(dst.cfg.Announcements) != 1 || dst.cfg.Announcements[0].Title != "备份公告" || dst.cfg.Announcements[0].Body != "随备份走" || !dst.cfg.Announcements[0].Pinned {
		t.Fatalf("恢复后公告应读回，实际 %+v", dst.cfg.Announcements)
	}
	if code, list := announcements1429(t, dst.routes()); code != 200 || len(list) != 1 {
		t.Fatalf("恢复后公开拉取应有 1 条，实际 %d %v", code, list)
	}
}

// TestAnnouncementsConfigPersistence 公告保存后落盘到 config.json，
// 重读回来一致（更新面板不丢）。
func TestAnnouncementsConfigPersistence(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	if code, _ := createAnnouncement1429(t, h, cookie, `{"title":"落盘公告","body":"重读一致","pinned":false}`); code != 200 {
		t.Fatalf("新建公告应 200，实际 %d", code)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if err != nil {
		t.Fatalf("重读 config.json 应成功：%v", err)
	}
	var cfg PanelConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("解析 config.json 应成功：%v", err)
	}
	if len(cfg.Announcements) != 1 || cfg.Announcements[0].Title != "落盘公告" {
		t.Fatalf("落盘的公告应是落盘公告，实际 %+v", cfg.Announcements)
	}
}

// TestVersion1429 VERSION 文件是 1.4.29。
func TestVersion1429(t *testing.T) {
	if Version != "1.4.36" {
		t.Fatalf("VERSION 应是 1.4.33，实际 %q", Version)
	}
}
