package main

import (
	"strings"
	"testing"
)

// ---------- 1.3.1：订阅 Clash YAML 补 GLOBAL 组＋复制默认不带 mode ----------

// groupMembers131 取某个策略组成员名列表（去引号），找不到组返回 nil。
func groupMembers131(t *testing.T, body, name string) []string {
	t.Helper()
	blk := groupBlock129(t, body, name)
	if blk == "" {
		return nil
	}
	var out []string
	inProxies := false
	for _, ln := range strings.Split(blk, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "proxies:" {
			inProxies = true
			continue
		}
		if !inProxies || !strings.HasPrefix(ln, "- ") {
			continue
		}
		m := strings.TrimSpace(strings.TrimPrefix(ln, "- "))
		m = strings.Trim(m, "\"")
		out = append(out, m)
	}
	return out
}

func TestClashGlobalGroup131(t *testing.T) {
	s := newTestSubServer(t)
	tok := generateSubToken()
	s.cfg.SubTokenHash = hashSubToken(tok)
	rec := callSub(t, s, tok, "/sub/"+tok+"?format=clash", "")
	if rec.Code != 200 {
		t.Fatalf("clash 应 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	// GLOBAL 存在、是 select、排在所有组第一位
	gi := strings.Index(body, "- name: \"GLOBAL\"")
	if gi < 0 {
		t.Fatalf("缺 GLOBAL 组：\n%s", body)
	}
	mi := strings.Index(body, "- name: \"手动选择\"")
	if mi < 0 || gi > mi {
		t.Fatalf("GLOBAL 组应排在手动选择之前")
	}
	blk := groupBlock129(t, body, "GLOBAL")
	if !strings.Contains(blk, "type: select") {
		t.Fatalf("GLOBAL 组应是 select：\n%s", blk)
	}
	// GLOBAL 成员与手动选择一致：全部 4 条节点，一个不少
	want := []string{"智利-中转", "智利-直连", "test-中转", "test-直连"}
	got := groupMembers131(t, body, "GLOBAL")
	manual := groupMembers131(t, body, "手动选择")
	if len(got) != len(want) || len(manual) != len(want) {
		t.Fatalf("GLOBAL 与手动选择都应含 4 条：GLOBAL=%v 手动选择=%v", got, manual)
	}
	for i, w := range want {
		if got[i] != w || manual[i] != w {
			t.Fatalf("GLOBAL 与手动选择成员应一致：GLOBAL=%v 手动选择=%v", got, manual)
		}
	}
	// 规则与 MATCH 不动：默认仍是分流 MATCH,DIRECT
	if !strings.Contains(body, "MATCH,DIRECT") {
		t.Fatalf("默认仍应是分流 MATCH,DIRECT：\n%s", body)
	}
}

// 合并订阅（部分节点集合）同样出 GLOBAL 组，成员随集合走。
func TestClashGlobalGroupSubset131(t *testing.T) {
	links := []subLink{
		{Name: "智利-中转", Link: "vless://11111111-2222-3333-4444-555555555555@example.com:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-中转"},
		{Name: "智利-直连", Link: "vless://11111111-2222-3333-4444-555555555555@203.0.113.1:24443?encryption=none&security=tls&type=ws&host=example.com&path=%2Fexample-path#智利-直连"},
	}
	body := renderClashYAML(links, "split")
	gi := strings.Index(body, "- name: \"GLOBAL\"")
	mi := strings.Index(body, "- name: \"手动选择\"")
	if gi < 0 || mi < 0 || gi > mi {
		t.Fatalf("GLOBAL 组应存在且排第一：\n%s", body)
	}
	got := groupMembers131(t, body, "GLOBAL")
	if len(got) != 2 || got[0] != "智利-中转" || got[1] != "智利-直连" {
		t.Fatalf("子集订阅的 GLOBAL 成员应是 2 条：%v", got)
	}
	// 子集里没有 test 落地的节点，GLOBAL 也不应含
	if strings.Contains(body, "test-中转") || strings.Contains(body, "test-直连") {
		t.Fatalf("子集订阅不应含未选中的节点：\n%s", body)
	}
}
