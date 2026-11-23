package main

// 1.3.49 追加件：已登记远程面板加“编辑”（改名＋改地址）。
// 复用 central_test.go 的 newTokenFakeRemote / newTestAddLandingServer /
// waitCentralPolls：假远程只认一枚令牌，另一枚调过去就是 401。

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCentralRemotesUpdate1349(t *testing.T) {
	tok1 := generateRemoteToken()
	tok2 := generateRemoteToken()
	tok3 := generateRemoteToken()
	// fake1b 与 fake1 认同一枚令牌、地址不同：改地址探通就靠它。
	// fake3 认 tok3、且不登记：改过去探不通就靠它。
	fake1 := newTokenFakeRemote(t, tok1)
	fake1b := newTokenFakeRemote(t, tok1)
	fake2 := newTokenFakeRemote(t, tok2)
	fake3 := newTokenFakeRemote(t, tok3)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())

	add := func(name, baseURL, tok string) string {
		req := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(`{"name":"`+name+`","base_url":"`+baseURL+`","token":"`+tok+`"}`))
		rec := httptest.NewRecorder()
		s.handleCentralRemotesAdd(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["ok"] != true {
			t.Fatalf("登记 %s 应成功：%v", name, resp)
		}
		id, _ := resp["id"].(string)
		if id == "" {
			t.Fatalf("登记 %s 应返回 id", name)
		}
		return id
	}
	id1 := add("香港plus", fake1.srv.URL, tok1)
	id2 := add("香港2", fake2.srv.URL, tok2)

	post := func(id, body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/v1/central/remotes/"+id, strings.NewReader(body))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		s.handleCentralRemotesUpdate(rec, req)
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	stored := func() RemotePanel { return s.cfg.Remotes[0] }
	// s.cfg.Remotes 按登记顺序排：[0] 是香港plus，[1] 是香港2。

	// ① 只改名：成功，地址/令牌/ID 不动。
	if code, r := post(id1, `{"name":"香港plus-新","base_url":"`+fake1.srv.URL+`"}`); code != 200 || r["ok"] != true {
		t.Fatalf("只改名应成功：%d %v", code, r)
	}
	if stored().Name != "香港plus-新" || stored().BaseURL != fake1.srv.URL || stored().Token != tok1 || stored().ID != id1 {
		t.Fatalf("只改名后只应变名字：%+v", stored())
	}

	// ② 改地址且探通：成功，地址变了，令牌/ID 不动，缓存快照也换成新地址。
	if code, r := post(id1, `{"name":"香港plus-新","base_url":"`+fake1b.srv.URL+`"}`); code != 200 || r["ok"] != true {
		t.Fatalf("改地址探通应成功：%d %v", code, r)
	}
	if stored().BaseURL != fake1b.srv.URL || stored().Token != tok1 || stored().ID != id1 || stored().Name != "香港plus-新" {
		t.Fatalf("改地址后只应变地址：%+v", stored())
	}
	if snap := s.centralCache[id1]; snap == nil || snap.BaseURL != fake1b.srv.URL || snap.Name != "香港plus-新" {
		t.Fatalf("改地址后缓存快照应换成新地址：%+v", snap)
	}
	// 新地址与令牌写进磁盘 config.json。
	b, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "config.json"))
	if !strings.Contains(string(b), fake1b.srv.URL) || !strings.Contains(string(b), tok1) {
		t.Fatalf("磁盘 config.json 里应存下新地址与原令牌")
	}

	// ③ 改到远程不认令牌的地址（没登记过、不撞）：探不通，拒绝，原值不动。
	if _, r := post(id1, `{"name":"改坏了","base_url":"`+fake3.srv.URL+`"}`); r["ok"] != false || r["side"] != "remote" {
		t.Fatalf("探不通的地址应拒绝且 side=remote：%v", r)
	}
	if stored().Name != "香港plus-新" || stored().BaseURL != fake1b.srv.URL {
		t.Fatalf("探不通时原值应不动：%+v", stored())
	}

	// ④ 改到另一台已登记远程的地址：撞了，400，原值不动。
	if code, _ := post(id1, `{"name":"撞地址","base_url":"`+fake2.srv.URL+`"}`); code != 400 {
		t.Fatalf("地址撞了另一台应 400，实际 %d", code)
	}
	if stored().BaseURL != fake1b.srv.URL {
		t.Fatalf("地址撞了时原值应不动：%+v", stored())
	}

	// ⑤ 格式校验：空名字、地址没写 http://、未知 id。
	if code, _ := post(id1, `{"name":"","base_url":"`+fake1b.srv.URL+`"}`); code != 400 {
		t.Fatalf("空名字应 400，实际 %d", code)
	}
	if code, _ := post(id1, `{"name":"x","base_url":"203.0.113.1:11111"}`); code != 400 {
		t.Fatalf("没写 http:// 的地址应 400，实际 %d", code)
	}
	if code, _ := post("rm-nope", `{"name":"x","base_url":"`+fake1b.srv.URL+`"}`); code != 404 {
		t.Fatalf("未知 id 应 404，实际 %d", code)
	}
	if stored().Name != "香港plus-新" || stored().BaseURL != fake1b.srv.URL || stored().Token != tok1 {
		t.Fatalf("格式校验失败时原值与令牌应不动：%+v", stored())
	}
	// 第二台远程全程没被碰过。
	if s.cfg.Remotes[1].ID != id2 || s.cfg.Remotes[1].BaseURL != fake2.srv.URL || s.cfg.Remotes[1].Token != tok2 {
		t.Fatalf("第二台远程不应被改动：%+v", s.cfg.Remotes[1])
	}

	// 审计里有编辑记录，且没出现令牌明文。
	audit, _ := os.ReadFile(filepath.Join(s.env.PanelHome, "audit.log"))
	if !strings.Contains(string(audit), "编辑远程面板") {
		t.Fatalf("审计里应记一笔编辑远程面板：%s", audit)
	}
	if strings.Contains(string(audit), tok1) || strings.Contains(string(audit), tok2) {
		t.Fatalf("审计日志里出现了令牌明文")
	}
	// 等后台轮询跑完再断言/清理，否则轮询可能撞上假服务器关闭。
	waitCentralPolls(t, s)
}
