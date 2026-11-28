// v1.3.50：面板内置订阅网关生成器——
// ① 生成代码三档（Worker/Caddy 一键容器/脚本直装），值按面板当前值填；
//    Caddy 档一份内容里含完整的 docker-compose.yml 与 Caddyfile 再加
//    exact 执行命令，脚本档一份内容里含完整的 install.sh 再加 exact
//    执行命令；② 生成内容与套件模板逐字对照（只差填值处），脚本档
//    跑 bash -n 语法检查；③ 检测接口三条路（通/旧版 404/未配上游/
//    无订阅/未配域名）；④ 生成代码与检测返回里都不含令牌。
// 用法：go test -run 'TestGateway.*1350' -count=1 .

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestServer1350(t *testing.T) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-1350")
	s, err := newServer(Env{PanelHome: t.TempDir(), RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// kitGatewayFile 读套件里的模板文件（只在本机有套件时存在；
// 没有时返回 false，调用方跳过逐字对照部分，只测填值行为）。
func kitGatewayFile(t *testing.T, rel string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "subscription-gateway-kit", rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func setGatewayCfg1350(s *Server, httpsOn bool, certDomain, publicDomain string) {
	s.cfg.HTTPSEnabled = httpsOn
	s.cfg.HTTPSDomain = certDomain
	s.cfg.SubPublicDomain = publicDomain
}

// ---------- 1. 生成代码 ----------

func TestGatewayCodeWorker1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	filename, content, note, _, err := s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker): %v", err)
	}
	if filename != "worker.js" {
		t.Fatalf("文件名应是 worker.js，实际 %q", filename)
	}
	if note != "" {
		t.Fatalf("值都填好了，note 应为空，实际 %q", note)
	}
	if !strings.Contains(content, `(env.ORIGIN || "https://ccpanel.example.com:11111")`) {
		t.Fatal("Worker 的 ORIGIN 默认值没填成面板回源地址")
	}
	if kit, ok := kitGatewayFile(t, "worker/worker.js"); ok {
		want := strings.Replace(kit, `env.ORIGIN || ""`, `env.ORIGIN || "https://ccpanel.example.com:11111"`, 1)
		// 订阅路径行整行换成填好值的常量（本测试里 SubPath 未设，主路径是默认 sub）。
		want = strings.Replace(want, `const subPath = (env.SUB_PATH || "sub").replace(/^\/+|\/+$/g, "") || "sub";`, `const subPath = "sub";`, 1)
		if content != want {
			t.Fatal("生成的 worker.js 与套件模板不一致（除 ORIGIN 默认值与订阅路径行外应逐字相同）")
		}
	}

	// HTTP 面板：协议跟着走 http。
	setGatewayCfg1350(s, false, "ccpanel.example.com", "gw.example.com")
	_, content, _, _, err = s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker/http): %v", err)
	}
	if !strings.Contains(content, `(env.ORIGIN || "http://ccpanel.example.com:11111")`) {
		t.Fatal("面板 HTTP 时 ORIGIN 应是 http://")
	}

	// 没配证书域名：占位符 + 提示替换。
	setGatewayCfg1350(s, true, "", "gw.example.com")
	_, content, note, _, err = s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker/无域名): %v", err)
	}
	if !strings.Contains(content, `(env.ORIGIN || "https://<面板域名>:11111")`) {
		t.Fatal("没配证书域名时 ORIGIN 应是占位符 https://<面板域名>:11111")
	}
	if !strings.Contains(note, "换成面板的域名") {
		t.Fatalf("占位符时 note 应提示替换，实际 %q", note)
	}

	// 未知类型拒绝。
	if _, _, _, _, err := s.gatewayCode("bogus"); err == nil {
		t.Fatal("未知 type 应报错")
	}
}

func TestGatewayCodeCaddy1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	filename, content, note, _, err := s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy): %v", err)
	}
	if filename != "docker-compose.yml" {
		t.Fatalf("文件名应是 docker-compose.yml，实际 %q", filename)
	}
	if note != "" {
		t.Fatalf("值都填好了，note 应为空，实际 %q", note)
	}
	// 合并内容四节：docker-compose.yml、Caddyfile、目录说明、执行命令，按顺序出现。
	secCompose := strings.Index(content, "# ================= docker-compose.yml =================")
	secCaddy := strings.Index(content, "# ================= Caddyfile =================")
	secDir := strings.Index(content, "# ================= 目录说明 =================")
	secCmd := strings.Index(content, "# ================= 执行命令 =================")
	if secCompose < 0 || secCaddy < 0 || secDir < 0 || secCmd < 0 || !(secCompose < secCaddy && secCaddy < secDir && secDir < secCmd) {
		t.Fatal("Caddy 档内容应按顺序分四节：docker-compose.yml、Caddyfile、目录说明、执行命令")
	}
	// 执行命令 exact：建配置目录、放配置文件、up、两条自测。
	for _, cmd := range []string{
		"mkdir -p data/config",
		"docker compose up -d",
		"curl -sI https://gw.example.com/healthz",
		"curl -s https://gw.example.com/api/v1/public-info",
	} {
		if !strings.Contains(content, cmd) {
			t.Fatalf("Caddy 档执行命令里缺了 %q", cmd)
		}
	}
	if strings.Contains(content, "env_file") {
		t.Fatal("compose 里不应再有 env_file（值已填进 Caddyfile，不用建 .env）")
	}
	if strings.Contains(content, "{$") {
		t.Fatal("Caddy 档里还有没填的 {$...} 占位符")
	}
	// 与套件逐字对照：两节内容除填值外与套件文件逐字相同。
	kitCompose, okC := kitGatewayFile(t, "caddy/docker-compose.yml")
	kitCaddy, okF := kitGatewayFile(t, "caddy/Caddyfile")
	if okC && okF {
		wantCompose := "# 由面板生成：Caddyfile 里的值已填好，不用再建 .env。\n" +
			strings.Replace(kitCompose, "    env_file:\n      - .env\n", "", 1)
		if !strings.Contains(content, wantCompose) {
			t.Fatal("Caddy 档里的 docker-compose.yml 与套件不一致（除去掉 env_file 与加一行说明外应逐字相同）")
		}
		wantCaddy := kitCaddy
		for _, kv := range [][2]string{
			{"{$GATEWAY_DOMAIN}", "gw.example.com"},
			{"{$PANEL_ORIGIN}", "https://ccpanel.example.com:11111"},
			{"{$CONFIG_DIR}", "/data/config"},
			{"{$SUB_PATH}", "sub"},
		} {
			wantCaddy = strings.ReplaceAll(wantCaddy, kv[0], kv[1])
		}
		if !strings.Contains(content, wantCaddy) {
			t.Fatal("Caddy 档里的 Caddyfile 与套件不一致（除三个填值处外应逐字相同）")
		}
	}

	// 没填订阅对外域名：网关域名占位符 + 提示。
	setGatewayCfg1350(s, true, "ccpanel.example.com", "")
	_, content, note, _, err = s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy/无网关域名): %v", err)
	}
	if !strings.Contains(content, "example.com {") {
		t.Fatal("没填订阅对外域名时网关域名应是占位符 example.com")
	}
	if !strings.Contains(note, "占位符") {
		t.Fatalf("占位符时 note 应提示，实际 %q", note)
	}

	// compose 不再是独立一档（已并进 caddy），未知类型拒绝。
	if _, _, _, _, err := s.gatewayCode("compose"); err == nil {
		t.Fatal("compose 已并进 caddy，单独传 compose 应报错")
	}
}

func TestGatewayCodeInstall1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	filename, content, note, _, err := s.gatewayCode("install")
	if err != nil {
		t.Fatalf("gatewayCode(install): %v", err)
	}
	if filename != "install.sh" {
		t.Fatalf("文件名应是 install.sh，实际 %q", filename)
	}
	if note != "" {
		t.Fatalf("值都填好了，note 应为空，实际 %q", note)
	}
	// 合并内容三节：install.sh、目录说明、执行命令，按顺序出现。
	secScript := strings.Index(content, "# ================= install.sh =================")
	secDir := strings.Index(content, "# ================= 目录说明 =================")
	secCmd := strings.Index(content, "# ================= 执行命令 =================")
	if secScript < 0 || secDir < 0 || secCmd < 0 || !(secScript < secDir && secDir < secCmd) {
		t.Fatal("脚本档内容应按顺序分三节：install.sh、目录说明、执行命令")
	}
	// 执行命令 exact：交互、环境变量非交互、DRY_RUN 干跑、curl 一行到底。
	// 正式安装必须用 root 跑（sudo），干跑不用。
	for _, cmd := range []string{
		"sudo bash install.sh",
		"sudo GATEWAY_DOMAIN=gw.example.com PANEL_ORIGIN=https://ccpanel.example.com:11111 SUB_PATH=sub bash install.sh",
		"DRY_RUN=1 ROOT_DIR=/tmp/gw-dryrun bash install.sh",
		"curl -fsSL https://example.com/subscription-gateway-kit/scripts/install.sh | sudo bash",
	} {
		if !strings.Contains(content, cmd) {
			t.Fatalf("脚本档执行命令里缺了 %q", cmd)
		}
	}
	// 脚本档第一节：与套件逐字对照（除插入的默认值段外应逐字相同），且
	// 必须是完整可执行的：bash -n 语法检查过。提取时跳过分节标题与
	// “保存成”说明行，到目录说明节为止，剩下的就是用户要保存的脚本本身。
	scriptHeader := "# ================= install.sh =================\n# 保存成 install.sh（值已按面板当前值填好，交互提示保留）\n"
	script := content[secScript+len(scriptHeader) : secDir]
	kit, ok := kitGatewayFile(t, "scripts/install.sh")
	if ok {
		block := "\n# ---- 由面板生成：下面三行是按面板当前值填好的默认值（环境变量优先） ----\n" +
			"GATEWAY_DOMAIN=\"${GATEWAY_DOMAIN:-gw.example.com}\"\n" +
			"PANEL_ORIGIN=\"${PANEL_ORIGIN:-https://ccpanel.example.com:11111}\"\n" +
			"SUB_PATH=\"${SUB_PATH:-sub}\"\n"
		want := strings.Replace(kit, "set -euo pipefail\n", "set -euo pipefail\n"+block, 1)
		if script != want {
			t.Fatal("脚本档里的 install.sh 与套件不一致（除加一段默认值外应逐字相同）")
		}
		// 完整性：开头是 shebang，结尾是套件的最后一行。
		if !strings.HasPrefix(script, "#!/usr/bin/env bash\n") {
			t.Fatal("脚本档里的 install.sh 开头丢了 shebang，不完整")
		}
		if !strings.HasSuffix(strings.TrimRight(script, "\n"), "say \"面板开关 HTTPS 后，记得同步改回源：改 Caddyfile 里的回源地址，再 systemctl reload sub-gateway\"") {
			t.Fatal("脚本档里的 install.sh 结尾与套件不一致，不完整")
		}
		// 交互提示保留：脚本里仍有逐项询问的提示。
		for _, prompt := range []string{
			"网关域名",
			"回源地址",
			"client-config.json",
		} {
			if !strings.Contains(script, prompt) {
				t.Fatalf("脚本档里的 install.sh 丢了交互提示 %q", prompt)
			}
		}
	}
	// bash -n 语法检查：脚本档第一节必须是可执行的完整脚本。
	tmp := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(tmp, []byte(script), 0o600); err != nil {
		t.Fatalf("写临时脚本失败：%v", err)
	}
	out, err := exec.Command("bash", "-n", tmp).CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n 语法检查失败：%v\n%s", err, out)
	}
}

func TestGatewayCodeNoToken1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	// 往配置里放三条真实密文令牌：生成代码时一个字都不能漏出来。
	tok := "sub_" + strings.Repeat("a", 64)
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.SubTokenEnc = enc
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "甲", TokenEnc: enc}}
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", TokenEnc: enc}}
	for _, typ := range []string{"worker", "caddy", "install"} {
		_, content, _, _, err := s.gatewayCode(typ)
		if err != nil {
			t.Fatalf("gatewayCode(%s): %v", typ, err)
		}
		if strings.Contains(content, tok) {
			t.Fatalf("%s 生成的代码里出现了订阅令牌明文", typ)
		}
		if strings.Contains(content, enc) {
			t.Fatalf("%s 生成的代码里出现了订阅令牌密文", typ)
		}
		if strings.Contains(content, "sub_") {
			t.Fatalf("%s 生成的代码里出现了 sub_ 前缀", typ)
		}
	}
}

// ---------- 2. 检测接口 ----------

// stubGateway1350 桩网关：三条路的状态与响应体都可配，记下订阅那条的请求。
type stubGateway1350 struct {
	mu               sync.Mutex
	publicInfoStatus int
	publicInfoBody   string
	configStatus     int
	configBody       string
	subStatus        int
	subBody          string
	lastSubPath      string
	lastSubUA        string
	subCalls         int
}

func (g *stubGateway1350) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v1/public-info":
		w.WriteHeader(g.publicInfoStatus)
		fmt.Fprint(w, g.publicInfoBody)
	case r.URL.Path == "/client-config.json":
		w.WriteHeader(g.configStatus)
		fmt.Fprint(w, g.configBody)
	case strings.HasPrefix(r.URL.Path, "/sub/"):
		g.subCalls++
		g.lastSubPath = r.URL.Path
		g.lastSubUA = r.Header.Get("User-Agent")
		w.WriteHeader(g.subStatus)
		fmt.Fprint(w, g.subBody)
	default:
		w.WriteHeader(404)
		fmt.Fprint(w, "Not Found")
	}
}

func singboxBody1350(nodes int) string {
	obs := []string{}
	for i := 0; i < nodes; i++ {
		obs = append(obs, fmt.Sprintf(`{"type":"vless","tag":"节点%d"}`, i+1))
	}
	obs = append(obs, `{"type":"selector","tag":"Proxy"}`, `{"type":"direct","tag":"direct"}`, `{"type":"block","tag":"block"}`)
	return `{"outbounds":[` + strings.Join(obs, ",") + `]}`
}

func checkClient1350() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
}

func TestGatewayCheckAllOK1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	tok := "sub_" + strings.Repeat("b", 64)
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.SubTokenEnc = enc

	stub := &stubGateway1350{
		publicInfoStatus: 200, publicInfoBody: `{"ok":true}`,
		configStatus: 200, configBody: `{"version":1}`,
		subStatus: 200, subBody: singboxBody1350(2),
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	if len(items) != 3 {
		t.Fatalf("应返回 3 条检测，实际 %d 条", len(items))
	}
	for _, it := range items {
		if !it.OK {
			t.Fatalf("%s 应通，实际不通：%s", it.Name, it.Reason)
		}
		if it.Ms < 0 {
			t.Fatalf("%s 耗时不应为负", it.Name)
		}
	}
	if items[2].Nodes != 2 {
		t.Fatalf("订阅应数出 2 个节点，实际 %d 个", items[2].Nodes)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.subCalls != 1 {
		t.Fatalf("订阅应只拉一次，实际 %d 次", stub.subCalls)
	}
	if stub.lastSubPath != "/sub/"+tok {
		t.Fatalf("订阅请求路径不对：%q", stub.lastSubPath)
	}
	if stub.lastSubUA != "Zhexuexi/1.0" {
		t.Fatalf("订阅请求 UA 不对：%q", stub.lastSubUA)
	}
	// 检测返回里绝不能有令牌明文。
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), tok) {
		t.Fatal("检测返回里出现了订阅令牌明文")
	}
}

func TestGatewayCheckOldWorker1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	tok := "sub_" + strings.Repeat("c", 64)
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.SubTokenEnc = enc

	// 旧版 Worker：公告与配置都 404 Not Found，订阅那条照常通。
	stub := &stubGateway1350{
		publicInfoStatus: 404, publicInfoBody: "Not Found",
		configStatus: 404, configBody: "Not Found",
		subStatus: 200, subBody: singboxBody1350(1),
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	if items[0].OK || !strings.Contains(items[0].Reason, "旧版") {
		t.Fatalf("公告 404 Not Found 应报旧版 Worker，实际 ok=%v reason=%q", items[0].OK, items[0].Reason)
	}
	if items[1].OK || !strings.Contains(items[1].Reason, "旧版") {
		t.Fatalf("配置 404 Not Found 应报旧版 Worker，实际 ok=%v reason=%q", items[1].OK, items[1].Reason)
	}
	if !items[2].OK || items[2].Nodes != 1 {
		t.Fatalf("订阅那条应照常通且数出 1 个节点，实际 ok=%v nodes=%d", items[2].OK, items[2].Nodes)
	}
}

func TestGatewayCheckNoUpstream1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	tok := "sub_" + strings.Repeat("d", 64)
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.SubTokenEnc = enc
	stub := &stubGateway1350{
		publicInfoStatus: 200, publicInfoBody: `{"ok":true}`,
		configStatus: 404, configBody: "client-config.json upstream not configured",
		subStatus: 200, subBody: singboxBody1350(0),
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	if items[1].OK || !strings.Contains(items[1].Reason, "CONFIG_UPSTREAM") {
		t.Fatalf("配置 404 upstream not configured 应报未配上游，实际 ok=%v reason=%q", items[1].OK, items[1].Reason)
	}
	if !items[0].OK || !items[2].OK {
		t.Fatal("另外两条应照常通")
	}
	if items[2].Nodes != 0 {
		t.Fatalf("0 节点订阅应数出 0 个，实际 %d 个", items[2].Nodes)
	}
}

func TestGatewayCheckNoSub1350(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	// 配置里没有任何订阅令牌。
	stub := &stubGateway1350{
		publicInfoStatus: 200, publicInfoBody: `{"ok":true}`,
		configStatus: 200, configBody: `{}`,
		subStatus: 200, subBody: singboxBody1350(0),
	}
	srv := httptest.NewServer(stub)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	if items[2].OK || !strings.Contains(items[2].Reason, "无订阅可测") {
		t.Fatalf("没有订阅时订阅项应报“无订阅可测”，实际 ok=%v reason=%q", items[2].OK, items[2].Reason)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.subCalls != 0 {
		t.Fatal("无订阅可测时不应发订阅请求")
	}
}

func TestGatewayCheckHandler1350(t *testing.T) {
	s := newTestServer1350(t)

	// 没配订阅对外域名：直接报原因，不发请求。
	setGatewayCfg1350(s, true, "ccpanel.example.com", "")
	rec := httptest.NewRecorder()
	s.handleGatewayCheck(rec, httptest.NewRequest("POST", "/api/v1/settings/gateway/check", nil))
	if rec.Code != 200 {
		t.Fatalf("没配域名时也应回 200（ok:false），实际 %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if resp["all_ok"] != false || !strings.Contains(fmt.Sprint(resp["reason"]), "未配置订阅对外域名") {
		t.Fatalf("没配域名时应报“未配置订阅对外域名”，实际 %v", resp)
	}
	// 顶层 ok 必须是 true（okResp 的默认值），否则前端 api() 会直接抛错、
	// 逐条结果显示不出来。
	if resp["ok"] != true {
		t.Fatalf("顶层 ok 应为 true，实际 %v", resp["ok"])
	}

	// 配了域名但连不上：三条都报请求失败，不抛错、不 500。
	setGatewayCfg1350(s, true, "ccpanel.example.com", "127.0.0.1:1")
	rec = httptest.NewRecorder()
	s.handleGatewayCheck(rec, httptest.NewRequest("POST", "/api/v1/settings/gateway/check", nil))
	if rec.Code != 200 {
		t.Fatalf("网关连不上时也应回 200（逐条报失败），实际 %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	checks, _ := resp["checks"].([]any)
	if len(checks) != 3 {
		t.Fatalf("应返回 3 条检测，实际 %v", resp["checks"])
	}
}
