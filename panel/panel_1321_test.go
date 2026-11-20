package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1.3.21：订阅设置界面整理 + 登录页公告/签名 ----------

// publicInfoGET 直接调公开接口（不走会话），返回状态码与解析后的 JSON。
func publicInfoGET(t *testing.T, s *Server) (int, map[string]any) {
	t.Helper()
	h := s.routes()
	req := httptest.NewRequest("GET", "/api/v1/public-info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// loginNoticePOST 直接调公告保存接口，返回状态码与解析后的 JSON。
func loginNoticePOST(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/settings/login-notice", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleLoginNoticeSet(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// loginSignaturePOST 直接调签名保存接口，返回状态码与解析后的 JSON。
func loginSignaturePOST(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/settings/login-signature", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleLoginSignatureSet(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

// TestLoginNoticePublic 登录页公告：默认空、公开接口未登录也能读；
// 保存后公开接口读到同一份；清空后公开接口回到空。
func TestLoginNoticePublic(t *testing.T) {
	s := newTestSubServer(t)
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_notice"] != "" {
		t.Fatalf("默认公告应为空、公开接口应 200，实际 %d %v", code, resp)
	}
	if code, resp := loginNoticePOST(t, s, `{"notice":"今晚 22:00 维护半小时"}`); code != 200 || resp["login_notice"] != "今晚 22:00 维护半小时" {
		t.Fatalf("保存公告应 200 并回显，实际 %d %v", code, resp)
	}
	if s.cfg.LoginNotice != "今晚 22:00 维护半小时" {
		t.Fatalf("公告应写进 config，实际 %q", s.cfg.LoginNotice)
	}
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_notice"] != "今晚 22:00 维护半小时" {
		t.Fatalf("公开接口应读到已保存的公告，实际 %d %v", code, resp)
	}
	if code, resp := loginNoticePOST(t, s, `{"notice":""}`); code != 200 || resp["login_notice"] != "" {
		t.Fatalf("清空公告应 200 并回显空，实际 %d %v", code, resp)
	}
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_notice"] != "" {
		t.Fatalf("清空后公开接口应读到空，实际 %d %v", code, resp)
	}
}

// TestLoginNoticeValidation 公告校验：超 500 字、含控制字符都 400。
func TestLoginNoticeValidation(t *testing.T) {
	s := newTestSubServer(t)
	long := strings.Repeat("字", 501)
	if code, _ := loginNoticePOST(t, s, `{"notice":"`+long+`"}`); code != 400 {
		t.Fatalf("501 字公告应 400，实际 %d", code)
	}
	if code, _ := loginNoticePOST(t, s, `{"notice":"a\u0001b"}`); code != 400 {
		t.Fatalf("含控制字符的公告应 400，实际 %d", code)
	}
	// 500 字整、多行文本都合法。
	if code, _ := loginNoticePOST(t, s, `{"notice":"`+strings.Repeat("字", 500)+`"}`); code != 200 {
		t.Fatalf("500 字公告应 200，实际 %d", code)
	}
	if code, _ := loginNoticePOST(t, s, `{"notice":"第一行\n第二行"}`); code != 200 {
		t.Fatalf("多行公告应 200，实际 %d", code)
	}
}

// TestLoginSignaturePublic 登录页签名：默认空、公开接口未登录也能读；
// 保存后公开接口读到同一份；清空后公开接口回到空。
func TestLoginSignaturePublic(t *testing.T) {
	s := newTestSubServer(t)
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_signature"] != "" {
		t.Fatalf("默认签名应为空、公开接口应 200，实际 %d %v", code, resp)
	}
	if code, resp := loginSignaturePOST(t, s, `{"signature":"欢迎回来"}`); code != 200 || resp["login_signature"] != "欢迎回来" {
		t.Fatalf("保存签名应 200 并回显，实际 %d %v", code, resp)
	}
	if s.cfg.LoginSignature != "欢迎回来" {
		t.Fatalf("签名应写进 config，实际 %q", s.cfg.LoginSignature)
	}
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_signature"] != "欢迎回来" {
		t.Fatalf("公开接口应读到已保存的签名，实际 %d %v", code, resp)
	}
	if code, resp := loginSignaturePOST(t, s, `{"signature":""}`); code != 200 || resp["login_signature"] != "" {
		t.Fatalf("清空签名应 200 并回显空，实际 %d %v", code, resp)
	}
	if code, resp := publicInfoGET(t, s); code != 200 || resp["login_signature"] != "" {
		t.Fatalf("清空后公开接口应读到空，实际 %d %v", code, resp)
	}
}

// TestLoginSignatureValidation 签名校验：超 60 字、含换行都 400。
func TestLoginSignatureValidation(t *testing.T) {
	s := newTestSubServer(t)
	long := strings.Repeat("字", 61)
	if code, _ := loginSignaturePOST(t, s, `{"signature":"`+long+`"}`); code != 400 {
		t.Fatalf("61 字签名应 400，实际 %d", code)
	}
	if code, _ := loginSignaturePOST(t, s, `{"signature":"第一行\n第二行"}`); code != 400 {
		t.Fatalf("含换行的签名应 400，实际 %d", code)
	}
	if code, _ := loginSignaturePOST(t, s, `{"signature":"`+strings.Repeat("字", 60)+`"}`); code != 200 {
		t.Fatalf("60 字签名应 200，实际 %d", code)
	}
}

// TestLoginNoticeSignatureInSettings 设置接口下发 login_notice 与
// login_signature（设置页回填用），保存后跟着变。
func TestLoginNoticeSignatureInSettings(t *testing.T) {
	s := newTestSubServer(t)
	if code, _ := loginNoticePOST(t, s, `{"notice":"维护公告"}`); code != 200 {
		t.Fatalf("保存公告应 200，实际 %d", code)
	}
	if code, _ := loginSignaturePOST(t, s, `{"signature":"欢迎回来"}`); code != 200 {
		t.Fatalf("保存签名应 200，实际 %d", code)
	}
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("设置接口应 200，实际 %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["login_notice"] != "维护公告" {
		t.Fatalf("设置里的 login_notice 应是维护公告，实际：%v", resp["login_notice"])
	}
	if resp["login_signature"] != "欢迎回来" {
		t.Fatalf("设置里的 login_signature 应是欢迎回来，实际：%v", resp["login_signature"])
	}
}

// TestLoginNoticeConfigPersistence 公告与签名保存后落盘到 config.json，
// 重读回来一致（更新面板不丢）。
func TestLoginNoticeConfigPersistence(t *testing.T) {
	s := newTestSubServer(t)
	if code, _ := loginNoticePOST(t, s, `{"notice":"维护公告"}`); code != 200 {
		t.Fatalf("保存公告应 200，实际 %d", code)
	}
	if code, _ := loginSignaturePOST(t, s, `{"signature":"欢迎回来"}`); code != 200 {
		t.Fatalf("保存签名应 200，实际 %d", code)
	}
	cfgBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if err != nil {
		t.Fatalf("重读 config.json 应成功：%v", err)
	}
	var cfg PanelConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("解析 config.json 应成功：%v", err)
	}
	if cfg.LoginNotice != "维护公告" {
		t.Fatalf("落盘的公告应是维护公告，实际 %q", cfg.LoginNotice)
	}
	if cfg.LoginSignature != "欢迎回来" {
		t.Fatalf("落盘的签名应是欢迎回来，实际 %q", cfg.LoginSignature)
	}
}
