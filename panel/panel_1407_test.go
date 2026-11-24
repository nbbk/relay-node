package main

// v1.4.7：轻量节点后端切片 5（面板半边）——sing-box 健康上报与详情显示。
//
// 覆盖：
//   - POST /api/v1/node/traffic 的 health 块新增 singbox_running／
//     singbox_restarts／singbox_version，落盘进节点，节点详情的
//     backend_health 原样返回（与 xray_* 同口径）。
//   - 版本断言 1.4.11。

import (
	"strings"
	"testing"
)

// TestNodeTrafficSingboxHealth1407 sing-box 健康字段随上报落盘、详情回得来。
func TestNodeTrafficSingboxHealth1407(t *testing.T) {
	_, h, cookie, cert := setupTrafficNode1404(t)
	// 上报 sing-box 运行中、重启 3 次、版本 1.14.4。
	body := `{"node_id":1,"window_start":1000,"window_end":1060,"users":[],"inbounds":[],"online":[],"health":{"agent_version":"0.1.5","xray_version":"26.11.9","singbox_version":"1.14.4","xray_running":true,"xray_restarts":0,"singbox_running":true,"singbox_restarts":3,"config_version":5,"last_error":"","cpu_percent":3.2,"mem_mb":210}}`
	if code, _ := postTraffic1404(t, h, cert, body); code != 200 {
		t.Fatalf("上报应 200，实际 %d", code)
	}
	code, resp := authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, resp)
	}
	bh := respMap1400(t, resp)["backend_health"].(map[string]any)
	if bh["singbox_running"] != true || bh["singbox_restarts"] != float64(3) || bh["singbox_version"] != "1.14.4" {
		t.Fatalf("sing-box 健康字段不对：%v", bh)
	}
	// 再报一窗 sing-box 未运行、重启 5 次 → 字段跟着变（旧值不残留）。
	body2 := strings.Replace(body, `"window_start":1000,"window_end":1060`, `"window_start":1060,"window_end":1120`, 1)
	body2 = strings.Replace(body2, `"singbox_running":true`, `"singbox_running":false`, 1)
	body2 = strings.Replace(body2, `"singbox_restarts":3`, `"singbox_restarts":5`, 1)
	if code, _ := postTraffic1404(t, h, cert, body2); code != 200 {
		t.Fatalf("第二窗上报应 200，实际 %d", code)
	}
	code, resp = authed1400(t, h, cookie, "GET", "/api/v1/nodes/1", "")
	if code != 200 {
		t.Fatalf("节点详情应 200，实际 %d：%s", code, resp)
	}
	bh = respMap1400(t, resp)["backend_health"].(map[string]any)
	if bh["singbox_running"] != false || bh["singbox_restarts"] != float64(5) {
		t.Fatalf("sing-box 未运行时字段应更新为 false/5：%v", bh)
	}
	// 老 agent（0.1.4）的上报里没有 sing-box 字段：JSON 解码得
	// 零值，handler 照常写 false／0——那是老 agent 的真实状态
	// （它没有 sing-box 监管），不算数据丢失；前端只在节点配了
	// sing-box 入站时才显示该行，老节点不会误报“未运行”。
}

// ---------- 版本断言 ----------

func TestVersion1407(t *testing.T) {
	if Version != "1.4.11" {
		t.Fatalf("Version 应为 1.4.11，实际 %q", Version)
	}
}
