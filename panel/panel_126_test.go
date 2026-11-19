package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.2.6：集中管理落地名称透传 ----------

// 被管面板 central/status 里落地自带 name（displayName：设了备注名就是
// 备注名，没设就回退成落地地址；第 3 个落地连 name 字段都没有）。
const fakeRemoteStatus126 = `{"ok":true,"panel_version":"1.2.5","realm":{"running":true},"disk_pct":42.5,"landings":[
{"index":1,"landing":"203.0.113.1","domain":"example.com","name":"智利","mode":"standard","ports":[{"proto":"tcp","listen":24443,"remote":24443}],"online":true,"latency_ms":407},
{"index":2,"landing":"203.0.113.2","domain":"node2.example.com","name":"203.0.113.2","mode":"door","ports":[{"proto":"tcp","listen":20443,"remote":443}],"online":false,"latency_ms":null},
{"index":3,"landing":"203.0.113.3","domain":"node3.example.com","mode":"standard","ports":[],"online":null,"latency_ms":null}
]}`

func newFakeRemote126(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fakeRemoteStatus126))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchRemoteStatusNameKept(t *testing.T) {
	srv := newFakeRemote126(t)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	rem := RemotePanel{ID: "rm-126", Name: "测试远程", BaseURL: srv.URL, Token: tok}
	snap, err := s.fetchRemoteStatus(rem)
	if err != nil {
		t.Fatalf("fetchRemoteStatus: %v", err)
	}
	if len(snap.Landings) != 3 {
		t.Fatalf("应有 3 个落地，实际：%+v", snap.Landings)
	}
	if snap.Landings[0].Name != "智利" {
		t.Fatalf("第 1 个落地应保留备注名“智利”，实际 %q", snap.Landings[0].Name)
	}
	if snap.Landings[1].Name != "" {
		t.Fatalf("第 2 个落地 name 回退成地址，应清成空（未设置），实际 %q", snap.Landings[1].Name)
	}
	if snap.Landings[2].Name != "" {
		t.Fatalf("第 3 个落地没有 name 字段，应为空，实际 %q", snap.Landings[2].Name)
	}
	if snap.Landings[0].Landing != "203.0.113.1" || snap.Landings[0].LatencyMs == nil || *snap.Landings[0].LatencyMs != 407 {
		t.Fatalf("其他字段不应被名字逻辑动到：%+v", snap.Landings[0])
	}
}

// 聚合接口 GET /api/v1/central/remotes/{id}/landings 应把名字原样交给前端。
func TestCentralRemoteLandingsNameServed(t *testing.T) {
	srv := newFakeRemote126(t)
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	body := `{"name":"测试远程","base_url":"` + srv.URL + `","token":"` + tok + `"}`
	req := httptest.NewRequest("POST", "/api/v1/central/remotes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCentralRemotesAdd(rec, req)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("登记应返回 id：%v", resp)
	}
	req2 := httptest.NewRequest("GET", "/api/v1/central/remotes/"+id+"/landings", nil)
	req2.SetPathValue("id", id)
	rec2 := httptest.NewRecorder()
	s.handleCentralRemoteLandings(rec2, req2)
	var out struct {
		OK       bool `json:"ok"`
		Landings []struct {
			Index   int    `json:"index"`
			Name    string `json:"name"`
			Landing string `json:"landing"`
		} `json:"landings"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil || !out.OK {
		t.Fatalf("聚合接口应成功：%s", rec2.Body.String())
	}
	if len(out.Landings) != 3 || out.Landings[0].Name != "智利" || out.Landings[1].Name != "" || out.Landings[2].Name != "" {
		t.Fatalf("聚合接口返回的名字不对：%+v", out.Landings)
	}
}
