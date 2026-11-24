package main

// v1.4.1：轻量节点后端切片 2——入站模板＋REALITY。
//
// 覆盖：默认三件套（一键生成，有域名三条、无域名两条、重复生成 400、
// 私钥不进管理接口返回、私钥落盘）；入站 CRUD（增删改查、改端口后配
// 置版本＋1、改入站保留 REALITY 私钥）；443 端口主人校验（TCP 443 与
// UDP 443 各只能一个入站占，报错写明冲突的两个 tag，停用的不占端口）；
// 字段校验（端口范围、未知协议、HY2 缺域名、tag 重复、未登录 401）；
// REALITY 密钥生成与轮换（公钥与私钥成对、short id 8 位十六进制、轮
// 换后公钥变、私钥落盘、非 REALITY 入站轮换 400）；配置文档 ETag 随
// 入站变化、私钥进 agent 配置文档；版本断言。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// createInbound1401 添加一条入站，期望 200，返回入站 id 与解析后的入站 map。
func createInbound1401(t *testing.T, h http.Handler, cookie string, nodeID int64, body string) (int64, map[string]any) {
	t.Helper()
	code, resp := authed1400(t, h, cookie, "POST", "/api/v1/nodes/"+itoa1400(nodeID)+"/inbounds", body)
	if code != 200 {
		t.Fatalf("添加入站应 200，实际 %d：%s", code, resp)
	}
	m := respMap1400(t, resp)
	id, _ := m["inbound_id"].(float64)
	in, _ := m["inbound"].(map[string]any)
	if id == 0 || in == nil {
		t.Fatalf("添加入站返回不对：%s", resp)
	}
	return int64(id), in
}

// listInbounds1401 取某节点的入站列表（管理接口视图）。
func listInbounds1401(t *testing.T, h http.Handler, cookie string, nodeID int64) []map[string]any {
	t.Helper()
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes/"+itoa1400(nodeID)+"/inbounds", "")
	if code != 200 {
		t.Fatalf("入站列表应 200，实际 %d：%s", code, body)
	}
	m := respMap1400(t, body)
	raw, _ := m["inbounds"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

// inboundByTag1401 在列表里按 tag 找入站，没找到返回 nil。
func inboundByTag1401(list []map[string]any, tag string) map[string]any {
	for _, in := range list {
		if in["inbound_tag"] == tag {
			return in
		}
	}
	return nil
}

// nodesJSON1401 读测试面板的 nodes.json，返回解析后的 map。
func nodesJSON1401(t *testing.T, s *Server) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.env.PanelHome, "nodes.json"))
	if err != nil {
		t.Fatalf("nodes.json 应存在：%v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("nodes.json 解析失败：%v", err)
	}
	return m
}

// diskInbound1401 在 nodes.json 里按 tag 找入站（含私钥，供测试核对）。
func diskInbound1401(t *testing.T, s *Server, nodeID int64, tag string) map[string]any {
	t.Helper()
	m := nodesJSON1401(t, s)
	nodes, _ := m["nodes"].([]any)
	for _, n := range nodes {
		node := n.(map[string]any)
		if int64(node["id"].(float64)) != nodeID {
			continue
		}
		ins, _ := node["inbounds"].([]any)
		for _, r := range ins {
			in := r.(map[string]any)
			if in["inbound_tag"] == tag {
				return in
			}
		}
	}
	t.Fatalf("nodes.json 里找不到节点 %d 的入站 %s", nodeID, tag)
	return nil
}

// ---------- 1. 默认三件套 ----------

func TestInboundsDefaultTrio1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	// 有域名：三条都生成。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)
	if code != 200 {
		t.Fatalf("生成三件套应 200，实际 %d：%s", code, body)
	}
	if strings.Contains(body, "reality_private_key") {
		t.Fatalf("三件套返回里不应出现 REALITY 私钥")
	}
	m := respMap1400(t, body)
	created, _ := m["created"].([]any)
	if len(created) != 3 {
		t.Fatalf("有域名时应生成 3 条，实际 %d：%s", len(created), body)
	}
	list := listInbounds1401(t, h, cookie, id)
	reality := inboundByTag1401(list, "reality-443")
	if reality == nil || reality["port"] != float64(443) || reality["transport"] != "reality" ||
		reality["security"] != "reality" || reality["flow"] != "xtls-rprx-vision" {
		t.Fatalf("reality-443 不对：%v", reality)
	}
	pub, _ := reality["reality_public_key"].(string)
	sid, _ := reality["reality_short_id"].(string)
	if pub == "" || len(sid) != 8 {
		t.Fatalf("REALITY 公钥与 short id 不对：pub=%q sid=%q", pub, sid)
	}
	if _, err := base64.RawURLEncoding.DecodeString(pub); err != nil {
		t.Fatalf("REALITY 公钥不是 base64 RawURLEncoding：%v", err)
	}
	xhttp := inboundByTag1401(list, "xhttp-443")
	if xhttp == nil || xhttp["port"] != float64(8443) || xhttp["transport"] != "xhttp" {
		t.Fatalf("xhttp-443 不对：%v", xhttp)
	}
	hy2 := inboundByTag1401(list, "hy2-443")
	if hy2 == nil || hy2["port"] != float64(443) || hy2["transport"] != "quic" || hy2["sni"] != "example.com" {
		t.Fatalf("hy2-443 不对：%v", hy2)
	}
	// 私钥落盘了，且与返回的公钥成对（公钥能从私钥推出——这里只核对
	// 私钥非空且格式对，成对关系由 agent 侧的真 Xray 启动验证）。
	disk := diskInbound1401(t, s, id, "reality-443")
	priv, _ := disk["reality_private_key"].(string)
	if priv == "" {
		t.Fatalf("nodes.json 里 reality-443 应有私钥")
	}
	if _, err := base64.RawURLEncoding.DecodeString(priv); err != nil {
		t.Fatalf("落盘的私钥不是 base64 RawURLEncoding：%v", err)
	}
	if disk["reality_public_key"] != pub {
		t.Fatalf("落盘公钥与返回公钥不一致")
	}

	// 再点一次：三条都已存在 → 400。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`); code != 400 {
		t.Fatalf("重复生成三件套应 400，实际 %d：%s", code, body)
	}

	// 新节点，不填域名：只生成前两条。
	id2, _ := createNode1400(t, h, cookie, "新落地-02", "203.0.113.2")
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/nodes/"+itoa1400(id2)+"/inbounds/default-trio", `{}`)
	if code != 200 {
		t.Fatalf("无域名生成三件套应 200，实际 %d：%s", code, body)
	}
	m = respMap1400(t, body)
	created, _ = m["created"].([]any)
	if len(created) != 2 {
		t.Fatalf("无域名时应生成 2 条，实际 %d：%s", len(created), body)
	}
	note, _ := m["note"].(string)
	if !strings.Contains(note, "域名") {
		t.Fatalf("无域名时的提示应提到域名：%q", note)
	}
	list2 := listInbounds1401(t, h, cookie, id2)
	if inboundByTag1401(list2, "hy2-443") != nil {
		t.Fatalf("无域名时不应生成 hy2-443")
	}
	// 不存在的节点 → 404；未登录 → 401。
	if code, _ := authed1400(t, h, cookie, "POST", "/api/v1/nodes/999/inbounds/default-trio", `{}`); code != 404 {
		t.Fatalf("不存在的节点生成三件套应 404，实际 %d", code)
	}
	if code, _ := authed1400(t, h, "", "POST", "/api/v1/nodes/1/inbounds/default-trio", `{}`); code != 401 {
		t.Fatalf("未登录生成三件套应 401，实际 %d", code)
	}
}

// ---------- 2. 443 端口主人校验 ----------

func TestInboundsPortConflict1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)

	// 再加一条 TCP 443 入站：与 reality-443 撞 → 400，报错写明两个 tag。
	code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"tls-443","protocol":"vless","transport":"tcp","security":"tls","port":443,"sni":"example.com"}`)
	if code != 400 || !strings.Contains(body, "reality-443") || !strings.Contains(body, "tls-443") || !strings.Contains(body, "TCP 443") {
		t.Fatalf("TCP 443 冲突应 400 且写明两个 tag，实际 %d：%s", code, body)
	}
	// 再加一条 UDP 443 入站（TUIC）：与 hy2-443 撞 → 400，报错写明两个 tag。
	code, body = authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"tuic-443","protocol":"tuic","transport":"quic","security":"tls","port":443,"sni":"example.com"}`)
	if code != 400 || !strings.Contains(body, "hy2-443") || !strings.Contains(body, "tuic-443") || !strings.Contains(body, "UDP 443") {
		t.Fatalf("UDP 443 冲突应 400 且写明两个 tag，实际 %d：%s", code, body)
	}
	// 停用 hy2-443 后再加 TUIC UDP 443：停用的不占端口，应通过。
	list := listInbounds1401(t, h, cookie, 1)
	hy2 := inboundByTag1401(list, "hy2-443")
	hy2ID, _ := hy2["inbound_id"].(float64)
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(hy2ID)), `{"enabled":false}`)
	if code != 200 {
		t.Fatalf("停用 hy2-443 应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"tuic-443","protocol":"tuic","transport":"quic","security":"tls","port":443,"sni":"example.com"}`); code != 200 {
		t.Fatalf("hy2 停用后加 TUIC UDP 443 应 200，实际 %d：%s", code, body)
	}
	// 改端口也能解冲突：reality-443 改到 4443 后，TCP 443 空出来了。
	reality := inboundByTag1401(list, "reality-443")
	rID, _ := reality["inbound_id"].(float64)
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(rID)), `{"port":4443}`)
	if code != 200 {
		t.Fatalf("reality-443 改端口应 200，实际 %d：%s", code, body)
	}
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"tls-443","protocol":"vless","transport":"tcp","security":"tls","port":443,"sni":"example.com"}`); code != 200 {
		t.Fatalf("reality 改走后加 TLS TCP 443 应 200，实际 %d：%s", code, body)
	}
}

// ---------- 3. 入站 CRUD ----------

func TestInboundsCRUD1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	// 加一条 SS 备用入站。
	inID, in := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":8388,"ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`)
	if in["port"] != float64(8388) || in["ss_method"] != "aes-256-gcm" {
		t.Fatalf("SS 入站不对：%v", in)
	}
	// 列表里有它；详情接口里也有，且私钥不在返回里（本条没私钥，但
	// 下面三件套那条会验）。
	list := listInbounds1401(t, h, cookie, id)
	if len(list) != 1 || inboundByTag1401(list, "ss-8388") == nil {
		t.Fatalf("列表应有 ss-8388：%v", list)
	}
	// 改端口 → 配置版本＋1。
	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	m := respMap1400(t, body)
	node, _ := m["node"].(map[string]any)
	verBefore, _ := node["config_version"].(float64)
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(inID), `{"port":18388}`)
	if code != 200 {
		t.Fatalf("改入站端口应 200，实际 %d：%s", code, body)
	}
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	m = respMap1400(t, body)
	node, _ = m["node"].(map[string]any)
	if verAfter, _ := node["config_version"].(float64); verAfter != verBefore+1 {
		t.Fatalf("改入站后配置版本应＋1：%v → %v", verBefore, verAfter)
	}
	// 删入站 → 列表空了；再删 → 404。
	code, body = authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1/inbounds/"+itoa1400(inID), "")
	if code != 200 {
		t.Fatalf("删入站应 200，实际 %d：%s", code, body)
	}
	if list := listInbounds1401(t, h, cookie, id); len(list) != 0 {
		t.Fatalf("删完列表应为空：%v", list)
	}
	if code, _ := authed1400(t, h, cookie, "DELETE", "/api/v1/nodes/1/inbounds/"+itoa1400(inID), ""); code != 404 {
		t.Fatalf("重复删入站应 404，实际 %d", code)
	}
	// 改不存在的入站 → 404；不存在的节点 → 404。
	if code, _ := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/999", `{"port":9999}`); code != 404 {
		t.Fatalf("改不存在的入站应 404，实际 %d", code)
	}
	if code, _ := authed1400(t, h, cookie, "GET", "/api/v1/nodes/999/inbounds", ""); code != 404 {
		t.Fatalf("不存在的节点入站列表应 404，实际 %d", code)
	}
	// 改入站保留 REALITY 私钥：三件套后改 reality-443 的端口，落盘私
	// 钥不变；详情接口返回里没有私钥。
	authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)
	before := diskInbound1401(t, s, id, "reality-443")
	privBefore, _ := before["reality_private_key"].(string)
	list = listInbounds1401(t, h, cookie, id)
	rID, _ := inboundByTag1401(list, "reality-443")["inbound_id"].(float64)
	code, body = authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(rID)), `{"port":4443}`)
	if code != 200 {
		t.Fatalf("改 reality-443 端口应 200，实际 %d：%s", code, body)
	}
	after := diskInbound1401(t, s, id, "reality-443")
	if after["reality_private_key"] != privBefore {
		t.Fatalf("改入站不应动 REALITY 私钥")
	}
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 || strings.Contains(body, "reality_private_key") {
		t.Fatalf("节点详情返回里不应出现 REALITY 私钥：%d", code)
	}
}

// ---------- 4. 字段校验 ----------

func TestInboundsValidation1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	bad := []struct{ name, body string }{
		{"端口 0", `{"inbound_tag":"a","protocol":"vless","transport":"tcp","security":"none","port":0}`},
		{"端口超范围", `{"inbound_tag":"a","protocol":"vless","transport":"tcp","security":"none","port":70000}`},
		{"未知协议", `{"inbound_tag":"a","protocol":"wireguard","transport":"tcp","security":"none","port":51820}`},
		{"HY2 缺域名", `{"inbound_tag":"a","protocol":"hysteria2","transport":"quic","security":"tls","port":443}`},
		// SS 缺密码不再 400：v1.4.10 起新建时自动生成（见
		// TestInboundBackupDefaults1410）；缺方法仍 400。
		{"SS 缺方法", `{"inbound_tag":"a","protocol":"shadowsocks","transport":"tcp","security":"none","port":8388,"ss_password":"testpassword123456"}`},
		{"VLESS 传输与安全不配", `{"inbound_tag":"a","protocol":"vless","transport":"quic","security":"tls","port":443}`},
		{"空 tag", `{"inbound_tag":"","protocol":"vless","transport":"tcp","security":"none","port":8443}`},
	}
	for _, c := range bad {
		if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds", c.body); code != 400 {
			t.Fatalf("%s 应 400，实际 %d：%s", c.name, code, body)
		}
	}
	// tag 重复。
	createInbound1401(t, h, cookie, 1,
		`{"inbound_tag":"ss-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":8388,"ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`)
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"ss-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":18388,"ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`); code != 400 || !strings.Contains(body, "重复") {
		t.Fatalf("tag 重复应 400 且写明重复，实际 %d：%s", code, body)
	}
	// REALITY 入站不填 SNI：面板填占位默认值，应通过且 SNI 是占位值。
	_, in := createInbound1401(t, h, cookie, 1,
		`{"inbound_tag":"reality-9443","protocol":"vless","transport":"reality","security":"reality","port":9443}`)
	if in["sni"] != "www.example.com" || in["reality_dest"] != "www.example.com:443" {
		t.Fatalf("REALITY 入站 SNI／dest 应填占位默认值：%v", in)
	}
	// 未登录 → 401。
	if code, _ := authed1400(t, h, "", "GET", "/api/v1/nodes/1/inbounds", ""); code != 401 {
		t.Fatalf("未登录取入站列表应 401，实际 %d", code)
	}
}

// ---------- 5. REALITY 密钥轮换 ----------

func TestRotateRealityKey1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)

	list := listInbounds1401(t, h, cookie, id)
	reality := inboundByTag1401(list, "reality-443")
	pubBefore, _ := reality["reality_public_key"].(string)
	sidBefore, _ := reality["reality_short_id"].(string)
	rID, _ := reality["inbound_id"].(float64)
	privBefore, _ := diskInbound1401(t, s, id, "reality-443")["reality_private_key"].(string)

	code, body := authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	m := respMap1400(t, body)
	node, _ := m["node"].(map[string]any)
	verBefore, _ := node["config_version"].(float64)

	code, body = authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(rID))+"/rotate-reality-key", `{}`)
	if code != 200 {
		t.Fatalf("轮换 REALITY 密钥应 200，实际 %d：%s", code, body)
	}
	if strings.Contains(body, "reality_private_key") {
		t.Fatalf("轮换返回里不应出现私钥")
	}
	m = respMap1400(t, body)
	in, _ := m["inbound"].(map[string]any)
	pubAfter, _ := in["reality_public_key"].(string)
	sidAfter, _ := in["reality_short_id"].(string)
	if pubAfter == "" || pubAfter == pubBefore || sidAfter == "" || sidAfter == sidBefore {
		t.Fatalf("轮换后公钥与 short id 都应变：%q→%q，%q→%q", pubBefore, pubAfter, sidBefore, sidAfter)
	}
	if note, _ := m["note"].(string); !strings.Contains(note, "重新导入") {
		t.Fatalf("轮换提示应写明旧链接失效须重新导入：%q", note)
	}
	after := diskInbound1401(t, s, id, "reality-443")
	if after["reality_private_key"] == privBefore || after["reality_private_key"] == "" {
		t.Fatalf("轮换后落盘私钥应变且非空")
	}
	if after["reality_public_key"] != pubAfter {
		t.Fatalf("落盘公钥与返回公钥不一致")
	}
	code, body = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	m = respMap1400(t, body)
	node, _ = m["node"].(map[string]any)
	if verAfter, _ := node["config_version"].(float64); verAfter != verBefore+1 {
		t.Fatalf("轮换后配置版本应＋1：%v → %v", verBefore, verAfter)
	}

	// 非 REALITY 入站轮换 → 400；不存在的入站 → 404。
	hy2 := inboundByTag1401(list, "hy2-443")
	hy2ID, _ := hy2["inbound_id"].(float64)
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/"+itoa1400(int64(hy2ID))+"/rotate-reality-key", `{}`); code != 400 {
		t.Fatalf("非 REALITY 入站轮换应 400，实际 %d：%s", code, body)
	}
	if code, _ := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/999/rotate-reality-key", `{}`); code != 404 {
		t.Fatalf("不存在的入站轮换应 404，实际 %d", code)
	}
}

// ---------- 6. 配置文档 ETag 随入站变化，私钥进 agent 配置 ----------

func TestInboundsConfigDocETag1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	s.nodes.mu.Lock()
	node := s.nodes.getNode(1)
	docBefore := s.nodes.configDocOf(node, nil)
	etagBefore := configETag(docBefore)
	s.nodes.mu.Unlock()

	authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)

	s.nodes.mu.Lock()
	node = s.nodes.getNode(1)
	docAfter := s.nodes.configDocOf(node, nil)
	etagAfter := configETag(docAfter)
	s.nodes.mu.Unlock()
	if etagBefore == etagAfter {
		t.Fatalf("加了入站后配置 ETag 应变：%s", etagBefore)
	}
	if len(docAfter.Inbounds) != 3 {
		t.Fatalf("配置文档应有 3 条入站，实际 %d", len(docAfter.Inbounds))
	}
	// agent 配置文档里有私钥（agent 渲染 Xray 配置要用）。
	for _, in := range docAfter.Inbounds {
		if in.InboundTag == "reality-443" && in.RealityPrivateKey == "" {
			t.Fatalf("agent 配置文档里的 reality-443 应带私钥")
		}
	}
}

// ---------- 7. 用户进配置文档：按节点地址取 UUID，缺失现生成 ----------

func TestNodeBackendUsers1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")
	s.nodes.mu.Lock()
	node := s.nodes.getNode(1)
	s.nodes.mu.Unlock()

	// 一个启用、一个停用：停用的不下发。
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-test1", Name: "测试用户"})
	s.cfg.SubUsers = append(s.cfg.SubUsers, SubUser{ID: "su-test2", Name: "停用用户", Disabled: true})
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-test1", Name: "合并用户"})

	// 新建节点默认一个用户不发（§5.3：老用户不自动上新节点，须手动勾）。
	users, changed := s.nodeBackendUsersForNode(node)
	if changed {
		t.Fatalf("新建节点默认不发用户，不应生成 UUID（changed 应 false）")
	}
	if len(users) != 0 {
		t.Fatalf("新建节点默认一个用户不发，实际 %d", len(users))
	}
	// 指派两个用户后才下发。
	if code, body := authed1400(t, h, cookie, "PUT", "/api/v1/nodes/1/users", `{"user_ids":["subuser:su-test1","merged:ms-test1"]}`); code != 200 {
		t.Fatalf("指派应 200，实际 %d：%s", code, body)
	}
	users, changed = s.nodeBackendUsersForNode(node)
	if !changed {
		t.Fatalf("第一次取用户应生成新 UUID（changed=true）")
	}
	if len(users) != 2 {
		t.Fatalf("应下发 2 个用户（停用的排除），实际 %d", len(users))
	}
	u1 := users[0]
	if u1.UserID != "subuser:su-test1" || u1.VLESSUUID == "" || u1.Email != "u-sub-su-test1@node" {
		t.Fatalf("用户记录不对：%+v", u1)
	}
	if u2 := users[1]; u2.UserID != "merged:ms-test1" || u2.VLESSUUID == "" {
		t.Fatalf("合并用户记录不对：%+v", u2)
	}
	// UUID 已写回 config，且两用户互不相同。
	if got := s.cfg.SubUsers[0].NodeUUIDs["203.0.113.1"]; got != u1.VLESSUUID {
		t.Fatalf("UUID 应写回用户 NodeUUIDs，实际 %q", got)
	}
	if users[0].VLESSUUID == users[1].VLESSUUID {
		t.Fatalf("两用户 UUID 不应相同")
	}
	// 第二次取：UUID 稳定、不再改 config。
	users2, changed2 := s.nodeBackendUsersForNode(node)
	if changed2 {
		t.Fatalf("第二次取用户不应再改 config")
	}
	if users2[0].VLESSUUID != users[0].VLESSUUID {
		t.Fatalf("UUID 应稳定不变")
	}
	// 配置文档带用户，ETag 随用户数变。
	doc := s.nodes.configDocOf(node, users)
	if len(doc.Users) != 2 {
		t.Fatalf("配置文档应带 2 个用户，实际 %d", len(doc.Users))
	}
	docEmpty := s.nodes.configDocOf(node, nil)
	if configETag(doc) == configETag(docEmpty) {
		t.Fatalf("用户变化后配置 ETag 应变")
	}
}

// ---------- 8. 监听地址走配置（变量原则 2026-11-23） ----------

func TestInboundListen1401(t *testing.T) {
	s := newTestServer1400(t)
	h := s.routes()
	cookie := loginTestCookie(t, h)
	id, _ := createNode1400(t, h, cookie, "新落地-01", "203.0.113.1")

	// 指定监听 IP：落盘与返回都用配置值。
	_, in := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss-8388","protocol":"shadowsocks","transport":"tcp","security":"none","port":8388,"listen":"203.0.113.1","ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`)
	if in["listen"] != "203.0.113.1" {
		t.Fatalf("监听地址应用配置值，实际 %v", in["listen"])
	}
	// 不填监听：默认 0.0.0.0（监听全部网卡）。
	_, in2 := createInbound1401(t, h, cookie, id,
		`{"inbound_tag":"ss-8389","protocol":"shadowsocks","transport":"tcp","security":"none","port":8389,"ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`)
	if in2["listen"] != "0.0.0.0" {
		t.Fatalf("不填监听应默认 0.0.0.0，实际 %v", in2["listen"])
	}
	// 非法监听地址：400，不让存。
	if code, body := authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds",
		`{"inbound_tag":"ss-bad","protocol":"shadowsocks","transport":"tcp","security":"none","port":8390,"listen":"not-an-ip","ss_method":"aes-256-gcm","ss_password":"testpassword123456"}`); code != 400 {
		t.Fatalf("非法监听地址应 400，实际 %d：%s", code, body)
	}
	// 三件套的监听也是配置值（0.0.0.0），且进 agent 配置文档。
	authed1400(t, h, cookie, "POST", "/api/v1/nodes/1/inbounds/default-trio", `{"domain":"example.com"}`)
	s.nodes.mu.Lock()
	node := s.nodes.getNode(1)
	doc := s.nodes.configDocOf(node, nil)
	s.nodes.mu.Unlock()
	for _, ib := range doc.Inbounds {
		if (ib.InboundTag == "reality-443" || ib.InboundTag == "xhttp-443" || ib.InboundTag == "hy2-443") && ib.Listen != "0.0.0.0" {
			t.Fatalf("三件套 %s 监听应是 0.0.0.0，实际 %q", ib.InboundTag, ib.Listen)
		}
	}
	if got := inboundByTag1401(listInbounds1401(t, h, cookie, id), "ss-8388")["listen"]; got != "203.0.113.1" {
		t.Fatalf("列表里的监听地址应是配置值，实际 %v", got)
	}
}

// ---------- 9. 版本断言 ----------

func TestVersion1401(t *testing.T) {
	if Version != "1.4.13" {
		t.Fatalf("VERSION 应是 1.4.13，实际 %q", Version)
	}
}
