package main

// ---------- 节点统计（v1.3.52） ----------
// 集中管理页顶部的节点统计：总数（中转/直连/其它）+ 按面板分行
// （本机一行、每台远程面板一行）。
//
// 口径：
//   - 只计数，不标在线/离线——现在的在线探测只是 TCP 握手，标了会误导人，
//     不写。远程失联时行上注明“离线，显示上次数据”，数字是上次拉到的，
//     不清零、也不说成 0 节点；从没拉成功过的标“尚未拉取”。
//   - 数字从面板已有的登记里算，不新增后台任务、不新增轮询、不新增探测，
//     只复用已有调用链：本机读 nodes.txt（订阅同一来源），远程用
//     central/status 快照里落地自带的 links（远程面板本来就下发，
//     这里只是接住、不再丢掉）。
//   - 中转/直连按节点登记的 Label 分，不按名字猜：“中转”与“任意门”都
//     是走中转机的转发（任意门是中转的 door 模式，computeTwinLinks 里
//     relayLabel 就是“中转”或“任意门”）；“直连”是直连落地；其它
//     Label 分不出类型，单独计“其它”，不硬归到某一边。

import (
	"os"
	"strings"
)

// nodeCount 按类型分的节点数。
type nodeCount struct {
	Relay  int `json:"relay"`
	Direct int `json:"direct"`
	Other  int `json:"other"`
}

// Total 三类加总。
func (c nodeCount) Total() int { return c.Relay + c.Direct + c.Other }

// add 累加另一份计数。
func (c *nodeCount) add(o nodeCount) {
	c.Relay += o.Relay
	c.Direct += o.Direct
	c.Other += o.Other
}

// classifyNodeLabel 按节点登记的 Label 分中转/直连/其它。
func classifyNodeLabel(label string) string {
	switch strings.TrimSpace(label) {
	case "中转", "任意门":
		return "relay"
	case "直连":
		return "direct"
	default:
		return "other"
	}
}

// countNodeLinks 数一组节点链接：按 Label 分类计数。
func countNodeLinks(links []labelledLink) nodeCount {
	var c nodeCount
	for _, l := range links {
		switch classifyNodeLabel(l.Label) {
		case "relay":
			c.Relay++
		case "direct":
			c.Direct++
		default:
			c.Other++
		}
	}
	return c
}

// nodeStatsRow 统计里的一行：一个面板（本机或一台远程）的节点数。
type nodeStatsRow struct {
	ID          string    `json:"id"`           // "local" 或远程面板 ID
	Name        string    `json:"name"`         // 显示名
	Offline     bool      `json:"offline"`      // 远程失联时 true：数字是上次拉到的
	NeverPolled bool      `json:"never_polled"` // 从没拉成功过：没有上次数据
	Count       nodeCount `json:"count"`
}

// nodeStats 节点统计：总数 + 本机一行 + 每台远程一行。
type nodeStats struct {
	Total   nodeCount      `json:"total"`
	Local   nodeStatsRow   `json:"local"`
	Remotes []nodeStatsRow `json:"remotes"`
}

// computeNodeStats 纯算数：本机计数 + 各远程快照（用快照里落地 links 计数）。
func computeNodeStats(local nodeCount, remotes []RemoteStatusSnapshot) nodeStats {
	st := nodeStats{
		Local:   nodeStatsRow{ID: "local", Name: "本机", Count: local},
		Remotes: []nodeStatsRow{},
	}
	st.Total.add(local)
	for _, rm := range remotes {
		var c nodeCount
		for _, l := range rm.Landings {
			c.add(countNodeLinks(l.Links))
		}
		row := nodeStatsRow{ID: rm.ID, Name: rm.Name, Count: c}
		if row.Name == "" {
			row.Name = rm.BaseURL
		}
		if !rm.Online {
			if rm.LastOKAt != "" {
				row.Offline = true
			} else {
				row.NeverPolled = true
			}
		}
		st.Remotes = append(st.Remotes, row)
		st.Total.add(c)
	}
	return st
}

// localNodeCount 本机节点统计：读 nodes.txt 的分组链接，按 Label 分类。
// 文件不存在或读失败按 0 个算（管理面板模式本机无落地）；只读已有登记，
// 不跑安装脚本、不触网。
func (s *Server) localNodeCount() nodeCount {
	b, err := os.ReadFile(s.nodesFilePath())
	if err != nil {
		return nodeCount{}
	}
	var c nodeCount
	for _, g := range parseNodesTxt(b) {
		c.add(countNodeLinks(g.Links))
	}
	return c
}
