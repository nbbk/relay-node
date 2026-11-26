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

// ---------- 1.4.29：远程配置 client-config.json ----------
// 管理端保存接口的请求体字段名是 config（与前端 saveClientConfig /
// clearClientConfig 的契约一致），即 {config: "<信封原文>"}。
// 面板不验签，只要求信封形状里有 signature 字段（签名真假由客户端
// 拿编译进的公钥验），所以测试里的 signature 用假字符串即可。

// clientConfigEnvelope1429 一份合法的已签名信封（测试夹具；signature
// 是假字符串，面板只校验字段存在、不验签；域名用 example.com 占位）。
const clientConfigEnvelope1429 = `{"config_version":3,"signature":"FAKE-SIGNATURE-DO-NOT-VERIFY","panel":{"domain":"panel.example.com"},"app_name":"Example"}`

// clientConfigGET1429 公开拉取 /client-config.json（不走会话），返回
// 状态码、原始响应体与 Content-Type。
func clientConfigGET1429(t *testing.T, h http.Handler) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/client-config.json", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header().Get("Content-Type")
}

// clientConfigPOST1429 调管理端保存接口（带/不带会话 cookie），
// body 是完整的请求体 JSON 字符串。
func clientConfigPOST1429(t *testing.T, h http.Handler, cookie, body string) (int, map[string]any) {
	t.Helper()
	return doJSON1429(t, h, cookie, "POST", "/api/v1/settings/client-config", body)
}

// clientConfigSaveBody1429 把信封原文包进 {config: ...} 请求体。
func clientConfigSaveBody1429(t *testing.T, envelope string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"config": envelope})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestClientConfigNotPublished 未发布时公开 GET 返回 404（客户端把
// 404 认作“配置还没发布”，不要回 200 空）。
func TestClientConfigNotPublished(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	if code, body, _ := clientConfigGET1429(t, h); code != 404 {
		t.Fatalf("未发布时 GET 应 404，实际 %d：%s", code, body)
	}
}

// TestClientConfigSaveRequiresAuth 未登录保存 401。
func TestClientConfigSaveRequiresAuth(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	if code, _ := clientConfigPOST1429(t, h, "", clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 401 {
		t.Fatalf("未登录保存应 401，实际 %d", code)
	}
}

// TestClientConfigValidation 形状校验都 400：信封不是合法 JSON、
// 不是对象、缺 signature、signature 为空、缺 config_version、
// config_version 不是数字、缺 panel、panel 不是对象、未知字段；
// 合法信封 200 且回包带 config_version。
func TestClientConfigValidation(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	bad := []struct {
		name string
		body string
	}{
		{"信封不是合法 JSON", `{"config":"{not json"}`},
		{"信封不是对象", `{"config":"[1,2]"}`},
		{"信封不是对象2", `{"config":"\"just a string\" "}`},
		{"缺 signature", `{"config":"{\"config_version\":3,\"panel\":{}}"}`},
		{"signature 为空", `{"config":"{\"config_version\":3,\"signature\":\"\",\"panel\":{}}"}`},
		{"缺 config_version", `{"config":"{\"signature\":\"FAKE\",\"panel\":{}}"}`},
		{"config_version 不是数字", `{"config":"{\"config_version\":\"3\",\"signature\":\"FAKE\",\"panel\":{}}"}`},
		{"缺 panel", `{"config":"{\"config_version\":3,\"signature\":\"FAKE\"}"}`},
		{"panel 不是对象", `{"config":"{\"config_version\":3,\"signature\":\"FAKE\",\"panel\":\"panel.example.com\"}"}`},
		{"未知字段", `{"config":"` + clientConfigEnvelope1429Escaped(t) + `","nope":1}`},
	}
	for _, tc := range bad {
		if code, resp := clientConfigPOST1429(t, h, cookie, tc.body); code != 400 {
			t.Fatalf("%s应 400，实际 %d：%v", tc.name, code, resp)
		}
	}
	// 校验失败不应写进 config。
	if s.cfg.ClientConfigJSON != "" {
		t.Fatalf("校验失败后 config 应仍为空，实际 %q", s.cfg.ClientConfigJSON)
	}
	if code, resp := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("合法信封应 200，实际 %d：%v", code, resp)
	} else if resp["config_version"] != float64(3) {
		t.Fatalf("回包 config_version 应是 3，实际 %v", resp["config_version"])
	}
}

// clientConfigEnvelope1429Escaped 信封原文经 JSON 转义后的形式
// （放进另一个 JSON 字符串里用）。
func clientConfigEnvelope1429Escaped(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(clientConfigEnvelope1429)
	if err != nil {
		t.Fatal(err)
	}
	// 去掉首尾引号，只留转义后的内容。
	return string(b[1 : len(b)-1])
}

// TestClientConfigPublishGetRoundTrip 发布→公开拉取：GET 返回的字节
// 与信封原文完全一致、Content-Type 为 application/json；再发一版
// config_version 4 会替换；落盘到 config.json；审计只记
// config_version，绝不记信封内容。
func TestClientConfigPublishGetRoundTrip(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	if code, _ := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("发布应 200，实际 %d", code)
	}
	if code, body, ct := clientConfigGET1429(t, h); code != 200 {
		t.Fatalf("已发布时 GET 应 200，实际 %d", code)
	} else if body != clientConfigEnvelope1429 {
		t.Fatalf("GET 应原样返回信封字节，实际 %q（期望 %q）", body, clientConfigEnvelope1429)
	} else if ct != "application/json" {
		t.Fatalf("GET 的 Content-Type 应是 application/json，实际 %q", ct)
	}
	// 再发一版 config_version 4：替换旧信封。
	envelope4 := `{"config_version":4,"signature":"FAKE-SIGNATURE-V4","panel":{"domain":"panel.example.com"}}`
	if code, resp := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, envelope4)); code != 200 {
		t.Fatalf("发布 v4 应 200，实际 %d", code)
	} else if resp["config_version"] != float64(4) {
		t.Fatalf("回包 config_version 应是 4，实际 %v", resp["config_version"])
	}
	if code, body, _ := clientConfigGET1429(t, h); code != 200 || body != envelope4 {
		t.Fatalf("替换后 GET 应返回 v4 信封，实际 %d %q", code, body)
	}
	// 落盘：重读 config.json 一致。
	cfgBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if err != nil {
		t.Fatalf("重读 config.json 应成功：%v", err)
	}
	var cfg PanelConfig
	if err := json.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatalf("解析 config.json 应成功：%v", err)
	}
	if cfg.ClientConfigJSON != envelope4 {
		t.Fatalf("落盘的信封应是 v4，实际 %q", cfg.ClientConfigJSON)
	}
	// 审计只记 config_version，不记信封内容（签名与域名都不应出现）。
	auditBytes, err := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if err != nil {
		t.Fatalf("读 audit.log 应成功：%v", err)
	}
	audit := string(auditBytes)
	if !strings.Contains(audit, "发布远程配置") || !strings.Contains(audit, "config_version=3") || !strings.Contains(audit, "config_version=4") {
		t.Fatalf("审计应记“发布远程配置”与 config_version，实际 %s", audit)
	}
	if strings.Contains(audit, "FAKE-SIGNATURE") || strings.Contains(audit, "panel.example.com") {
		t.Fatalf("审计绝不能记信封内容，实际 %s", audit)
	}
}

// TestClientConfigClear 传空字符串即撤下：撤下后 GET 回到 404，
// config 为空；只含空白同样撤下；审计记“撤下远程配置”。
func TestClientConfigClear(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	if code, _ := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("发布应 200，实际 %d", code)
	}
	if code, _ := clientConfigPOST1429(t, h, cookie, `{"config":""}`); code != 200 {
		t.Fatalf("传空字符串撤下应 200，实际 %d", code)
	}
	if s.cfg.ClientConfigJSON != "" {
		t.Fatalf("撤下后 config 应为空，实际 %q", s.cfg.ClientConfigJSON)
	}
	if code, _, _ := clientConfigGET1429(t, h); code != 404 {
		t.Fatalf("撤下后 GET 应 404，实际 %d", code)
	}
	// 只含空白同样撤下（先再发布一次）。
	if code, _ := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("重新发布应 200，实际 %d", code)
	}
	if code, _ := clientConfigPOST1429(t, h, cookie, `{"config":"   "}`); code != 200 {
		t.Fatalf("传空白字符串撤下应 200，实际 %d", code)
	}
	if code, _, _ := clientConfigGET1429(t, h); code != 404 {
		t.Fatalf("空白撤下后 GET 应 404，实际 %d", code)
	}
	auditBytes, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if !strings.Contains(string(auditBytes), "撤下远程配置") {
		t.Fatalf("审计应记“撤下远程配置”，实际 %s", string(auditBytes))
	}
	if strings.Contains(string(auditBytes), "FAKE-SIGNATURE") {
		t.Fatalf("审计绝不能记信封内容，实际 %s", string(auditBytes))
	}
}

// TestClientConfigBackupExportRestore 备份导出含 client_config_json，
// 恢复到全新面板后读回同一份信封（与 announcements 同款顶级字段口径）；
// 老备份（没有这一项）恢复时不动现有值。
func TestClientConfigBackupExportRestore(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	// 真机上 config.toml 一直有 [[endpoints]]，测试夹具默认是空文件，先补一份最小配置。
	if err := os.WriteFile(filepath.Join(s.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("发布应 200，实际 %d", code)
	}
	raw, _ := json.Marshal(s.exportData())
	var exported map[string]any
	_ = json.Unmarshal(raw, &exported)
	if exported["client_config_json"] != clientConfigEnvelope1429 {
		t.Fatalf("备份导出应含 client_config_json 信封原文，实际 %v", exported["client_config_json"])
	}
	var p importPayload
	_ = json.Unmarshal(raw, &p)
	dst := newTestSubServer(t)
	if _, fail := dst.applyImportPayload(p, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", auditDetail: "远程配置备份恢复测试",
		restartWarnPrefix: "导入成功，但重启失败：", user: "test",
	}); fail != nil {
		t.Fatalf("恢复应成功，实际失败：%v", fail)
	}
	if dst.cfg.ClientConfigJSON != clientConfigEnvelope1429 {
		t.Fatalf("恢复后信封应读回，实际 %q", dst.cfg.ClientConfigJSON)
	}
	if code, body, _ := clientConfigGET1429(t, dst.routes()); code != 200 || body != clientConfigEnvelope1429 {
		t.Fatalf("恢复后公开拉取应返回信封原文，实际 %d %q", code, body)
	}
	// 老备份（没有 client_config_json 这一项）恢复时不动现有值：
	// 拿 dst2 自己的导出当底本，只删掉 client_config_json 这一项，
	// 其余字段齐全——这才是一份真实的老备份形状（光一个
	// {"version":...} 的空壳过不了 applyImportPayload 的写盘步）。
	dst2 := newTestSubServer(t)
	dst2.cfg.ClientConfigJSON = clientConfigEnvelope1429
	// dst2 的 config.toml 也是空夹具，先补同一份最小配置，否则
	// 它自己的导出里 config_toml 为空，恢复写盘那步会失败。
	if err := os.WriteFile(filepath.Join(dst2.env.RealmDir, "config.toml"), []byte("[[endpoints]]\nlisten = \"0.0.0.0:24443\"\nremote = \"203.0.113.1:24443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw2, _ := json.Marshal(dst2.exportData())
	var exported2 map[string]any
	_ = json.Unmarshal(raw2, &exported2)
	delete(exported2, "client_config_json")
	raw2, _ = json.Marshal(exported2)
	var oldPayload importPayload
	_ = json.Unmarshal(raw2, &oldPayload)
	if oldPayload.ClientConfigJSON != nil {
		t.Fatalf("老备份的 ClientConfigJSON 指针应是 nil，实际 %v", oldPayload.ClientConfigJSON)
	}
	if _, fail := dst2.applyImportPayload(oldPayload, importApplyOpts{
		preBackupLabel: "测试", auditAction: "测试恢复", auditDetail: "老备份恢复测试",
		restartWarnPrefix: "导入成功，但重启失败：", user: "test",
	}); fail != nil {
		t.Fatalf("老备份恢复应成功，实际失败：%v", fail)
	}
	if dst2.cfg.ClientConfigJSON != clientConfigEnvelope1429 {
		t.Fatalf("老备份恢复不应动现有信封，实际 %q", dst2.cfg.ClientConfigJSON)
	}
}

// TestClientConfigIPWhitelistBypass 公开 GET 不走 IP 白名单（与
// /api/v1/announcements 同款例外）：白名单外 IP 也能拉，已鉴权的
// 接口仍被白名单拦住。
func TestClientConfigIPWhitelistBypass(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	if code, _ := clientConfigPOST1429(t, h, cookie, clientConfigSaveBody1429(t, clientConfigEnvelope1429)); code != 200 {
		t.Fatalf("发布应 200，实际 %d", code)
	}
	// 白名单只放一个测试客户端到不了的 IP（httptest 的对端是 192.0.2.1）。
	s.cfg.IPWhitelist = []string{"203.0.113.99"}
	if code, body, _ := clientConfigGET1429(t, h); code != 200 || body != clientConfigEnvelope1429 {
		t.Fatalf("白名单外公开拉取仍应 200，实际 %d %q", code, body)
	}
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("白名单外调 /api/v1/me 应 403，实际 %d", rec.Code)
	}
}
