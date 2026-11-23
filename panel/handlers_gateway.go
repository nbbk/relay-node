package main

// ---------- 订阅网关代码生成与检测（v1.3.50，1.3.51 修订阅路径与加文件下载） ----------
// 背景：订阅网关的部署代码（Worker / Caddy / 脚本）以前只在套件
// ~/workspace/subscription-gateway-kit/ 里，面板里没有，用户问为什么
// 不能直接在管理面板生成。答案：完全可以，只是之前没做，这里补上。
//
// 生成：GET /api/v1/settings/gateway/code?type=worker|caddy|install，
// 返回 {filename, content, note, files}。content 是展示用的合并内容
// （含执行命令），files 是每档真正要部署的文件列表（[{name, content}]，
// 前端每个文件一个“下载 <文件名>”按钮）：worker 是 worker.js＋wrangler.toml，
// caddy 是 docker-compose.yml 与 Caddyfile 两个文件，install 是 install.sh
// 一个文件（update-config.sh 是发布配置后才用的同步脚本，部署时不需要，不给；
// client-config.json 不是部署文件，发布后才放进 data/config/）。
// 模板是 panel/gateway/ 下的文件，与套件里的现成文件逐字一致（worker.js、
// Caddyfile、docker-compose.yml、install.sh、wrangler.toml），生成时只把
// 需要填的值换成面板当前值：
//   - Worker：ORIGIN 默认值填面板当前回源地址（协议按 HTTPS 开关，域名用
//     证书域名，端口用面板端口）；没配证书域名时用占位符
//     https://<面板域名>:11111 并在 note 里提示替换。CONFIG_UPSTREAM 不填
//     （留空位，等 client-config.json 发布后填直链）。
//     订阅路径（1.3.51）：订阅路由的主路径就是 s.subPath() 的返回值（配置
//     SubPath 为空时它已经返回默认 sub，不用再判断一次），直接填进代码里
//     （模板里的 subPath 行整行换成 const subPath = "<当前路径>";）；再加
//     /sub/ 兜底。面板生成的代码里不留 SUB_PATH 这种要用户自己填的变量——
//     值必须已经填好，复制过去就能用。SUB_PATH 变量只留在套件的通用版里
//     （套件是给别人用的，默认 sub、注释写明按面板的自定义路径改）。
//     2026-11-23 真机教训：1.3.50 生成的 Worker 只写了 /sub/，面板订阅路径
//     是自定义的 /x9f2/，订阅经 Worker 全是 404、直连面板却是 200。
//   - Caddy 一键容器：完整的 docker-compose.yml 与 Caddyfile（值已填好），
//     再加 exact 执行命令，三样拼在一份内容里、分节标明文件名。
//     Caddyfile 里 {$GATEWAY_DOMAIN} 填订阅对外域名（没填用 example.com
//     占位），{$PANEL_ORIGIN} 填面板回源地址，{$CONFIG_DIR} 填 /data/config，
//     {$SUB_PATH} 填 s.subPath()（订阅路由主路径），再加一条 handle /sub/* 兜底；
//     compose 与套件一致，但去掉了 env_file 一行（值已填进 Caddyfile，
//     不用再建 .env，否则 docker compose 会因找不到 .env 报错）。
//   - 脚本直装：完整的 install.sh 可执行脚本内容（与套件一致，只在
//     set -euo pipefail 后面加一段默认值：网关域名、回源地址与订阅路径按
//     面板当前值填好，环境变量优先，留空就按默认值走，不再逐项问；其余提示
//     保留），再加 exact 执行命令（sudo bash install.sh、环境变量非交互、
//     DRY_RUN=1 干跑、curl 管道一行到底）。
// 生成的代码里绝不能出现令牌、密钥、真实订阅链接（测试覆盖）。
//
// 检测：POST /api/v1/settings/gateway/check，面板服务端去请求订阅对外域名
// 下的三条路，逐条返回状态，不把敏感内容回传给前端：
//   - /api/v1/public-info：200 通；404 说明 Worker 还是旧版（只接订阅）。
//   - /client-config.json：200 通；404 时看响应体区分旧版 Worker
//     （Not Found）与新版未配上游（upstream not configured）。
//   - /sub/*：三类订阅各测一次（通用订阅、每人订阅取第一个用户、合并订阅
//     取第一个；都是面板已有的令牌，只留在服务端，绝不返回给前端），每类返回
//     状态、节点总数与中转/直连/其它计数（按 sing-box 配置里 outbound 的 tag
//     分）；没有令牌的那类报“无此类型订阅”，不算不通；没有可用订阅时整项报
//     “无订阅可测”。404 时加直连对照探测（见下）。
//     404 时加一次对照探测（1.3.51）：同一条 URL 直接请求面板回源地址；
//     直连是 200 就报“网关没配这条订阅路径”，直连也是 404 再报原来的
//     “订阅令牌不对（或网关没配订阅这条路）”。对照探测的令牌同样只留在服务端。
// 没配订阅对外域名时直接报“未配置订阅对外域名”。三条都带耗时。

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

//go:embed gateway/worker.js gateway/Caddyfile gateway/docker-compose.yml gateway/install.sh gateway/wrangler.toml
var gatewayTemplates embed.FS

// gatewayCheckClient 检测用的 HTTP 客户端（包级变量只为测试能替身超时）。
var gatewayCheckClient = &http.Client{Timeout: 10 * time.Second}

// gatewayPanelOrigin 面板回源地址：协议按 HTTPS 开关，域名用证书域名
// （HTTPS 卡片里填的那个），端口用面板端口。没配证书域名时返回占位符
// https://<面板域名>:11111，needsDomain 为 true，调用方要在 note 里提示替换。
func (s *Server) gatewayPanelOrigin() (origin string, needsDomain bool) {
	port := s.cfg.Port
	if port == 0 {
		port = 11111
	}
	domain := strings.TrimSpace(s.cfg.HTTPSDomain)
	if domain == "" {
		return fmt.Sprintf("https://<面板域名>:%d", port), true
	}
	scheme := "http"
	if s.cfg.HTTPSEnabled {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, domain, port), false
}

// gatewayDomain 网关域名：订阅对外域名；没填时用 example.com 占位。
func (s *Server) gatewayDomain() (string, bool) {
	d := strings.TrimSpace(s.cfg.SubPublicDomain)
	if d == "" {
		return "example.com", true
	}
	return d, false
}

// gatewayFile 生成接口返回的单个部署文件（前端每个文件一个下载按钮）。
type gatewayFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// gatewayCode 生成一种部署方式的代码。返回文件名、展示用的合并内容、提示
// 与真正要部署的文件列表（files）。
// 三档一个都不能少，而且给的必须是能直接用的完整内容：
//   - worker：整份 worker.js（订阅路径已按面板当前值填好）＋ wrangler.toml。
//   - caddy：完整的 docker-compose.yml 与 Caddyfile（值已填好），再加
//     exact 执行命令，三样拼在一份内容里、分节标明文件名，复制整份就能用。
//   - install：完整的 install.sh 可执行脚本内容（值已预填好，交互提示保留），
//     再加 exact 执行命令（sudo bash install.sh、环境变量非交互、DRY_RUN=1 干跑、
//     curl 管道一行到底）。
func (s *Server) gatewayCode(typ string) (filename, content, note string, files []gatewayFile, err error) {
	origin, needsDomain := s.gatewayPanelOrigin()
	domain, noDomain := s.gatewayDomain()
	subPath := s.subPath()

	// 占位符提示：拼在具体类型的 note 前面。
	domainNote := ""
	if needsDomain {
		domainNote = "面板还没填证书域名：代码里 ORIGIN 的 <面板域名> 请换成面板的域名（须先把 A 记录指到面板 IP），再部署。"
	}
	gatewayNote := ""
	if noDomain {
		gatewayNote = "还没填订阅对外域名：代码里的网关域名现在是占位符 example.com，请换成网关域名再部署。"
	}
	noteOf := func() string { return strings.TrimSpace(gatewayNote + " " + domainNote) }

	switch typ {
	case "worker":
		raw, e := gatewayTemplates.ReadFile("gateway/worker.js")
		if e != nil {
			return "", "", "", nil, fmt.Errorf("读 Worker 模板失败：%v", e)
		}
		tpl := string(raw)
		old := `env.ORIGIN || ""`
		if !strings.Contains(tpl, old) {
			return "", "", "", nil, fmt.Errorf("Worker 模板不匹配：找不到 ORIGIN 默认值")
		}
		tpl = strings.Replace(tpl, old, `env.ORIGIN || "`+origin+`"`, 1)
		// 订阅路径：模板里的 subPath 行整行换成填好值的常量（主路径就是
		// s.subPath()，配置为空时它已返回默认 sub）；/sub/ 兜底在路由条件
		// 里，模板自带。生成出来的代码里不留 SUB_PATH 变量。
		subPathAnchor := `const subPath = (env.SUB_PATH || "sub").replace(/^\/+|\/+$/g, "") || "sub";`
		if !strings.Contains(tpl, subPathAnchor) {
			return "", "", "", nil, fmt.Errorf("Worker 模板不匹配：找不到订阅路径行")
		}
		tpl = strings.Replace(tpl, subPathAnchor, `const subPath = "`+subPath+`";`, 1)
		wrangler, e := s.gatewayWrangler(origin, subPath)
		if e != nil {
			return "", "", "", nil, e
		}
		return "worker.js", tpl, domainNote, []gatewayFile{
			{Name: "worker.js", Content: tpl},
			{Name: "wrangler.toml", Content: wrangler},
		}, nil
	case "caddy":
		compose, e := s.gatewayCompose()
		if e != nil {
			return "", "", "", nil, e
		}
		caddyfile, e := s.gatewayCaddyfile(origin, domain)
		if e != nil {
			return "", "", "", nil, e
		}
		var b strings.Builder
		b.WriteString("# ================= docker-compose.yml =================\n")
		b.WriteString("# 保存成 docker-compose.yml（与 Caddyfile 放同一目录）\n")
		b.WriteString(compose)
		b.WriteString("# ================= Caddyfile =================\n")
		b.WriteString("# 保存成 Caddyfile（与 docker-compose.yml 放同一目录）\n")
		b.WriteString(caddyfile)
		b.WriteString("# ================= 目录说明 =================\n")
		b.WriteString("# 没有固定绝对路径：新建一个目录（例如 ~/sub-gateway），把上面两个文件\n")
		b.WriteString("# 放进去，下面的命令都在这个目录里执行；data/config 是相对路径、建在这个\n")
		b.WriteString("# 目录下，client-config.json 放 data/config/ 里。\n")
		b.WriteString("# ================= 执行命令 =================\n")
		b.WriteString("# 在上面两个文件所在目录里执行：\n")
		b.WriteString("mkdir -p ~/sub-gateway && cd ~/sub-gateway\n")
		b.WriteString("mkdir -p data/config\n")
		b.WriteString("# 把签好的 client-config.json 放进 data/config/（没有就先空着，/client-config.json 会 404）\n")
		b.WriteString("docker compose up -d\n")
		b.WriteString("# 自测三条路里的两条（订阅那条带令牌，用面板里的“检测网关”按钮测）：\n")
		b.WriteString("curl -sI https://" + domain + "/healthz\n")
		b.WriteString("curl -s https://" + domain + "/api/v1/public-info\n")
		return "docker-compose.yml", b.String(), noteOf(), []gatewayFile{
			{Name: "docker-compose.yml", Content: compose},
			{Name: "Caddyfile", Content: caddyfile},
		}, nil
	case "install":
		script, e := s.gatewayInstallScript(origin, domain)
		if e != nil {
			return "", "", "", nil, e
		}
		var b strings.Builder
		b.WriteString("# ================= install.sh =================\n")
		b.WriteString("# 保存成 install.sh（值已按面板当前值填好，交互提示保留）\n")
		b.WriteString(script)
		b.WriteString("# ================= 目录说明 =================\n")
		b.WriteString("# install.sh 放哪都行，但必须用 root 跑（sudo bash install.sh）；它会装到\n")
		b.WriteString("# 固定系统路径：Caddy 二进制 /usr/local/bin/caddy、Caddyfile /etc/caddy/Caddyfile、\n")
		b.WriteString("# 配置目录 /etc/sub-gateway/config（client-config.json 放这里）、\n")
		b.WriteString("# systemd 服务 /etc/systemd/system/sub-gateway.service。\n")
		b.WriteString("# ================= 执行命令 =================\n")
		b.WriteString("# 交互安装（逐项问，默认值已填好，直接回车就行）：\n")
		b.WriteString("sudo bash install.sh\n")
		b.WriteString("# 非交互安装（环境变量形式）：\n")
		b.WriteString("sudo GATEWAY_DOMAIN=" + domain + " PANEL_ORIGIN=" + origin + " SUB_PATH=" + subPath + " bash install.sh\n")
		b.WriteString("# 先看不装（干跑，只打印步骤、文件写到临时目录，不动系统，不用 root）：\n")
		b.WriteString("DRY_RUN=1 ROOT_DIR=/tmp/gw-dryrun bash install.sh\n")
		b.WriteString("# 一行到底（从套件地址拉取脚本直接装；先把地址换成你自己的托管地址）：\n")
		b.WriteString("curl -fsSL https://example.com/subscription-gateway-kit/scripts/install.sh | sudo bash\n")
		return "install.sh", b.String(), noteOf(), []gatewayFile{
			{Name: "install.sh", Content: script},
		}, nil
	default:
		return "", "", "", nil, fmt.Errorf("type 只支持 worker/caddy/install")
	}
}

// gatewayCaddyfile 填好值的 Caddyfile：{$GATEWAY_DOMAIN} 填订阅对外域名
// （没填用 example.com 占位），{$PANEL_ORIGIN} 填面板回源地址，
// {$CONFIG_DIR} 填 /data/config，{$SUB_PATH} 填 s.subPath()（订阅路由主路径）。
func (s *Server) gatewayCaddyfile(origin, domain string) (string, error) {
	raw, e := gatewayTemplates.ReadFile("gateway/Caddyfile")
	if e != nil {
		return "", fmt.Errorf("读 Caddyfile 模板失败：%v", e)
	}
	tpl := string(raw)
	for _, kv := range [][2]string{
		{"{$GATEWAY_DOMAIN}", domain},
		{"{$PANEL_ORIGIN}", origin},
		{"{$CONFIG_DIR}", "/data/config"},
		{"{$SUB_PATH}", s.subPath()},
	} {
		if !strings.Contains(tpl, kv[0]) {
			return "", fmt.Errorf("Caddyfile 模板不匹配：找不到 %s", kv[0])
		}
		tpl = strings.ReplaceAll(tpl, kv[0], kv[1])
	}
	return tpl, nil
}

// gatewayCompose docker-compose.yml：与套件一致，但去掉了 env_file 一行
// （值已填进 Caddyfile，不用再建 .env，否则 docker compose 会因找不到
// .env 报错），并在顶部加一行说明。
func (s *Server) gatewayCompose() (string, error) {
	raw, e := gatewayTemplates.ReadFile("gateway/docker-compose.yml")
	if e != nil {
		return "", fmt.Errorf("读 docker-compose 模板失败：%v", e)
	}
	tpl := string(raw)
	envFile := "    env_file:\n      - .env\n"
	if !strings.Contains(tpl, envFile) {
		return "", fmt.Errorf("docker-compose 模板不匹配：找不到 env_file 一行")
	}
	tpl = strings.Replace(tpl, envFile, "", 1)
	return "# 由面板生成：Caddyfile 里的值已填好，不用再建 .env。\n" + tpl, nil
}

// gatewayInstallScript 填好值的 install.sh：与套件一致，只在
// set -euo pipefail 后面加一段默认值（网关域名、回源地址与订阅路径按面板当前值
// 填好，环境变量优先，留空就按默认值走，不再逐项问；其余提示保留）。
func (s *Server) gatewayInstallScript(origin, domain string) (string, error) {
	raw, e := gatewayTemplates.ReadFile("gateway/install.sh")
	if e != nil {
		return "", fmt.Errorf("读 install.sh 模板失败：%v", e)
	}
	tpl := string(raw)
	anchor := "set -euo pipefail\n"
	if !strings.Contains(tpl, anchor) {
		return "", fmt.Errorf("install.sh 模板不匹配：找不到 set -euo pipefail")
	}
	block := "\n# ---- 由面板生成：下面三行是按面板当前值填好的默认值（环境变量优先） ----\n" +
		"GATEWAY_DOMAIN=\"${GATEWAY_DOMAIN:-" + domain + "}\"\n" +
		"PANEL_ORIGIN=\"${PANEL_ORIGIN:-" + origin + "}\"\n" +
		"SUB_PATH=\"${SUB_PATH:-" + s.subPath() + "}\"\n"
	return strings.Replace(tpl, anchor, anchor+block, 1), nil
}

// gatewayWrangler 填好值的 wrangler.toml：ORIGIN 与 SUB_PATH 按面板当前值填，
// CONFIG_UPSTREAM 未发布时留空（Worker 里空值当未配置，/client-config.json 404）。
// 套件通用版里三项都是示例值，按实改。
func (s *Server) gatewayWrangler(origin, subPath string) (string, error) {
	raw, e := gatewayTemplates.ReadFile("gateway/wrangler.toml")
	if e != nil {
		return "", fmt.Errorf("读 wrangler.toml 模板失败：%v", e)
	}
	tpl := string(raw)
	for _, kv := range [][2]string{
		{`ORIGIN = "https://ccpanel.example.com:11111"`, `ORIGIN = "` + origin + `"`},
		{`SUB_PATH = "sub"`, `SUB_PATH = "` + subPath + `"`},
		{`CONFIG_UPSTREAM = "https://raw.githubusercontent.com/example/example/main/client-config.json"`, `CONFIG_UPSTREAM = ""`},
	} {
		if !strings.Contains(tpl, kv[0]) {
			return "", fmt.Errorf("wrangler.toml 模板不匹配：找不到 %s", kv[0])
		}
		tpl = strings.Replace(tpl, kv[0], kv[1], 1)
	}
	return tpl, nil
}

// handleGatewayCode 生成网关部署代码（会话认证）：
// GET /api/v1/settings/gateway/code?type=worker|caddy|install。
func (s *Server) handleGatewayCode(w http.ResponseWriter, r *http.Request) {
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	filename, content, note, files, err := s.gatewayCode(typ)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{
		"type": typ, "filename": filename, "content": content, "note": note, "files": files,
	}))
}

// ---------- 检测 ----------

type gatewayCheckItem struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	Ms     int64  `json:"ms"`
	Reason string `json:"reason"`
	Nodes  int    `json:"nodes,omitempty"`
	// Subs 只给订阅那一项：三类订阅各一行（通用/每人/合并），每行有自己的
	// 状态与中转/直连/其它计数；没有令牌的那类 Skipped=true，不算不通。
	Subs []gatewayCheckSub `json:"subs,omitempty"`
}

// gatewayCheckSub 订阅检测里某一类订阅的结果。
type gatewayCheckSub struct {
	Kind    string `json:"kind"`  // merged | subuser | generic
	Label   string `json:"label"` // 合并订阅 | 每人订阅 | 通用订阅
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"` // 没有这种订阅的令牌，没测，不算不通
	Status  int    `json:"status"`
	Ms      int64  `json:"ms"`
	Reason  string `json:"reason"`
	Nodes   int    `json:"nodes"`
	Relay   int    `json:"relay"`  // tag 含“中转”的节点
	Direct  int    `json:"direct"` // tag 含“直连”的节点
	Other   int    `json:"other"`  // 其余（或 tag 缺失）
}

// handleGatewayCheck 检测网关三条路（会话认证）：
// POST /api/v1/settings/gateway/check。没配订阅对外域名时直接报原因。
func (s *Server) handleGatewayCheck(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSpace(s.cfg.SubPublicDomain)
	if domain == "" {
		// 注意：顶层 ok 不能写 false——okResp 默认 ok:true，前端 api()
		// 见到 data.ok === false 会直接抛错，逐条结果就显示不出来了；
		// 汇总状态放 all_ok。
		writeJSON(w, 200, okResp(map[string]any{
			"all_ok": false, "reason": "未配置订阅对外域名", "checks": []gatewayCheckItem{},
		}))
		return
	}
	items := s.runGatewayChecks("https://"+domain, s.gatewayCheckOrigin(), gatewayCheckClient)
	allOK := true
	for _, it := range items {
		if !it.OK {
			allOK = false
		}
	}
	writeJSON(w, 200, okResp(map[string]any{
		"all_ok": allOK, "domain": domain, "checks": items,
	}))
}

// gatewayCheckOrigin 检测订阅 404 时对照探测用的面板回源地址：没配证书域名
// 时返回 ""（探不了，调用方回落到原来的原因）。
func (s *Server) gatewayCheckOrigin() string {
	origin, needsDomain := s.gatewayPanelOrigin()
	if needsDomain {
		return ""
	}
	return origin
}

// runGatewayChecks 对网关逐条检测（base 是 https://<订阅对外域名>，
// originBase 是面板回源地址，空 = 探不了直连对照）。
// client 由调用方传，测试用桩服务直连。
func (s *Server) runGatewayChecks(base, originBase string, client *http.Client) []gatewayCheckItem {
	return []gatewayCheckItem{
		s.checkGatewayPublicInfo(base, client),
		s.checkGatewayClientConfig(base, client),
		s.checkGatewaySubscription(base, originBase, client),
	}
}

func (s *Server) checkGatewayPublicInfo(base string, client *http.Client) gatewayCheckItem {
	it := gatewayCheckItem{Name: "public-info"}
	start := time.Now()
	resp, err := client.Get(base + "/api/v1/public-info")
	it.Ms = time.Since(start).Milliseconds()
	if err != nil {
		it.Reason = "请求失败：" + err.Error()
		return it
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	it.Status = resp.StatusCode
	switch {
	case resp.StatusCode == 200:
		it.OK = true
		it.Reason = "通"
	case resp.StatusCode == 404:
		it.Reason = "Worker 还是旧版（只接订阅）：请按面板生成的最新 worker.js 重新部署"
	default:
		it.Reason = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}
	return it
}

func (s *Server) checkGatewayClientConfig(base string, client *http.Client) gatewayCheckItem {
	it := gatewayCheckItem{Name: "client-config"}
	start := time.Now()
	resp, err := client.Get(base + "/client-config.json")
	it.Ms = time.Since(start).Milliseconds()
	if err != nil {
		it.Reason = "请求失败：" + err.Error()
		return it
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	it.Status = resp.StatusCode
	switch {
	case resp.StatusCode == 200:
		it.OK = true
		it.Reason = "通"
	case resp.StatusCode == 404 && strings.Contains(string(body), "upstream not configured"):
		it.Reason = "新版 Worker 已部署，但还没配 CONFIG_UPSTREAM：client-config.json 尚未发布，等发布后把直链填到 Worker 变量里"
	case resp.StatusCode == 404 && strings.TrimSpace(string(body)) == "Not Found":
		it.Reason = "Worker 还是旧版（只接订阅）：请按面板生成的最新 worker.js 重新部署"
	case resp.StatusCode == 404:
		it.Reason = "没找到：远程配置文件还没放上去（Caddy 部署时把签好的 client-config.json 放进配置目录），或网关没配这条路"
	default:
		it.Reason = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}
	return it
}

// gatewayCheckTokens 检测订阅用的三类令牌：通用订阅（SubTokenEnc）、
// 每人订阅（取第一个 SubUsers 的令牌）、合并订阅（取第一个 MergedSubs 的
// 令牌）。每类只取第一个能解出来的；解不开或没有就不测那类。令牌只留在服务端。
// 三类都测，不再只挑一条——管理面板模式下通用订阅是空的（本机无落地，节点
// 都在远程面板上，只有合并订阅有节点），只测通用会报“通，0 个节点”，
// 路由明明是通的，容易误导。
func (s *Server) gatewayCheckTokens() map[string]string {
	out := map[string]string{}
	if s.cfg.SubTokenEnc != "" {
		if tok, err := s.decryptSubTokenNow(s.cfg.SubTokenEnc); err == nil && tok != "" {
			out["generic"] = tok
		}
	}
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].TokenEnc != "" {
			if tok, err := s.decryptSubTokenNow(s.cfg.SubUsers[i].TokenEnc); err == nil && tok != "" {
				out["subuser"] = tok
				break
			}
		}
	}
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].TokenEnc != "" {
			if tok, err := s.decryptSubTokenNow(s.cfg.MergedSubs[i].TokenEnc); err == nil && tok != "" {
				out["merged"] = tok
				break
			}
		}
	}
	return out
}

// gatewaySubKindLabel 检测订阅用的令牌种类中文名（显示在检测结果里，
// 让用户知道测的是哪种订阅）。
func gatewaySubKindLabel(kind string) string {
	switch kind {
	case "merged":
		return "合并订阅"
	case "subuser":
		return "每人订阅"
	default:
		return "通用订阅"
	}
}

var gatewayNodeOutboundTypes = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
	"hysteria2": true, "tuic": true,
}

// gatewaySubKinds 订阅检测的三类顺序：合并订阅、每人订阅、通用订阅。
// 展示时按这个顺序分三行。
var gatewaySubKinds = []struct{ kind, label string }{
	{"merged", "合并订阅"},
	{"subuser", "每人订阅"},
	{"generic", "通用订阅"},
}

func (s *Server) checkGatewaySubscription(base, originBase string, client *http.Client) gatewayCheckItem {
	it := gatewayCheckItem{Name: "subscription"}
	tokens := s.gatewayCheckTokens()
	var summary []string
	tested := 0
	allOK := true
	for _, k := range gatewaySubKinds {
		tok, ok := tokens[k.kind]
		if !ok {
			// 没有这种订阅的令牌：那行报无此类型订阅，不算不通。
			it.Subs = append(it.Subs, gatewayCheckSub{Kind: k.kind, Label: k.label, Skipped: true, Reason: "无此类型订阅"})
			summary = append(summary, k.label+"：无此类型订阅")
			continue
		}
		tested++
		sub := s.checkGatewaySubscriptionKind(base, originBase, k.kind, k.label, tok, client)
		it.Subs = append(it.Subs, sub)
		it.Nodes += sub.Nodes
		it.Ms += sub.Ms
		if sub.Status != 0 && it.Status == 0 {
			it.Status = sub.Status
		}
		if !sub.OK {
			allOK = false
		}
		summary = append(summary, k.label+"："+sub.Reason)
	}
	if tested == 0 {
		it.Reason = "无订阅可测：面板里还没有任何订阅链接"
		return it
	}
	it.OK = allOK
	if it.Status == 0 {
		it.Status = 200
	}
	it.Reason = strings.Join(summary, "；")
	return it
}

// checkGatewaySubscriptionKind 测某一类订阅：用该类的令牌拉一次 sing-box 格式，
// 数节点并按 tag 分中转/直连/其它。404 时加直连对照探测（见 gatewaySub404Reason）。
func (s *Server) checkGatewaySubscriptionKind(base, originBase, kind, label, tok string, client *http.Client) gatewayCheckSub {
	sub := gatewayCheckSub{Kind: kind, Label: label}
	subURLPath := "/" + s.subPath() + "/" + tok + "?format=singbox"
	req, err := http.NewRequest("GET", base+subURLPath, nil)
	if err != nil {
		sub.Reason = "请求失败：" + err.Error()
		return sub
	}
	// 带固定 UA（与哲学系客户端一致），面板按 UA 自适应格式；这里已显式
	// 要 singbox 格式，UA 只是走通路。
	req.Header.Set("User-Agent", "Zhexuexi/1.0")
	start := time.Now()
	resp, err := client.Do(req)
	sub.Ms = time.Since(start).Milliseconds()
	if err != nil {
		sub.Reason = "请求失败：" + err.Error()
		return sub
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	sub.Status = resp.StatusCode
	switch {
	case resp.StatusCode == 200:
		var doc struct {
			Outbounds []gatewaySubOutbound `json:"outbounds"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			sub.Reason = "返回了 200 但内容不是 sing-box 格式"
			return sub
		}
		sub.Nodes, sub.Relay, sub.Direct, sub.Other = countGatewayNodes(doc.Outbounds)
		sub.OK = true
		sub.Reason = gatewaySubOKReason(kind, sub.Nodes, sub.Relay, sub.Direct, sub.Other)
	case resp.StatusCode == 404:
		// 对照探测：同一条 URL 直接请求面板回源。直连通 = 网关没配这条订阅
		// 路径（典型就是 1.3.50 的 Worker 只写了 /sub/，面板路径是自定义的
		// /x9f2/）；直连也 404 = 令牌不对（或订阅本身没这条路）。令牌只留在服务端。
		sub.Reason = s.gatewaySub404Reason(originBase, subURLPath, client)
	case resp.StatusCode == 410:
		sub.Reason = "订阅已到期"
	case resp.StatusCode == 429:
		sub.Reason = "请求太频繁，稍后再测"
	default:
		sub.Reason = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}
	return sub
}

// gatewaySubOutbound 订阅检测解析 sing-box 配置用的 outbound（只要类型与 tag）。
type gatewaySubOutbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

// countGatewayNodes 数 sing-box 配置里的节点，并按 outbound 的 tag 分类：
// 含“中转”为中转、含“直连”为直连、其余（或 tag 缺失）为其它。面板生成的
// 节点名本身就带中转/直连后缀，这个口径够用。selector/direct/block 不是节点，不数。
func countGatewayNodes(outbounds []gatewaySubOutbound) (nodes, relay, direct, other int) {
	for _, o := range outbounds {
		if !gatewayNodeOutboundTypes[o.Type] {
			continue
		}
		nodes++
		switch {
		case strings.Contains(o.Tag, "中转"):
			relay++
		case strings.Contains(o.Tag, "直连"):
			direct++
		default:
			other++
		}
	}
	return nodes, relay, direct, other
}

// gatewaySubOKReason 某一类订阅通了的理由：节点数＋中转/直连/其它（只写
// 非零的类）。0 个节点时不写分类；通用订阅 0 节点加一句本机无落地是空的
// （管理面板模式的常见情况，路由是通的）。
func gatewaySubOKReason(kind string, nodes, relay, direct, other int) string {
	if nodes == 0 {
		if kind == "generic" {
			return "通，0 个节点（本机无落地是空的）"
		}
		return "通，0 个节点"
	}
	var parts []string
	if relay > 0 {
		parts = append(parts, fmt.Sprintf("中转 %d", relay))
	}
	if direct > 0 {
		parts = append(parts, fmt.Sprintf("直连 %d", direct))
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("其它 %d", other))
	}
	return fmt.Sprintf("通，%d 个节点（%s）", nodes, strings.Join(parts, "，"))
}

// gatewaySub404Reason 订阅经网关 404 时的对照探测：同一条订阅路径直指面板
// 回源（originBase 为空 = 回源地址还不知道，探不了）。直连 200 说明订阅本身
// 是通的、网关没配这条订阅路径；直连 404 或探失败都回落到原来的原因。
func (s *Server) gatewaySub404Reason(originBase, subURLPath string, client *http.Client) string {
	const fallback = "订阅令牌不对（或网关没配订阅这条路）"
	if originBase == "" {
		return fallback
	}
	req, err := http.NewRequest("GET", originBase+subURLPath, nil)
	if err != nil {
		return fallback
	}
	req.Header.Set("User-Agent", "Zhexuexi/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == 200 {
		return "网关没配这条订阅路径（面板直连是通的，请检查网关的订阅路径配置）"
	}
	return fallback
}
