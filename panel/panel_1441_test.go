package main

// ---------- 1.4.41：布局错乱＋合并订阅勾选树加后端节点＋汇总口径＋顶栏 IP ----------
//
// 四件事：① index.html 213 行多余 </div> 删掉（1.4.39 子选项卡重构引入，
// 8 个 section 逃出 main.content，页面全错位；1.4.40 的顶栏 0.92 是误诊）；
// ② 独立合并订阅勾选树里加后端节点（用户 2026-11-28 18:21 拍板）；
// ③ 集中管理顶部汇总口径标注＋“香港2 0 个”分隔符＋“独立合并订阅”标题重复；
// ④ 管理面板顶栏拿不到本机 IP（loadOverview 在管理面板模式下永远不跑）。

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestVersion1441(t *testing.T) {
	if Version != "1.4.41" {
		t.Fatalf("Version 应为 1.4.41，实际 %q", Version)
	}
	vb, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("读 VERSION 失败：%v", err)
	}
	if strings.TrimSpace(string(vb)) != "1.4.41" {
		t.Fatalf("VERSION 文件应为 1.4.41，实际 %q", strings.TrimSpace(string(vb)))
	}
}

// ---------- ① 布局结构回归 ----------

// TestLayoutStructure1441：index.html 结构断言——12 个 section.page 全是
// main.content 的后代，且每节内 div 开闭平衡。1.4.39 的 213 行多余 </div>
// 就是这类错误：文本包含断言拦不住，只有结构断言能拦。解析器只认标签，
// 跳过注释与 script/style 内容（内联脚本里可能有 "<div" 字面量）。
func TestLayoutStructure1441(t *testing.T) {
	b, err := os.ReadFile("web/index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败：%v", err)
	}
	src := string(b)
	void := map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
	type openTag struct {
		name  string
		attrs string
	}
	var stack []openTag
	type sectionInfo struct {
		id       string
		inMain   bool
		divDepth int
	}
	var sections []sectionInfo
	cur := -1
	i := 0
	for i < len(src) {
		// 注释。
		if strings.HasPrefix(src[i:], "<!--") {
			if j := strings.Index(src[i+4:], "-->"); j >= 0 {
				i += 4 + j + 3
				continue
			}
			break
		}
		if src[i] != '<' {
			i++
			continue
		}
		j := strings.IndexByte(src[i:], '>')
		if j < 0 {
			break
		}
		raw := src[i+1 : i+j]
		i += j + 1
		if raw == "" || raw[0] == '!' || raw[0] == '?' {
			// doctype 与处理指令（<?...?>）不是标签，直接跳过。
			continue
		}
		// 结束标签。
		if raw[0] == '/' {
			name := strings.ToLower(strings.TrimSpace(raw[1:]))
			if name == "script" || name == "style" {
				// script/style 的内容在上面已被整段跳过，这里只弹栈。
			}
			if name == "section" && cur >= 0 {
				if sections[cur].divDepth != 0 {
					t.Fatalf("section %s 内 div 不平衡：%d", sections[cur].id, sections[cur].divDepth)
				}
				cur = -1
			}
			if name == "div" && cur >= 0 {
				sections[cur].divDepth--
			}
			// 弹栈（找最近的同名）。
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].name == name {
					stack = stack[:k]
					break
				}
			}
			continue
		}
		// 开始标签。
		parts := strings.Fields(raw)
		if len(parts) == 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(parts[0], "/"))
		attrs := ""
		if len(raw) > len(parts[0]) {
			attrs = raw[len(parts[0]):]
		}
		selfClosing := strings.HasSuffix(strings.TrimSpace(raw), "/") || void[name]
		// script/style 整段跳过（内容里可能有 "<div" 字面量）。
		if (name == "script" || name == "style") && !selfClosing {
			end := strings.Index(src[i:], "</"+name)
			if end >= 0 {
				i += end
			}
			continue
		}
		if name == "section" && strings.Contains(attrs, "page") {
			inMain := false
			for _, st := range stack {
				if st.name == "main" && strings.Contains(st.attrs, "content") {
					inMain = true
					break
				}
			}
			id := ""
			if k := strings.Index(attrs, `id="`); k >= 0 {
				rest := attrs[k+4:]
				if q := strings.IndexByte(rest, '"'); q >= 0 {
					id = rest[:q]
				}
			}
			sections = append(sections, sectionInfo{id: id, inMain: inMain})
			cur = len(sections) - 1
		}
		if name == "div" && cur >= 0 && !selfClosing {
			sections[cur].divDepth++
		}
		if !selfClosing {
			stack = append(stack, openTag{name: name, attrs: attrs})
		}
	}
	if len(sections) != 12 {
		t.Fatalf("section.page 应有 12 个，实际 %d", len(sections))
	}
	for _, s := range sections {
		if !s.inMain {
			t.Fatalf("section %s 不在 main.content 内（布局会逃逸）", s.id)
		}
	}
	if len(stack) != 0 {
		t.Fatalf("index.html 有未闭合标签：%v", stack)
	}
}

// ---------- ② 后端节点进树 ----------

// setupNodeAddr1441 直接往 store 里加一个指定地址的后端节点（UserIDs=[]，
// 一个不发，等指派时再加人），入站用 1420 的契约入站。
func setupNodeAddr1441(t *testing.T, s *Server, id int64, addr string) *BackendNode {
	t.Helper()
	node := &BackendNode{ID: id, Name: "测试节点-" + addr, Address: addr, UserIDs: []string{}, Inbounds: contractInbounds1420()}
	for i := range node.Inbounds {
		node.Inbounds[i].NodeID = id
	}
	s.nodes.mu.Lock()
	s.nodes.data.Nodes = append(s.nodes.data.Nodes, node)
	s.nodes.mu.Unlock()
	return node
}

// fetchIndMergedLinks1441 拉 /m/ 并 base64 解码成链接行。
func fetchIndMergedLinks1441(t *testing.T, h http.Handler, url string) []string {
	t.Helper()
	rec := fetchIndMerged1434(t, h, url)
	if rec.Code != 200 {
		t.Fatalf("/m/ 拉取状态=%d want 200：%s", rec.Code, rec.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("/m/ 内容应是 base64 列表：%v（实际 %q）", err, rec.Body.String())
	}
	trimmed := strings.TrimRight(string(decoded), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestIndMergedLocalNodeSelectFetch1441：新建独立合并订阅 → 勾本机后端
// 节点 → /m/ 拉到该节点的链接（契约入站 3 条：vless＋anytls＋socks5，
// 无中转路由故无 B 链接）。保存时指派同步（scope=node、targets=[id]）、
// UUID 补齐、节点侧 UserIDs 加人，三条链路都要通。
func TestIndMergedLocalNodeSelectFetch1441(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupNode1427(t, s, 1)
	id, url := createIndMerged1434(t, h, cookie, `{"name":"节点订阅"}`)
	code, resp := putJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id,
		`{"selection":[{"panel":"local","node":1}]}`)
	if code != 200 {
		t.Fatalf("勾本机节点状态=%d want 200：%v", code, resp)
	}
	ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id)
	if ims == nil || len(ims.Selection) != 1 || ims.Selection[0].Node != 1 || ims.Selection[0].Panel != "local" {
		t.Fatalf("勾选后记录里应有本地节点 1，实际 %+v", ims)
	}
	if ims.BackendScope != "node" || len(ims.BackendTargets) != 1 || ims.BackendTargets[0] != "1" {
		t.Fatalf("勾选后指派应同步成 scope=node、targets=[1]，实际 scope=%q targets=%v", ims.BackendScope, ims.BackendTargets)
	}
	if ims.NodeUUIDs["203.0.113.1"] == "" {
		t.Fatalf("勾选后节点地址应补上 UUID，实际 NodeUUIDs=%v", ims.NodeUUIDs)
	}
	key := indMergedSubKey(id)
	if !containsStr1427(nodeUserIDs1427(t, s, 1), key) {
		t.Fatalf("节点 1 的 UserIDs 里应有 %q，实际 %v", key, nodeUserIDs1427(t, s, 1))
	}
	// 列表摘要里有节点名与“后端节点”。
	code, resp = getJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged")
	if code != 200 {
		t.Fatalf("列表状态=%d want 200：%v", code, resp)
	}
	list, _ := resp["subs"].([]any)
	if len(list) != 1 {
		t.Fatalf("列表应有 1 条，实际 %d", len(list))
	}
	row, _ := list[0].(map[string]any)
	summary, _ := row["summary"].(string)
	if !strings.Contains(summary, "测试节点") || !strings.Contains(summary, "后端节点") {
		t.Fatalf("列表摘要应含节点名与“后端节点”，实际 %q", summary)
	}
	// /m/ 拉取：3 条节点链接。
	lines := fetchIndMergedLinks1441(t, h, url)
	if len(lines) != 3 {
		t.Fatalf("/m/ 拉取应有节点 1 的 3 条链接，实际 %d：%v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"vless://", "anytls://", "socks5://", "203.0.113.1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("/m/ 应含 %q，实际：%v", want, lines)
		}
	}
	// 再把节点全取消勾选：指派同步成空 targets，/m/ 不再出节点链接。
	code, resp = putJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id,
		`{"selection":[]}`)
	if code != 200 {
		t.Fatalf("取消勾选状态=%d want 200：%v", code, resp)
	}
	ims = findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id)
	if ims.BackendScope != "node" || len(ims.BackendTargets) != 0 {
		t.Fatalf("取消勾选后指派应是 scope=node、targets 为空，实际 scope=%q targets=%v", ims.BackendScope, ims.BackendTargets)
	}
	if containsStr1427(nodeUserIDs1427(t, s, 1), key) {
		t.Fatalf("取消勾选后节点 1 的 UserIDs 里不应再有 %q", key)
	}
	if lines := fetchIndMergedLinks1441(t, h, url); len(lines) != 0 {
		t.Fatalf("取消勾选后 /m/ 应是空的，实际 %d 条：%v", len(lines), lines)
	}
}

// mockRemoteNodeServer1441 起一个假远程面板：/central/status 回指定版本
// 与节点摘要，/central/nodes/7 回指定节点详情（地址＋入站＋链接模式）。
func mockRemoteNodeServer1441(t *testing.T, version, nodesJSON, detailJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"panel_version":%q,"realm":{"running":true},"disk_pct":10,"landings":[],"nodes":%s}`, version, nodesJSON)
	})
	mux.HandleFunc("/api/v1/central/nodes/7", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(detailJSON))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const remoteNodeDetail1441 = `{"ok":true,"node":{"id":7,"name":"测试机","address":"166.0.17.193","link_mode":"both"},"inbounds":[` +
	`{"inbound_id":11,"inbound_tag":"ss-8338","protocol":"shadowsocks","transport":"tcp","security":"none","port":8338,"ss_method":"aes-256-gcm","ss_password":"testpass12345678","enabled":true},` +
	`{"inbound_id":12,"inbound_tag":"reality-443","protocol":"vless","transport":"reality","security":"reality","port":443,"sni":"www.microsoft.com","reality_public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","reality_short_id":"abcdef01","reality_dest":"www.microsoft.com:443","reality_server_name":"www.microsoft.com","reality_fingerprint":"chrome","flow":"xtls-rprx-vision","enabled":true}` +
	`]}`

const remoteNodes1441 = `[{"node_id":7,"name":"测试机","health":"never","traffic":{"up_period":0,"down_period":0,"up_total":0,"down_total":0}}]`

// registerRemote1441 把假远程面板登记进测试面板。
func registerRemote1441(s *Server, srv *httptest.Server) {
	s.cfg.Remotes = []RemotePanel{{ID: "rm-1441", Name: "香港2", BaseURL: srv.URL, Token: generateRemoteToken()}}
}

// TestIndMergedRemoteNodeNoTwin1441：远程节点无本地同胎——勾选后 /m/
// 只出 SS 链接（入站共享凭证，不用 UUID），VLESS（UUID 制）不出；
// 远程面板不认这个订阅的用户，UUID 制协议的链接本来就出不来。
func TestIndMergedRemoteNodeNoTwin1441(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	srv := mockRemoteNodeServer1441(t, "1.4.40", remoteNodes1441, remoteNodeDetail1441)
	registerRemote1441(s, srv)
	id, url := createIndMerged1434(t, h, cookie, `{"name":"远程节点订阅"}`)
	code, resp := putJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id,
		`{"selection":[{"panel":"rm-1441","node":7}]}`)
	if code != 200 {
		t.Fatalf("勾远程节点状态=%d want 200：%v", code, resp)
	}
	lines := fetchIndMergedLinks1441(t, h, url)
	if len(lines) != 1 {
		t.Fatalf("无同胎时 /m/ 应只有 1 条 SS 链接，实际 %d：%v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "ss://") || !strings.Contains(lines[0], "166.0.17.193:8338") {
		t.Fatalf("/m/ 应是远程节点 SS 入站的链接，实际 %q", lines[0])
	}
}

// TestIndMergedRemoteNodeTwin1441：远程节点有本地同胎（同一台 166.0.17.193
// 既是香港2 登记的测试机、也是本机节点 2）——保存时指派同步把同胎纳
// 入 targets、补 UUID；/m/ 出同胎本地节点的 3 条链接（vless＋anytls＋
// socks5），远程详情那条 SS 链接不再重复出。
func TestIndMergedRemoteNodeTwin1441(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	setupNodeAddr1441(t, s, 2, "166.0.17.193")
	srv := mockRemoteNodeServer1441(t, "1.4.40", remoteNodes1441, remoteNodeDetail1441)
	registerRemote1441(s, srv)
	id, url := createIndMerged1434(t, h, cookie, `{"name":"同胎订阅"}`)
	code, resp := putJSON144(t, h, cookie, "/api/v1/subscriptions/independent-merged/"+id,
		`{"selection":[{"panel":"rm-1441","node":7}]}`)
	if code != 200 {
		t.Fatalf("勾远程节点状态=%d want 200：%v", code, resp)
	}
	ims := findIndependentMergedSubByID(s.cfg.IndependentMergedSubs, id)
	if ims.BackendScope != "node" || len(ims.BackendTargets) != 1 || ims.BackendTargets[0] != "2" {
		t.Fatalf("勾选后指派应同步成 scope=node、targets=[2]（本地同胎），实际 scope=%q targets=%v", ims.BackendScope, ims.BackendTargets)
	}
	if ims.NodeUUIDs["166.0.17.193"] == "" {
		t.Fatalf("勾选后同胎地址应补上 UUID，实际 NodeUUIDs=%v", ims.NodeUUIDs)
	}
	lines := fetchIndMergedLinks1441(t, h, url)
	if len(lines) != 3 {
		t.Fatalf("/m/ 应有同胎节点 2 的 3 条链接，实际 %d：%v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"vless://", "anytls://", "socks5://", "166.0.17.193"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("/m/ 应含 %q，实际：%v", want, lines)
		}
	}
	if strings.Contains(joined, ":8338") {
		t.Fatalf("/m/ 不应再出远程详情那条 SS 链接（同胎已由本机那路出），实际：%v", lines)
	}
}

// TestValidateIndMergedNodeEntries1441：节点条目校验——本机没有的节点拒掉；
// 远程快照里没有的节点拒掉；远程版本低于 1.4.19（没有节点详情接口）拒掉；
// 分组／订阅用户那路（validateMergedSelection）带 Node 的条目直接拒掉。
func TestValidateIndMergedNodeEntries1441(t *testing.T) {
	s := newTestSubServer(t)
	// 本机没有编号 99 的节点。
	if _, _, err := s.validateIndMergedSelection([]SubSelection{{Panel: "local", Node: 99}}); err == nil || !strings.Contains(err.Error(), "没有编号 99 的后端节点") {
		t.Fatalf("勾不存在的本机节点应拒绝，实际 %v", err)
	}
	// 远程快照里没有编号 8 的节点。
	srv := mockRemoteNodeServer1441(t, "1.4.40", remoteNodes1441, remoteNodeDetail1441)
	registerRemote1441(s, srv)
	if _, _, err := s.validateIndMergedSelection([]SubSelection{{Panel: "rm-1441", Node: 8}}); err == nil || !strings.Contains(err.Error(), "没有编号 8 的后端节点") {
		t.Fatalf("勾远程快照里没有的节点应拒绝，实际 %v", err)
	}
	// 远程版本 1.4.18：没有节点详情接口。
	srvOld := mockRemoteNodeServer1441(t, "1.4.18", remoteNodes1441, remoteNodeDetail1441)
	registerRemote1441(s, srvOld)
	if _, _, err := s.validateIndMergedSelection([]SubSelection{{Panel: "rm-1441", Node: 7}}); err == nil || !strings.Contains(err.Error(), "1.4.19+") {
		t.Fatalf("远程版本太旧应拒绝（要 1.4.19+），实际 %v", err)
	}
	// 分组／订阅用户那路不认节点条目。
	if _, _, err := s.validateMergedSelection([]SubSelection{{Panel: "local", Node: 1}}); err == nil || !strings.Contains(err.Error(), "不能有后端节点") {
		t.Fatalf("分组／订阅用户勾选带节点条目应拒绝，实际 %v", err)
	}
	// 规范化：落地与节点条目二选一，两者都空或两者都填的都丢掉；键的格式
	// （排序按 Panel、Landing、Node，节点条目的 Landing=0 故排在前面）。
	sel, key := canonicalSelection([]SubSelection{
		{Panel: "local", Landing: 1},
		{Panel: "local", Node: 2},
		{Panel: "local", Landing: 0, Node: 0},
		{Panel: "local", Landing: 3, Node: 4},
		{Panel: "local", Landing: 1},
	})
	if len(sel) != 2 || key != "local:node:2,local:1" {
		t.Fatalf("规范化应保留 1 落地＋1 节点、键为 local:node:2,local:1，实际 %v 键 %q", sel, key)
	}
}

// ---------- ④ 顶栏本机 IP ----------

// TestMachineIPPrivateOnly1441：本地只有内网 IP 时也问回显——回显回公网
// IP 就优先回公网 IP，回显失败再回内网 IP（v1.4.41 加固；1.4.30 只在
// 本地枚举为空时才问回显）。
func TestMachineIPPrivateOnly1441(t *testing.T) {
	calls, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, []net.IP{net.ParseIP("10.0.0.5")}, nil, url)
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("本地只有内网 IP＋回显回公网 IP 时应回公网 IP，实际 %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("本地只有内网 IP 时应问回显 1 次，实际 %d 次", calls.Load())
	}
	// 回显失败（500）时回内网 IP。
	_, url2 := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, []net.IP{net.ParseIP("10.0.0.5")}, nil, url2)
	if got := machineIP(); got != "10.0.0.5" {
		t.Fatalf("回显失败时应回内网 IP，实际 %q", got)
	}
}

// TestHandleMeMachineIP1441：/api/v1/me 回包带 machine_ip（顶栏 IP 不再
// 依赖总览页，checkMe 在登录时顺手填）。
func TestHandleMeMachineIP1441(t *testing.T) {
	s := newTestSubServer(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	})
	setMachineIPStubs1430(t, []net.IP{net.ParseIP("10.0.0.5")}, nil, url)
	code, resp := getJSON144(t, h, cookie, "/api/v1/me")
	if code != 200 {
		t.Fatalf("/api/v1/me 状态=%d want 200：%v", code, resp)
	}
	if resp["machine_ip"] != "10.0.0.5" {
		t.Fatalf("/api/v1/me 应带 machine_ip=10.0.0.5，实际 %v", resp)
	}
	if resp["version"] != "1.4.41" {
		t.Fatalf("/api/v1/me 的 version 应为 1.4.41，实际 %v", resp["version"])
	}
}
