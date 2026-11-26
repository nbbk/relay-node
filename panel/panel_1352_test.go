package main

// v1.3.52：集中管理页顶部的节点统计——总数（中转/直连/其它）+ 按面板分行。

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------- 1. 统计计算 ----------

func TestNodeStatsCompute1352(t *testing.T) {
	// 本机：中转 1、直连 1、其它 1。
	local := nodeCount{Relay: 1, Direct: 1, Other: 1}
	remotes := []RemoteStatusSnapshot{
		{
			ID: "rm-a", Name: "香港plus", BaseURL: "https://203.0.113.1:11111", Online: true, LastOKAt: "2026-11-23T00:00:00Z",
			Landings: []remoteLandingSummary{
				{Index: 1, Landing: "203.0.113.10", Links: []labelledLink{
					{Label: "中转", Link: "vless://x"}, {Label: "直连", Link: "vless://y"},
				}},
				{Index: 2, Landing: "203.0.113.11", Links: []labelledLink{
					{Label: "任意门", Link: "vless://a"}, {Label: "直连", Link: "vless://b"}, {Label: "手工备注", Link: "vless://c"},
				}},
			},
		},
		{
			ID: "rm-b", Name: "香港pro", BaseURL: "https://203.0.113.2:11111", Online: true, LastOKAt: "2026-11-23T00:00:00Z",
			Landings: []remoteLandingSummary{
				{Index: 1, Landing: "203.0.113.20", Links: []labelledLink{}},
			},
		},
	}
	st := computeNodeStats(local, remotes)
	// 总数：本机 3 + A（中转 2、直连 2、其它 1）+ B 0 = 8；中转 3、直连 3、其它 2。
	if st.Total != (nodeCount{Relay: 3, Direct: 3, Other: 2}) {
		t.Fatalf("总数不对：%+v", st.Total)
	}
	if st.Total.Total() != 8 {
		t.Fatalf("总数应为 8，实际 %d", st.Total.Total())
	}
	if st.Local.Count != local || st.Local.Name != "本机" || st.Local.ID != "local" {
		t.Fatalf("本机行不对：%+v", st.Local)
	}
	if len(st.Remotes) != 2 {
		t.Fatalf("远程行应为 2，实际 %d", len(st.Remotes))
	}
	a := st.Remotes[0]
	if a.Name != "香港plus" || a.Count != (nodeCount{Relay: 2, Direct: 2, Other: 1}) || a.Offline || a.NeverPolled {
		t.Fatalf("远程 A 行不对：%+v", a)
	}
	b := st.Remotes[1]
	if b.Name != "香港pro" || b.Count != (nodeCount{}) || b.Offline || b.NeverPolled {
		t.Fatalf("远程 B 行不对（无链接的落地应计 0）：%+v", b)
	}
}

// ---------- 2. 远程离线：显示上次数据 + 注明 ----------

func TestNodeStatsOffline1352(t *testing.T) {
	remotes := []RemoteStatusSnapshot{
		{
			ID: "rm-a", Name: "香港plus", BaseURL: "https://203.0.113.1:11111",
			Online: false, LastOKAt: "2026-11-23T00:00:00Z", Error: "连不上",
			Landings: []remoteLandingSummary{
				{Index: 1, Landing: "203.0.113.10", Links: []labelledLink{
					{Label: "中转", Link: "vless://x"}, {Label: "直连", Link: "vless://y"},
				}},
			},
		},
	}
	st := computeNodeStats(nodeCount{}, remotes)
	if st.Total != (nodeCount{Relay: 1, Direct: 1}) {
		t.Fatalf("离线远程的数不应清零：%+v", st.Total)
	}
	row := st.Remotes[0]
	if !row.Offline || row.NeverPolled {
		t.Fatalf("离线行应标 Offline：%+v", row)
	}
	if row.Count.Total() != 2 {
		t.Fatalf("离线行应显示上次的 2 个节点，实际 %+v", row.Count)
	}
}

// ---------- 3. 从没拉成功过：标“尚未拉取”，不是离线 ----------

func TestNodeStatsNeverPolled1352(t *testing.T) {
	remotes := []RemoteStatusSnapshot{
		{ID: "rm-a", Name: "香港2", BaseURL: "https://203.0.113.3:11111", Online: false, Landings: []remoteLandingSummary{}},
	}
	st := computeNodeStats(nodeCount{}, remotes)
	if st.Total != (nodeCount{}) {
		t.Fatalf("从没拉过的远程应计 0：%+v", st.Total)
	}
	row := st.Remotes[0]
	if row.Offline || !row.NeverPolled {
		t.Fatalf("从没拉过的行应标 NeverPolled、不是 Offline：%+v", row)
	}
}

// ---------- 4. 空面板：全 0 ----------

func TestNodeStatsEmpty1352(t *testing.T) {
	st := computeNodeStats(nodeCount{}, nil)
	if st.Total != (nodeCount{}) || st.Local.Count != (nodeCount{}) {
		t.Fatalf("空面板应全 0：%+v", st)
	}
	if len(st.Remotes) != 0 {
		t.Fatalf("空面板远程行应为空，实际 %d", len(st.Remotes))
	}
}

// ---------- 5. Label 分类：中转/任意门→中转，直连→直连，其它→其它 ----------

func TestClassifyNodeLabel1352(t *testing.T) {
	cases := map[string]string{
		"中转": "relay", "任意门": "relay", "直连": "direct",
		"手工备注": "other", "": "other", "中转 ": "relay",
	}
	for label, want := range cases {
		if got := classifyNodeLabel(label); got != want {
			t.Fatalf("Label %q 应分 %q，实际 %q", label, want, got)
		}
	}
}

// ---------- 6. 本机 nodes.txt 读数与缺文件 ----------

func TestLocalNodeCount1352(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, kitDir, realmDir)
	// 缺文件按 0 算（HOME 指到空目录，免得读到真机的 nodes.txt）。
	t.Setenv("HOME", t.TempDir())
	if got := s.localNodeCount(); got != (nodeCount{}) {
		t.Fatalf("缺 nodes.txt 应计 0，实际 %+v", got)
	}
	// 写一份 nodes.txt：1 号落地中转+直连，2 号落地任意门+直连+一行手工链接。
	// parseNodesTxt 只认 中转：/任意门：/直连：三种行头，手工行不进计数。
	nodes := "## 落地 1：203.0.113.1\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:24443?x=1#智利-中转\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?x=1#智利-直连\n## 落地 2：203.0.113.2\n任意门：vless://11111111-2222-3333-4444-555555555555@example.com:30004?x=1#智利-任意门\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.2:24443?x=1#智利-直连\n"
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(nodes), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := s.localNodeCount(); got != (nodeCount{Relay: 2, Direct: 2}) {
		t.Fatalf("本机计数不对：%+v（任意门应计中转）", got)
	}
}

// ---------- 7. /api/v1/central/remotes 返回的 node_stats ----------

func TestCentralRemotesListNodeStats1352(t *testing.T) {
	kitDir := t.TempDir()
	realmDir := t.TempDir()
	s := newTestAddLandingServer(t, kitDir, realmDir)
	t.Setenv("HOME", t.TempDir())
	nodes := "## 落地 1：203.0.113.1\n中转：vless://11111111-2222-3333-4444-555555555555@example.com:24443?x=1#本机-中转\n直连：vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?x=1#本机-直连\n"
	if err := os.WriteFile(filepath.Join(kitDir, "nodes.txt"), []byte(nodes), 0o644); err != nil {
		t.Fatal(err)
	}
	s.cfg.Remotes = []RemotePanel{
		{ID: "rm-a", Name: "香港plus", BaseURL: "https://203.0.113.1:11111", Token: "rpt_test"},
		{ID: "rm-b", Name: "香港pro", BaseURL: "https://203.0.113.2:11111", Token: "rpt_test"},
	}
	s.centralCache["rm-a"] = &RemoteStatusSnapshot{
		ID: "rm-a", Name: "香港plus", BaseURL: "https://203.0.113.1:11111", Online: true, LastOKAt: "2026-11-23T00:00:00Z",
		Landings: []remoteLandingSummary{
			{Index: 1, Landing: "203.0.113.10", Links: []labelledLink{
				{Label: "中转", Link: "vless://x"}, {Label: "直连", Link: "vless://y"},
			}},
		},
	}
	s.centralCache["rm-b"] = &RemoteStatusSnapshot{
		ID: "rm-b", Name: "香港pro", BaseURL: "https://203.0.113.2:11111", Online: false, LastOKAt: "2026-11-23T00:00:00Z", Error: "连不上",
		Landings: []remoteLandingSummary{
			{Index: 1, Landing: "203.0.113.20", Links: []labelledLink{{Label: "直连", Link: "vless://z"}}},
		},
	}
	req := httptest.NewRequest("GET", "/api/v1/central/remotes", nil)
	rec := httptest.NewRecorder()
	s.handleCentralRemotesList(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HTTP 状态=%d 响应=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool `json:"ok"`
		NodeStats struct {
			Total   nodeCount      `json:"total"`
			Local   nodeStatsRow   `json:"local"`
			Remotes []nodeStatsRow `json:"remotes"`
		} `json:"node_stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("ok 应为 true：%s", rec.Body.String())
	}
	// 总数：本机 2 + A 2 + B 1 = 5；中转 2、直连 3。
	if resp.NodeStats.Total != (nodeCount{Relay: 2, Direct: 3}) {
		t.Fatalf("node_stats 总数不对：%+v", resp.NodeStats.Total)
	}
	if resp.NodeStats.Local.Count != (nodeCount{Relay: 1, Direct: 1}) {
		t.Fatalf("node_stats 本机行不对：%+v", resp.NodeStats.Local)
	}
	if len(resp.NodeStats.Remotes) != 2 {
		t.Fatalf("node_stats 远程行应为 2，实际 %d", len(resp.NodeStats.Remotes))
	}
	if resp.NodeStats.Remotes[1].Offline != true || resp.NodeStats.Remotes[1].Count.Total() != 1 {
		t.Fatalf("离线远程行应标离线且保留上次数：%+v", resp.NodeStats.Remotes[1])
	}
	// 响应里不能出现令牌明文。
	if strings.Contains(rec.Body.String(), "rpt_test") {
		t.Fatalf("node_stats 响应里不应出现令牌")
	}
}

// ---------- 8. 版本断言 ----------

func TestVersion1352(t *testing.T) {
	if Version != "1.4.30" {
		t.Fatalf("VERSION 应是 1.4.30，实际 %q", Version)
	}
}
