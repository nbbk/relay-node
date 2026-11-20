package main

// v1.3.18 备份统一成一套：只有一张“备份”卡、一个“立即备份”按钮，
// 手动点与定时到点都走同一条路——先存本机 JSON 备份（scheduled-backups/），
// 再按需传远程（R2/七牛云开了才传，没开就不传）。旧手动备份入口
// （/api/v1/backups 三条路由 + listBackups）已删，旧 backups/ 文件不动。
// 测试：手动备份生成本机文件、远程开启时传、远程关闭时不传、定时备份
// 同样走统一路径、旧 backups/ 文件不动、旧三条路由已删。

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// schedNow1318 直接调“立即备份”处理器（统一入口），返回状态码与 JSON。
func schedNow1318(t *testing.T, s *Server) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/scheduled-backups/now", nil)
	rec := httptest.NewRecorder()
	s.handleScheduledBackupNow(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// countFakeS3Objects1318 假 S3 里现有的对象数。
func countFakeS3Objects1318(f *fakeS3141) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.objects)
}

func TestUnifiedBackupNowCreatesLocal1318(t *testing.T) {
	s := newTestSubServer(t)
	code, resp := schedNow1318(t, s)
	if code != 200 {
		t.Fatalf("立即备份应 200，实际 %d：%v", code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("响应里应有备份 id，实际 %v", resp)
	}
	path := filepath.Join(s.env.PanelHome, "scheduled-backups", id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("本机备份文件应存在：%v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("备份文件应是合法 JSON：%v", err)
	}
	if data["kind"] != "scheduled" {
		t.Fatalf("备份 kind 应是 scheduled，实际 %v", data["kind"])
	}
	// 列表里能看到这份
	list := s.listScheduledBackups()
	found := false
	for _, m := range list {
		if m.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("本机备份列表里应有 %s，实际 %v", id, list)
	}
}

func TestUnifiedBackupRemoteEnabledUploads1318(t *testing.T) {
	origDelay := remoteBackupRetryDelay
	remoteBackupRetryDelay = func(int) time.Duration { return 0 }
	defer func() { remoteBackupRetryDelay = origDelay }()
	s := newTestSubServer(t)
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	setRemotePassphrase141(t, s, "unified-pass-123")
	enableFakeR2141(t, s, srv.URL)
	id, err := s.runScheduledBackup("统一测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	s.uploadBackupToRemote(id)
	if n := countFakeS3Objects1318(fake); n != 1 {
		t.Fatalf("远程开启时应传 1 个对象，实际 %d", n)
	}
	fake.mu.Lock()
	_, ok := fake.objects[remoteObjectKey(id)]
	fake.mu.Unlock()
	if !ok {
		t.Fatalf("假 S3 里应有 %s", remoteObjectKey(id))
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "scheduled-backups", id+".json")); err != nil {
		t.Fatalf("上传后本地备份应还在：%v", err)
	}
}

func TestUnifiedBackupRemoteDisabledSkips1318(t *testing.T) {
	origDelay := remoteBackupRetryDelay
	remoteBackupRetryDelay = func(int) time.Duration { return 0 }
	defer func() { remoteBackupRetryDelay = origDelay }()
	s := newTestSubServer(t)
	fake := newFakeS3141("test-secret")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	setRemotePassphrase141(t, s, "unified-pass-123")
	// 目的地配好但开关关着：端点指向假 S3，enabled=false。
	body, _ := json.Marshal(map[string]any{"r2": map[string]any{
		"enabled": false, "account_id": "test-account", "bucket": "test-bucket",
		"access_key": "test-ak", "secret_key": "test-secret", "endpoint": srv.URL,
	}, "qiniu": map[string]any{"enabled": false}})
	req := httptest.NewRequest("POST", "/api/v1/settings/remote-backup", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleRemoteBackupSettingsPost(rec, req)
	if rec.Code != 200 {
		t.Fatalf("保存远程设置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	id, err := s.runScheduledBackup("统一测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	s.uploadBackupToRemote(id)
	if n := countFakeS3Objects1318(fake); n != 0 {
		t.Fatalf("远程关闭时应 0 个对象，实际 %d", n)
	}
	if _, err := os.Stat(filepath.Join(s.env.PanelHome, "scheduled-backups", id+".json")); err != nil {
		t.Fatalf("本地备份应还在：%v", err)
	}
}

func TestUnifiedBackupScheduledSamePath1318(t *testing.T) {
	s := newTestSubServer(t)
	s.cfg.Backup.Enabled = true
	s.cfg.Backup.Time = "00:00"
	s.cfg.Backup.Keep = 7
	s.maybeScheduledBackup()
	list := s.listScheduledBackups()
	if len(list) != 1 {
		t.Fatalf("定时触发后应有 1 份本机备份，实际 %d", len(list))
	}
	b, err := os.ReadFile(filepath.Join(s.env.PanelHome, "scheduled-backups", list[0].ID+".json"))
	if err != nil {
		t.Fatalf("定时备份文件应存在：%v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("备份文件应是合法 JSON：%v", err)
	}
	if data["label"] != "自动定时备份" {
		t.Fatalf("定时备份 label 应是“自动定时备份”，实际 %v", data["label"])
	}
	if !s.scheduledBackupDoneToday(time.Now().Format("20060102")) {
		t.Fatalf("定时备份跑完后当天应标记已备份")
	}
}

func TestOldBackupsUntouched1318(t *testing.T) {
	s := newTestSubServer(t)
	// 模拟一份旧手动备份快照（backups/ 里的目录 + 文件）。
	oldDir := filepath.Join(s.env.PanelHome, "backups", "20261119-221500-abc123")
	if err := os.MkdirAll(oldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldFiles := map[string]string{
		"meta.json":   `{"id":"20261119-221500-abc123","label":"手动备份"}`,
		"config.toml": "old-config",
		"relay.conf":  "old-relay",
		"state.json":  "old-state",
	}
	for name, content := range oldFiles {
		if err := os.WriteFile(filepath.Join(oldDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.runScheduledBackup("统一测试")
	if err != nil {
		t.Fatalf("备份失败：%v", err)
	}
	s.uploadBackupToRemote(id)
	s.pruneScheduledBackups(7)
	for name, content := range oldFiles {
		b, err := os.ReadFile(filepath.Join(oldDir, name))
		if err != nil {
			t.Fatalf("旧备份文件 %s 应还在：%v", name, err)
		}
		if string(b) != content {
			t.Fatalf("旧备份文件 %s 内容不应被改动", name)
		}
	}
	// 旧快照不在定时备份列表里（两个目录互不干扰）。
	for _, m := range s.listScheduledBackups() {
		if m.ID == "20261119-221500-abc123" {
			t.Fatalf("旧快照不应出现在定时备份列表里")
		}
	}
}

func TestOldBackupRoutesRemoved1318(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	serve := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// 旧三条路由已删：无后缀路径落到前端 SPA 回退，应返回 index.html 外壳
	// （Content-Type text/html、含 <div id="app">），而不是旧接口的 JSON。
	for _, tc := range []struct{ method, target string }{
		{"GET", "/api/v1/backups"},
		{"POST", "/api/v1/backups"},
		{"POST", "/api/v1/backups/20261119-221500-abc123/restore"},
	} {
		rec := serve(tc.method, tc.target)
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s %s 应走 SPA 回退（text/html），实际 Content-Type=%s 状态=%d", tc.method, tc.target, ct, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `<div id="app"`) {
			t.Fatalf("%s %s 应返回 index.html 外壳", tc.method, tc.target)
		}
		if strings.Contains(rec.Body.String(), `"backups"`) {
			t.Fatalf("%s %s 不应再返回备份 JSON", tc.method, tc.target)
		}
	}
	// 统一入口还在：未登录应 401 JSON（路由存在、鉴权拦住），不是 SPA 外壳。
	rec := serve("POST", "/api/v1/scheduled-backups/now")
	if rec.Code != 401 {
		t.Fatalf("POST /api/v1/scheduled-backups/now 未登录应 401，实际 %d", rec.Code)
	}
	rec = serve("GET", "/api/v1/scheduled-backups")
	if rec.Code != 401 {
		t.Fatalf("GET /api/v1/scheduled-backups 未登录应 401，实际 %d", rec.Code)
	}
}
