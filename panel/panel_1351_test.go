// v1.3.51：订阅网关按面板当前订阅路径生成代码（1.3.50 只写了 /sub/，
// 面板订阅路径是自定义的 /x9f2/ 时订阅经网关全 404、直连却是 200，
// 2026-11-23 真机实测确认）：
// ① 生成的 Worker 与 Caddyfile 主路由就是 s.subPath() 的返回值（配置为空时
//    它已返回默认 sub），再加 /sub/ 兜底；面板生成的代码里不留 SUB_PATH 这种
//    要用户自己填的变量，值必须已经填好；SUB_PATH 变量只留在套件的通用版里；
// ② 生成接口按文件返回（每档真正要部署的文件列表，前端每个文件一个下载按钮）；
// ③ 每档的说明里写明目录（Caddy 新建目录、两文件同目录、命令在该目录里执行；
//    脚本必须用 root 跑、装到固定系统路径）；
// ④ 检测订阅 404 时加直连对照探测（网关 404＋直连 200 = 网关没配这条订阅路径）。
// 用法：go test -run 'TestGateway.*1351' -count=1 .

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func setSubPath1351(s *Server, subPath string) {
	s.cfg.SubPath = subPath
}

func setGatewayToken1351(t *testing.T, s *Server, seed byte) string {
	t.Helper()
	tok := "sub_" + strings.Repeat(string([]byte{seed}), 64)
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	s.cfg.SubTokenEnc = enc
	return tok
}

// ---------- 1. 订阅路径进生成代码 ----------

func TestGatewaySubPathWorker1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")

	// SubPath=x9f2：主路由就是 /x9f2/，/sub/ 兜底也在，代码里不留 SUB_PATH 变量。
	setSubPath1351(s, "x9f2")
	_, content, _, _, err := s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker/x9f2): %v", err)
	}
	if !strings.Contains(content, `const subPath = "x9f2";`) {
		t.Fatal("SubPath=x9f2 时 Worker 主路由应是填好值的 const subPath = \"x9f2\"")
	}
	if strings.Contains(content, "env.SUB_PATH ||") {
		t.Fatal("面板生成的 Worker 代码里不应留 SUB_PATH 变量（值必须已经填好）")
	}
	if !strings.Contains(content, `startsWith("/" + subPath + "/")`) || !strings.Contains(content, `startsWith("/sub/")`) {
		t.Fatal("Worker 路由应同时接主路径与 /sub/ 兜底")
	}
	if kit, ok := kitGatewayFile(t, "worker/worker.js"); ok {
		want := strings.Replace(kit, `env.ORIGIN || ""`, `env.ORIGIN || "https://ccpanel.example.com:11111"`, 1)
		want = strings.Replace(want, `const subPath = (env.SUB_PATH || "sub").replace(/^\/+|\/+$/g, "") || "sub";`, `const subPath = "x9f2";`, 1)
		if content != want {
			t.Fatal("生成的 worker.js 与套件模板不一致（除 ORIGIN 默认值与订阅路径行外应逐字相同）")
		}
	}

	// SubPath 为空（默认）：主路由是 /sub/，/sub/ 兜底也在，不出现 x9f2。
	setSubPath1351(s, "")
	_, content, _, _, err = s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker/默认): %v", err)
	}
	if !strings.Contains(content, `const subPath = "sub";`) {
		t.Fatal("SubPath 为空时 Worker 主路由应是默认 sub")
	}
	if strings.Contains(content, `startsWith("/x9f2/")`) {
		t.Fatal("SubPath 为空时 Worker 路由里不应出现 /x9f2/")
	}
	if strings.Contains(content, "env.SUB_PATH ||") {
		t.Fatal("面板生成的 Worker 代码里不应留 SUB_PATH 变量")
	}
}

func TestGatewaySubPathCaddy1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")

	// SubPath=x9f2：主路由 /x9f2/ 与 /sub/ 兜底都在。
	setSubPath1351(s, "x9f2")
	_, content, _, _, err := s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy/x9f2): %v", err)
	}
	if !strings.Contains(content, "handle /x9f2/*") {
		t.Fatal("SubPath=x9f2 时 Caddyfile 主路由应是 handle /x9f2/*")
	}
	if !strings.Contains(content, "handle /sub/*") {
		t.Fatal("Caddyfile 里应保留 handle /sub/* 兜底")
	}
	if strings.Contains(content, "{$SUB_PATH}") {
		t.Fatal("面板生成的 Caddyfile 里不应留 {$SUB_PATH} 占位符（值必须已经填好）")
	}
	if kit, ok := kitGatewayFile(t, "caddy/Caddyfile"); ok {
		want := kit
		for _, kv := range [][2]string{
			{"{$GATEWAY_DOMAIN}", "gw.example.com"},
			{"{$PANEL_ORIGIN}", "https://ccpanel.example.com:11111"},
			{"{$CONFIG_DIR}", "/data/config"},
			{"{$SUB_PATH}", "x9f2"},
		} {
			want = strings.ReplaceAll(want, kv[0], kv[1])
		}
		if !strings.Contains(content, want) {
			t.Fatal("Caddy 档里的 Caddyfile 与套件不一致（除四个填值处外应逐字相同）")
		}
	}

	// SubPath 为空（默认）：主路由是 /sub/，两条 handle 都是 /sub/*。
	setSubPath1351(s, "")
	_, content, _, _, err = s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy/默认): %v", err)
	}
	if strings.Count(content, "handle /sub/*") != 2 {
		t.Fatalf("SubPath 为空时 Caddyfile 应有两条 handle /sub/*（主路由＋兜底），实际 %d 条", strings.Count(content, "handle /sub/*"))
	}
	if strings.Contains(content, "handle /x9f2/*") {
		t.Fatal("SubPath 为空时 Caddyfile 路由里不应出现 handle /x9f2/*")
	}
}

func TestGatewaySubPathInstall1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	setSubPath1351(s, "x9f2")
	_, content, _, _, err := s.gatewayCode("install")
	if err != nil {
		t.Fatalf("gatewayCode(install/x9f2): %v", err)
	}
	// 默认值段里订阅路径已按面板当前值填好，装的时候不再逐项问。
	if !strings.Contains(content, `SUB_PATH="${SUB_PATH:-x9f2}"`) {
		t.Fatal("脚本档默认值段里订阅路径应是 x9f2")
	}
	// 脚本写 Caddyfile 时用 ${SUB_PATH} 展开，装出来就是 /x9f2/*。
	if !strings.Contains(content, "handle /${SUB_PATH}/*") || !strings.Contains(content, "handle /sub/*") {
		t.Fatal("脚本档里的 Caddyfile 应同时有 ${SUB_PATH} 主路由与 /sub/ 兜底")
	}
}

// ---------- 2. 按文件返回 ----------

func TestGatewayFiles1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	setSubPath1351(s, "x9f2")
	tok := setGatewayToken1351(t, s, 'e')

	// Worker 档：worker.js＋wrangler.toml 两个文件。
	_, content, _, files, err := s.gatewayCode("worker")
	if err != nil {
		t.Fatalf("gatewayCode(worker): %v", err)
	}
	if len(files) != 2 || files[0].Name != "worker.js" || files[1].Name != "wrangler.toml" {
		t.Fatalf("Worker 档应返回 [worker.js, wrangler.toml] 两个文件，实际 %v", fileNames1351(files))
	}
	if files[0].Content != content {
		t.Fatal("worker.js 文件内容应与展示的合并内容一致")
	}
	if !strings.Contains(files[1].Content, `ORIGIN = "https://ccpanel.example.com:11111"`) {
		t.Fatal("wrangler.toml 里的 ORIGIN 没按面板回源地址填好")
	}
	if !strings.Contains(files[1].Content, `SUB_PATH = "x9f2"`) {
		t.Fatal("wrangler.toml 里的 SUB_PATH 没按面板订阅路径填好")
	}
	if !strings.Contains(files[1].Content, `CONFIG_UPSTREAM = ""`) {
		t.Fatal("wrangler.toml 里的 CONFIG_UPSTREAM 未发布时应留空")
	}
	if kit, ok := kitGatewayFile(t, "worker/wrangler.toml"); ok {
		// 本测试的回源地址与套件示例值相同，ORIGIN 不用换；只换 SUB_PATH 与 CONFIG_UPSTREAM。
		want := kit
		want = strings.Replace(want, `SUB_PATH = "sub"`, `SUB_PATH = "x9f2"`, 1)
		want = strings.Replace(want, `CONFIG_UPSTREAM = "https://raw.githubusercontent.com/example/example/main/client-config.json"`, `CONFIG_UPSTREAM = ""`, 1)
		if files[1].Content != want {
			t.Fatal("wrangler.toml 与套件模板不一致（除三个填值处外应逐字相同）")
		}
	}

	// Caddy 档：docker-compose.yml 与 Caddyfile 两个文件。
	_, content, _, files, err = s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy): %v", err)
	}
	if len(files) != 2 || files[0].Name != "docker-compose.yml" || files[1].Name != "Caddyfile" {
		t.Fatalf("Caddy 档应返回 [docker-compose.yml, Caddyfile] 两个文件，实际 %v", fileNames1351(files))
	}
	if !strings.Contains(content, files[0].Content) || !strings.Contains(content, files[1].Content) {
		t.Fatal("Caddy 档两个文件的内容应与展示的合并内容里的两节一致")
	}
	if !strings.Contains(files[1].Content, "handle /x9f2/*") || !strings.Contains(files[1].Content, "handle /sub/*") {
		t.Fatal("Caddyfile 文件里应同时有 /x9f2/ 主路由与 /sub/ 兜底")
	}

	// 脚本档：install.sh 一个文件（update-config.sh 是发布配置后才用的，部署时不给）。
	_, content, _, files, err = s.gatewayCode("install")
	if err != nil {
		t.Fatalf("gatewayCode(install): %v", err)
	}
	if len(files) != 1 || files[0].Name != "install.sh" {
		t.Fatalf("脚本档应只返回 [install.sh] 一个文件，实际 %v", fileNames1351(files))
	}
	if !strings.Contains(content, files[0].Content) {
		t.Fatal("install.sh 文件内容应与展示的合并内容里的一节一致")
	}
	if !strings.HasPrefix(files[0].Content, "#!/usr/bin/env bash\n") {
		t.Fatal("install.sh 文件开头丢了 shebang，不完整")
	}

	// 三档的文件列表里都不含令牌明文。
	for _, typ := range []string{"worker", "caddy", "install"} {
		_, _, _, fs, err := s.gatewayCode(typ)
		if err != nil {
			t.Fatalf("gatewayCode(%s): %v", typ, err)
		}
		for _, f := range fs {
			if strings.Contains(f.Content, tok) {
				t.Fatalf("%s 的 %s 文件里出现了订阅令牌明文", typ, f.Name)
			}
		}
	}
}

func fileNames1351(files []gatewayFile) []string {
	names := []string{}
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names
}

// ---------- 3. 目录说明 ----------

func TestGatewayDirNotes1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")

	// Caddy 档：新建目录、两文件同目录、命令在该目录里执行；data/config 是相对路径。
	_, content, _, _, err := s.gatewayCode("caddy")
	if err != nil {
		t.Fatalf("gatewayCode(caddy): %v", err)
	}
	for _, want := range []string{
		"新建一个目录",
		"~/sub-gateway",
		"两个文件",
		"放进去",
		"mkdir -p ~/sub-gateway && cd ~/sub-gateway",
		"data/config",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("Caddy 档目录说明里缺了 %q", want)
		}
	}

	// 脚本档：install.sh 放哪都行，但必须用 root 跑，装到固定系统路径。
	_, content, _, _, err = s.gatewayCode("install")
	if err != nil {
		t.Fatalf("gatewayCode(install): %v", err)
	}
	for _, want := range []string{
		"install.sh 放哪都行",
		"必须用 root 跑",
		"sudo bash install.sh",
		"/usr/local/bin/caddy",
		"/etc/caddy/Caddyfile",
		"/etc/sub-gateway/config",
		"/etc/systemd/system/sub-gateway.service",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("脚本档目录说明里缺了 %q", want)
		}
	}
}

// ---------- 4. 检测订阅 404 的直连对照 ----------

// stubHTTP1351 按给定函数回包的桩服务，记下请求次数与最后路径。
type stubHTTP1351 struct {
	mu       sync.Mutex
	handler  func(w http.ResponseWriter, r *http.Request)
	calls    int
	lastPath string
}

func (g *stubHTTP1351) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.calls++
	g.lastPath = r.URL.Path
	g.mu.Unlock()
	g.handler(w, r)
}

func (g *stubHTTP1351) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func newGatewayCheckServer1351(t *testing.T) (*Server, string) {
	t.Helper()
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	setSubPath1351(s, "x9f2")
	return s, setGatewayToken1351(t, s, 'f')
}

func TestGatewayCheckSub404DirectOK1351(t *testing.T) {
	s, _ := newGatewayCheckServer1351(t)
	// 网关只接 /sub/（旧 Worker）：/x9f2/ 404；直连面板同一条路 200、2 个节点。
	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/public-info" {
			w.WriteHeader(200)
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		if r.URL.Path == "/client-config.json" {
			w.WriteHeader(200)
			fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, "Not Found")
	}}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()
	origin := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, singboxBody1350(2))
	}}
	originSrv := httptest.NewServer(origin)
	defer originSrv.Close()

	items := s.runGatewayChecks(gwSrv.URL, originSrv.URL, checkClient1350())
	if len(items) != 3 {
		t.Fatalf("应返回 3 条检测，实际 %d 条", len(items))
	}
	sub := items[2]
	if sub.OK || !strings.Contains(sub.Reason, "网关没配这条订阅路径") || !strings.Contains(sub.Reason, "直连是通的") {
		t.Fatalf("网关 404＋直连 200 应报“网关没配这条订阅路径（面板直连是通的）”，实际 ok=%v reason=%q", sub.OK, sub.Reason)
	}
	if origin.count() != 1 {
		t.Fatalf("对照探测应只向回源发一次请求，实际 %d 次", origin.count())
	}
	if gw.count() != 3 {
		t.Fatalf("网关三条路各应只拉一次，实际共 %d 次", gw.count())
	}
	// 对照探测的令牌只留在服务端：检测返回里绝不能有令牌明文。
	raw := fmt.Sprint(items)
	if strings.Contains(raw, "sub_") {
		t.Fatal("检测返回里出现了订阅令牌明文")
	}
}

func TestGatewayCheckSub404Both4041351(t *testing.T) {
	s, _ := newGatewayCheckServer1351(t)
	// 网关 404，直连也 404：令牌不对（或订阅本身没这条路），报原来的原因。
	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "Not Found")
	}}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()
	origin := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "Not Found")
	}}
	originSrv := httptest.NewServer(origin)
	defer originSrv.Close()

	items := s.runGatewayChecks(gwSrv.URL, originSrv.URL, checkClient1350())
	if items[2].OK {
		t.Fatalf("两边都 404 时订阅项应不通，实际 ok=%v reason=%q", items[2].OK, items[2].Reason)
	}
	// 通用订阅那行（下标 2）报原来的原因；合并/每人没有令牌，报无此类型订阅。
	if items[2].Subs[2].Reason != "订阅令牌不对（或网关没配订阅这条路）" {
		t.Fatalf("两边都 404 时通用订阅行应报原来的原因，实际 reason=%q", items[2].Subs[2].Reason)
	}
	if !items[2].Subs[0].Skipped || !items[2].Subs[1].Skipped {
		t.Fatal("合并/每人没有令牌时那两行应报无此类型订阅（Skipped）")
	}
}

func TestGatewayCheckSub404NoOrigin1351(t *testing.T) {
	s, _ := newGatewayCheckServer1351(t)
	// 回源地址不知道（没配证书域名）：探不了对照，回落到原来的原因，不报错。
	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "Not Found")
	}}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()

	items := s.runGatewayChecks(gwSrv.URL, "", checkClient1350())
	if items[2].OK {
		t.Fatalf("没回源地址时订阅项应不通，实际 ok=%v reason=%q", items[2].OK, items[2].Reason)
	}
	if items[2].Subs[2].Reason != "订阅令牌不对（或网关没配订阅这条路）" {
		t.Fatalf("没回源地址时通用订阅行应回落到原来的原因，实际 reason=%q", items[2].Subs[2].Reason)
	}
}

func TestGatewayCheckSub200NoProbe1351(t *testing.T) {
	s, _ := newGatewayCheckServer1351(t)
	// 网关订阅通：不做对照探测，回源一次也不应被请求。
	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/public-info" || r.URL.Path == "/client-config.json" {
			w.WriteHeader(200)
			fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, singboxBody1350(1))
	}}
	gwSrv := httptest.NewServer(gw)
	defer gwSrv.Close()
	origin := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, singboxBody1350(1))
	}}
	originSrv := httptest.NewServer(origin)
	defer originSrv.Close()

	items := s.runGatewayChecks(gwSrv.URL, originSrv.URL, checkClient1350())
	if !items[2].OK || items[2].Nodes != 1 {
		t.Fatalf("网关订阅通时应直接报通且数出 1 个节点，实际 ok=%v nodes=%d reason=%q", items[2].OK, items[2].Nodes, items[2].Reason)
	}
	if origin.count() != 0 {
		t.Fatalf("网关订阅通时不应做对照探测，实际向回源发了 %d 次请求", origin.count())
	}
}

// ---------- 5. 检测三类订阅与分类计数 ----------

// TestGatewayCheckTokens1351 三类令牌各取一个：通用订阅、每人订阅取第一个用户、
// 独立合并订阅取第一个；解不开的跳过、那类就不测。
func TestGatewayCheckTokens1351(t *testing.T) {
	s := newTestServer1350(t)
	encMerged := mustEnc1351(t, s, "sub_"+strings.Repeat("m", 64))
	encUser := mustEnc1351(t, s, "sub_"+strings.Repeat("u", 64))
	encGeneric := mustEnc1351(t, s, "sub_"+strings.Repeat("g", 64))

	// 三种都有：三类各一个。
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", TokenEnc: encMerged}}
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "甲", TokenEnc: encUser}}
	s.cfg.SubTokenEnc = encGeneric
	toks := s.gatewayCheckTokens()
	if len(toks) != 3 || toks["indmerged"] != "sub_"+strings.Repeat("m", 64) || toks["subuser"] != "sub_"+strings.Repeat("u", 64) || toks["generic"] != "sub_"+strings.Repeat("g", 64) {
		t.Fatalf("三种都有时应三类各取一个，实际 %v", toks)
	}
	// 只有通用订阅。
	s.cfg.IndependentMergedSubs = nil
	s.cfg.SubUsers = nil
	toks = s.gatewayCheckTokens()
	if len(toks) != 1 || toks["generic"] == "" {
		t.Fatalf("只有通用订阅时应只取通用，实际 %v", toks)
	}
	// 独立合并订阅的解不开：那类跳过，其余两类照常。
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", TokenEnc: "bogus-ciphertext"}}
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "甲", TokenEnc: encUser}}
	toks = s.gatewayCheckTokens()
	if len(toks) != 2 || toks["indmerged"] != "" || toks["subuser"] == "" || toks["generic"] == "" {
		t.Fatalf("独立合并订阅解不开时应跳过那类、取其余两类，实际 %v", toks)
	}
	// 什么都没有。
	s.cfg.IndependentMergedSubs = nil
	s.cfg.SubUsers = nil
	s.cfg.SubTokenEnc = ""
	if len(s.gatewayCheckTokens()) != 0 {
		t.Fatal("没有订阅时应一个令牌都取不到")
	}
}

func mustEnc1351(t *testing.T, s *Server, tok string) string {
	t.Helper()
	enc, err := s.encryptSubTokenNow(tok)
	if err != nil {
		t.Fatalf("encryptSubTokenNow: %v", err)
	}
	return enc
}

// singboxBodyTagged1351 带 tag 的 sing-box 配置：nodes 是节点 outbound，
// 后面再补 selector/direct/block 三个非节点。
func singboxBodyTagged1351(t *testing.T, nodes []gatewaySubOutbound) string {
	t.Helper()
	obs := append([]gatewaySubOutbound{}, nodes...)
	obs = append(obs,
		gatewaySubOutbound{Type: "selector", Tag: "Proxy"},
		gatewaySubOutbound{Type: "direct", Tag: "direct"},
		gatewaySubOutbound{Type: "block", Tag: "block"},
	)
	b, err := json.Marshal(map[string]any{"outbounds": obs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestGatewayCheckSubKinds1351 三类都测：每行写清类型与数量，中转/直连/其它
// 按 tag 分；缺某类时那行报无此类型订阅，不算不通。
func TestGatewayCheckSubKinds1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", TokenEnc: mustEnc1351(t, s, "sub_"+strings.Repeat("m", 64))}}
	s.cfg.SubUsers = []SubUser{{ID: "u1", Name: "甲", TokenEnc: mustEnc1351(t, s, "sub_"+strings.Repeat("u", 64))}}
	s.cfg.SubTokenEnc = mustEnc1351(t, s, "sub_"+strings.Repeat("g", 64))

	// 桩网关：三类订阅各回不同的节点——独立合并 2 个（中转 1、直连 1），
	// 每人 1 个（其它），通用 0 个。
	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/public-info" || r.URL.Path == "/client-config.json" {
			w.WriteHeader(200)
			fmt.Fprint(w, `{}`)
			return
		}
		// 按令牌尾巴区分是哪类订阅：m=独立合并、u=每人、g=通用。
		body := singboxBody1350(0)
		if strings.Contains(r.URL.Path, strings.Repeat("m", 64)) {
			body = singboxBodyTagged1351(t, []gatewaySubOutbound{
				{Type: "vless", Tag: "智利-中转"},
				{Type: "vless", Tag: "智利-直连"},
			})
		} else if strings.Contains(r.URL.Path, strings.Repeat("u", 64)) {
			body = singboxBodyTagged1351(t, []gatewaySubOutbound{
				{Type: "vless", Tag: "plain-node"},
			})
		}
		w.WriteHeader(200)
		fmt.Fprint(w, body)
	}}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	sub := items[2]
	if !sub.OK || sub.Nodes != 3 {
		t.Fatalf("三类都通时订阅项应通、总数 3 个，实际 ok=%v nodes=%d reason=%q", sub.OK, sub.Nodes, sub.Reason)
	}
	if len(sub.Subs) != 3 {
		t.Fatalf("订阅项下应分三行，实际 %d 行", len(sub.Subs))
	}
	// 独立合并订阅行：2 个节点（中转 1，直连 1）。
	m := sub.Subs[0]
	if m.Kind != "indmerged" || m.Label != "独立合并订阅" || !m.OK || m.Nodes != 2 || m.Relay != 1 || m.Direct != 1 || m.Other != 0 {
		t.Fatalf("独立合并订阅行不对：%+v", m)
	}
	if m.Reason != "通，2 个节点（中转 1，直连 1）" {
		t.Fatalf("独立合并订阅行理由不对：%q", m.Reason)
	}
	// 每人订阅行：1 个节点（其它 1）。
	u := sub.Subs[1]
	if u.Kind != "subuser" || u.Label != "每人订阅" || !u.OK || u.Nodes != 1 || u.Relay != 0 || u.Direct != 0 || u.Other != 1 {
		t.Fatalf("每人订阅行不对：%+v", u)
	}
	if u.Reason != "通，1 个节点（其它 1）" {
		t.Fatalf("每人订阅行理由不对：%q", u.Reason)
	}
	// 通用订阅行：0 个节点（本机无落地是空的）。
	g := sub.Subs[2]
	if g.Kind != "generic" || g.Label != "通用订阅" || !g.OK || g.Nodes != 0 {
		t.Fatalf("通用订阅行不对：%+v", g)
	}
	if g.Reason != "通，0 个节点（本机无落地是空的）" {
		t.Fatalf("通用订阅行理由不对：%q", g.Reason)
	}
	// 检测返回里绝不能有令牌明文。
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "sub_") {
		t.Fatal("检测返回里出现了订阅令牌明文")
	}
}

// TestGatewayCheckSubMissingKind1351 缺某类时那行报无此类型订阅，不算不通。
func TestGatewayCheckSubMissingKind1351(t *testing.T) {
	s := newTestServer1350(t)
	setGatewayCfg1350(s, true, "ccpanel.example.com", "gw.example.com")
	s.cfg.IndependentMergedSubs = []IndependentMergedSub{{ID: "ims-1", TokenEnc: mustEnc1351(t, s, "sub_"+strings.Repeat("m", 64))}}

	gw := &stubHTTP1351{handler: func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/public-info" || r.URL.Path == "/client-config.json" {
			w.WriteHeader(200)
			fmt.Fprint(w, `{}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, singboxBody1350(2))
	}}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	items := s.runGatewayChecks(srv.URL, "", checkClient1350())
	sub := items[2]
	if !sub.OK || sub.Nodes != 2 {
		t.Fatalf("只有独立合并订阅通时订阅项应通、总数 2 个，实际 ok=%v nodes=%d reason=%q", sub.OK, sub.Nodes, sub.Reason)
	}
	if !sub.Subs[1].Skipped || sub.Subs[1].Reason != "无此类型订阅" {
		t.Fatalf("每人订阅缺令牌时那行应报无此类型订阅，实际 %+v", sub.Subs[1])
	}
	if !sub.Subs[2].Skipped || sub.Subs[2].Reason != "无此类型订阅" {
		t.Fatalf("通用订阅缺令牌时那行应报无此类型订阅，实际 %+v", sub.Subs[2])
	}
	if !strings.Contains(sub.Reason, "每人订阅：无此类型订阅") || !strings.Contains(sub.Reason, "通用订阅：无此类型订阅") {
		t.Fatalf("整项理由汇总里应有缺类的文案，实际 %q", sub.Reason)
	}
}
