package main

// ---------- 订阅网关代码生成与检测（v1.3.50） ----------
// 背景：订阅网关的部署代码（Worker / Caddy / 脚本）以前只在套件
// ~/workspace/subscription-gateway-kit/ 里，面板里没有，用户问为什么
// 不能直接在管理面板生成。答案：完全可以，只是之前没做，这里补上。
//
// 生成：GET /api/v1/settings/gateway/code?type=worker|caddy|install，
// 返回 {filename, content, note}。模板是 panel/gateway/ 下的四个文件，
// 与套件里的现成文件逐字一致（worker.js、 Caddyfile、docker-compose.yml、
// install.sh），生成时只把需要填的值换成面板当前值：
//   - Worker：ORIGIN 默认值填面板当前回源地址（协议按 HTTPS 开关，域名用
//     证书域名，端口用面板端口）；没配证书域名时用占位符
//     https://<面板域名>:11111 并在 note 里提示替换。CONFIG_UPSTREAM 不填
//     （留空位，等 client-config.json 发布后填直链）。
//   - Caddy 一键容器：完整的 docker-compose.yml 与 Caddyfile（值已填好），
//     再加 exact 执行命令，三样拼在一份内容里、分节标明文件名。
//     Caddyfile 里 {$GATEWAY_DOMAIN} 填订阅对外域名（没填用 example.com
//     占位），{$PANEL_ORIGIN} 填面板回源地址，{$CONFIG_DIR} 填 /data/config；
//     compose 与套件一致，但去掉了 env_file 一行（值已填进 Caddyfile，
//     不用再建 .env，否则 docker compose 会因找不到 .env 报错）。
//   - 脚本直装：完整的 install.sh 可执行脚本内容（与套件一致，只在
//     set -euo pipefail 后面加一段默认值：网关域名与回源地址按面板当前值
//     填好，环境变量优先，留空就按默认值走，交互提示保留），再加 exact
//     执行命令（bash install.sh、环境变量非交互、DRY_RUN=1 干跑、
//     curl 管道一行到底）。
// 生成的代码里绝不能出现令牌、密钥、真实订阅链接（测试覆盖）。
//
// 检测：POST /api/v1/settings/gateway/check，面板服务端去请求订阅对外域名
// 下的三条路，逐条返回状态，不把敏感内容回传给前端：
//   - /api/v1/public-info：200 通；404 说明 Worker 还是旧版（只接订阅）。
//   - /client-config.json：200 通；404 时看响应体区分旧版 Worker
//     （Not Found）与新版未配上游（upstream not configured）。
//   - /sub/*：服务端用面板自己已有的任一条订阅令牌去拉（令牌只留在服务端，
//     绝不返回给前端），只返回状态与节点数；没有可用订阅时报“无订阅可测”。
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

//go:embed gateway/worker.js gateway/Caddyfile gateway/docker-compose.yml gateway/install.sh
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

// gatewayCode 生成一种部署方式的代码。返回文件名、内容与提示
// （note 为空 = 值都填好了；非空 = 有占位符要用户替换）。
// 三档一个都不能少，而且给的必须是能直接用的完整内容：
//   - worker：整份 worker.js。
//   - caddy：完整的 docker-compose.yml 与 Caddyfile（值已填好），再加
//     exact 执行命令，三样拼在一份内容里、分节标明文件名，复制整份就能用。
//   - install：完整的 install.sh 可执行脚本内容（值已预填好，交互提示保留），
//     再加 exact 执行命令（bash install.sh、环境变量非交互、DRY_RUN=1 干跑、
//     curl 管道一行到底）。
func (s *Server) gatewayCode(typ string) (filename, content, note string, err error) {
	origin, needsDomain := s.gatewayPanelOrigin()
	domain, noDomain := s.gatewayDomain()

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
			return "", "", "", fmt.Errorf("读 Worker 模板失败：%v", e)
		}
		tpl := string(raw)
		old := `env.ORIGIN || ""`
		if !strings.Contains(tpl, old) {
			return "", "", "", fmt.Errorf("Worker 模板不匹配：找不到 ORIGIN 默认值")
		}
		return "worker.js", strings.Replace(tpl, old, `env.ORIGIN || "`+origin+`"`, 1), domainNote, nil
	case "caddy":
		compose, e := s.gatewayCompose()
		if e != nil {
			return "", "", "", e
		}
		caddyfile, e := s.gatewayCaddyfile(origin, domain)
		if e != nil {
			return "", "", "", e
		}
		var b strings.Builder
		b.WriteString("# ================= docker-compose.yml =================\n")
		b.WriteString("# 保存成 docker-compose.yml（与 Caddyfile 放同一目录）\n")
		b.WriteString(compose)
		b.WriteString("# ================= Caddyfile =================\n")
		b.WriteString("# 保存成 Caddyfile（与 docker-compose.yml 放同一目录）\n")
		b.WriteString(caddyfile)
		b.WriteString("# ================= 执行命令 =================\n")
		b.WriteString("# 在上面两个文件所在目录里执行：\n")
		b.WriteString("mkdir -p data/config\n")
		b.WriteString("# 把签好的 client-config.json 放进 data/config/（没有就先空着，/client-config.json 会 404）\n")
		b.WriteString("docker compose up -d\n")
		b.WriteString("# 自测三条路里的两条（订阅那条带令牌，用面板里的“检测网关”按钮测）：\n")
		b.WriteString("curl -sI https://" + domain + "/healthz\n")
		b.WriteString("curl -s https://" + domain + "/api/v1/public-info\n")
		return "docker-compose.yml", b.String(), noteOf(), nil
	case "install":
		script, e := s.gatewayInstallScript(origin, domain)
		if e != nil {
			return "", "", "", e
		}
		var b strings.Builder
		b.WriteString("# ================= install.sh =================\n")
		b.WriteString("# 保存成 install.sh（值已按面板当前值填好，交互提示保留）\n")
		b.WriteString(script)
		b.WriteString("# ================= 执行命令 =================\n")
		b.WriteString("# 交互安装（逐项问，默认值已填好，直接回车就行）：\n")
		b.WriteString("bash install.sh\n")
		b.WriteString("# 非交互安装（环境变量形式）：\n")
		b.WriteString("GATEWAY_DOMAIN=" + domain + " PANEL_ORIGIN=" + origin + " bash install.sh\n")
		b.WriteString("# 先看不装（干跑，只打印步骤、文件写到临时目录，不动系统）：\n")
		b.WriteString("DRY_RUN=1 ROOT_DIR=/tmp/gw-dryrun bash install.sh\n")
		b.WriteString("# 一行到底（从套件地址拉取脚本直接装；先把地址换成你自己的托管地址）：\n")
		b.WriteString("curl -fsSL https://example.com/subscription-gateway-kit/scripts/install.sh | bash\n")
		return "install.sh", b.String(), noteOf(), nil
	default:
		return "", "", "", fmt.Errorf("type 只支持 worker/caddy/install")
	}
}

// gatewayCaddyfile 填好值的 Caddyfile：{$GATEWAY_DOMAIN} 填订阅对外域名
// （没填用 example.com 占位），{$PANEL_ORIGIN} 填面板回源地址，
// {$CONFIG_DIR} 填 /data/config。
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
// set -euo pipefail 后面加一段默认值（网关域名与回源地址按面板当前值填好，
// 环境变量优先，留空就按默认值走，不再逐项问；其余提示保留）。
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
	block := "\n# ---- 由面板生成：下面两行是按面板当前值填好的默认值（环境变量优先） ----\n" +
		"GATEWAY_DOMAIN=\"${GATEWAY_DOMAIN:-" + domain + "}\"\n" +
		"PANEL_ORIGIN=\"${PANEL_ORIGIN:-" + origin + "}\"\n"
	return strings.Replace(tpl, anchor, anchor+block, 1), nil
}

// handleGatewayCode 生成网关部署代码（会话认证）：
// GET /api/v1/settings/gateway/code?type=worker|caddy|install。
func (s *Server) handleGatewayCode(w http.ResponseWriter, r *http.Request) {
	typ := strings.TrimSpace(r.URL.Query().Get("type"))
	filename, content, note, err := s.gatewayCode(typ)
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	writeJSON(w, 200, okResp(map[string]any{
		"type": typ, "filename": filename, "content": content, "note": note,
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
	items := s.runGatewayChecks("https://"+domain, gatewayCheckClient)
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

// runGatewayChecks 对网关逐条检测（base 是 https://<订阅对外域名>）。
// client 由调用方传，测试用桩服务直连。
func (s *Server) runGatewayChecks(base string, client *http.Client) []gatewayCheckItem {
	return []gatewayCheckItem{
		s.checkGatewayPublicInfo(base, client),
		s.checkGatewayClientConfig(base, client),
		s.checkGatewaySubscription(base, client),
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

// gatewayCheckToken 检测订阅那条路用的令牌：通用订阅优先，其次每人订阅、
// 合并订阅；解不开或没有就返回 ""（调用方报“无订阅可测”）。令牌只留在服务端。
func (s *Server) gatewayCheckToken() string {
	if s.cfg.SubTokenEnc != "" {
		if tok, err := s.decryptSubTokenNow(s.cfg.SubTokenEnc); err == nil && tok != "" {
			return tok
		}
	}
	for i := range s.cfg.SubUsers {
		if s.cfg.SubUsers[i].TokenEnc != "" {
			if tok, err := s.decryptSubTokenNow(s.cfg.SubUsers[i].TokenEnc); err == nil && tok != "" {
				return tok
			}
		}
	}
	for i := range s.cfg.MergedSubs {
		if s.cfg.MergedSubs[i].TokenEnc != "" {
			if tok, err := s.decryptSubTokenNow(s.cfg.MergedSubs[i].TokenEnc); err == nil && tok != "" {
				return tok
			}
		}
	}
	return ""
}

var gatewayNodeOutboundTypes = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true,
	"hysteria2": true, "tuic": true,
}

func (s *Server) checkGatewaySubscription(base string, client *http.Client) gatewayCheckItem {
	it := gatewayCheckItem{Name: "subscription"}
	tok := s.gatewayCheckToken()
	if tok == "" {
		it.Reason = "无订阅可测：面板里还没有任何订阅链接"
		return it
	}
	url := base + "/" + s.subPath() + "/" + tok + "?format=singbox"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		it.Reason = "请求失败：" + err.Error()
		return it
	}
	// 带固定 UA（与哲学系客户端一致），面板按 UA 自适应格式；这里已显式
	// 要 singbox 格式，UA 只是走通路。
	req.Header.Set("User-Agent", "Zhexuexi/1.0")
	start := time.Now()
	resp, err := client.Do(req)
	it.Ms = time.Since(start).Milliseconds()
	if err != nil {
		it.Reason = "请求失败：" + err.Error()
		return it
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	it.Status = resp.StatusCode
	switch {
	case resp.StatusCode == 200:
		var doc struct {
			Outbounds []struct {
				Type string `json:"type"`
			} `json:"outbounds"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			it.Reason = "返回了 200 但内容不是 sing-box 格式"
			return it
		}
		for _, o := range doc.Outbounds {
			if gatewayNodeOutboundTypes[o.Type] {
				it.Nodes++
			}
		}
		it.OK = true
		it.Reason = fmt.Sprintf("通，%d 个节点", it.Nodes)
	case resp.StatusCode == 404:
		it.Reason = "订阅令牌不对（或网关没配订阅这条路）"
	case resp.StatusCode == 410:
		it.Reason = "订阅已到期"
	case resp.StatusCode == 429:
		it.Reason = "请求太频繁，稍后再测"
	default:
		it.Reason = fmt.Sprintf("状态码 %d", resp.StatusCode)
	}
	return it
}
