package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 1.3.34：随时更换中转端口（含自定义端口） ----------
// 用户 2026-11-22 提议：万一中转机端口被封，要能直接换端口。订阅编辑
// 弹窗里按落地逐条显示当前中转端口，每条可换：填自定义端口，或留空
// 自动分配。自定义端口须在用户端口段内、不撞任何已占端口；撞了报错、
// 不应用；换完订阅链接跟着变、用量不清零、旧端口释放回池子；下发失败
// 时本地记录与远程集合都回滚到旧端口。

// fakeChangePortRemote1334 可控的假远程面板：GET 返回它存的转发与已占
// 端口（可配成 404，模拟 1.3.33 之前的旧远程）；POST 按 skipPorts 模拟
// 远程校验跳过（撞端口），并记下收到的每一份下发集合。
type fakeChangePortRemote1334 struct {
	srv       *httptest.Server
	posts     []map[string]any
	forwards  []UserForward
	get404    bool
	skipPorts map[int]bool
}

func newFakeChangePortRemote1334(t *testing.T) *fakeChangePortRemote1334 {
	t.Helper()
	f := &fakeChangePortRemote1334{
		forwards: []UserForward{
			{Key: "merged:ms-a", SubKind: "merged", SubID: "ms-a", SubName: "合并一", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30000, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1},
			{Key: "merged:ms-b", SubKind: "merged", SubID: "ms-b", SubName: "合并二", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30001, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1},
			{Key: "merged:ms-test", SubKind: "merged", SubID: "ms-test", SubName: "测试一番", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30002, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1},
		},
		skipPorts: map[int]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/central/user-forwards", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			if f.get404 {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"ok":false,"error":"not found"}`))
				return
			}
			used := []int{80, 443, 11111}
			for _, fw := range f.forwards {
				used = append(used, fw.ListenPort)
			}
			b, _ := json.Marshal(map[string]any{"ok": true, "panel_version": "1.3.33", "forwards": f.forwards, "used_ports": used})
			_, _ = w.Write(b)
			return
		}
		var req struct {
			Key      string               `json:"key"`
			Forwards []centralUserForward `json:"forwards"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		applied, skipped := 0, 0
		var kept []UserForward
		for _, fw := range f.forwards {
			if fw.Key != req.Key {
				kept = append(kept, fw)
			}
		}
		for _, cf := range req.Forwards {
			if f.skipPorts[cf.ListenPort] {
				skipped++
				continue
			}
			applied++
			kept = append(kept, UserForward{Key: req.Key, SubKind: "merged", SubID: strings.TrimPrefix(req.Key, "merged:"), Panel: "local", LandingAddr: cf.LandingAddr, ListenPort: cf.ListenPort, LimitBytes: cf.LimitBytes, LimitMode: cf.LimitMode, ResetMode: cf.ResetMode, ResetDay: cf.ResetDay, NextResetAt: cf.NextResetAt, Disabled: cf.Disabled})
		}
		f.forwards = kept
		f.posts = append(f.posts, map[string]any{"key": req.Key, "forwards": req.Forwards, "applied": applied, "skipped": skipped})
		b, _ := json.Marshal(map[string]any{"ok": true, "panel_version": "1.3.33", "applied": applied, "skipped": skipped})
		_, _ = w.Write(b)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func newChangePortServer1334(t *testing.T, remote *fakeChangePortRemote1334) *Server {
	t.Helper()
	s := newTestAddLandingServer(t, t.TempDir(), t.TempDir())
	tok := generateRemoteToken()
	s.cfg.Remotes = append(s.cfg.Remotes, RemotePanel{ID: "rm-1334", Name: "测试远程", BaseURL: remote.srv.URL, Token: tok})
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-test", Name: "测试一番", TrafficLimitBytes: 1073741824, TrafficLimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-test", SubKind: "merged", SubID: "ms-test", SubName: "测试一番", Panel: "rm-1334", LandingAddr: "203.0.113.1", ListenPort: 30002, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	return s
}

func callChangePort1334(t *testing.T, s *Server, kind, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/subscriptions/" + kind + "/" + id + "/change-port"
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	s.handleSubChangePort(kind)(rec, req)
	return rec
}

func decodeChangePort1334(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	return resp
}

// ---------- 自定义端口成功换口 ----------

func TestChangeUserForwardPortCustom1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	// 用量记录先记一笔：换口后必须原样保留（不清零）。
	rec := s.userTraffic.get("merged:ms-test")
	rec.UpTotal, rec.DownTotal, rec.UpPeriod, rec.DownPeriod = 111, 222, 33, 44

	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 200 || resp["ok"] != true {
		t.Fatalf("换口应成功：code=%d resp=%v", r.Code, resp)
	}
	if resp["old_port"] != float64(30002) || resp["new_port"] != float64(30005) {
		t.Fatalf("应 30002→30005，实际 resp=%v", resp)
	}
	if resp["remote_checked"] != true {
		t.Fatalf("远程问到了，remote_checked 应为 true，实际 resp=%v", resp)
	}
	// 本机记录已改。
	f := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.1")
	if f == nil || f.ListenPort != 30005 {
		t.Fatalf("本机记录端口应是 30005，实际 %+v", f)
	}
	// 远程收到下发，新端口在集合里。
	if len(remote.posts) != 1 {
		t.Fatalf("远程应收到 1 次下发，实际 %d 次", len(remote.posts))
	}
	found := false
	if fwds, ok := remote.posts[0]["forwards"].([]centralUserForward); ok {
		for _, cf := range fwds {
			if cf.ListenPort == 30005 && cf.LandingAddr == "203.0.113.1" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("下发集合里应有新端口 30005，实际 posts=%v", remote.posts)
	}
	// 用量记录原样保留。
	up, down, upP, downP, ok := s.userTraffic.totals("merged:ms-test")
	if !ok || up != 111 || down != 222 || upP != 33 || downP != 44 {
		t.Fatalf("用量记录应原样保留（111/222/33/44），实际 %d/%d/%d/%d ok=%v", up, down, upP, downP, ok)
	}
	// 旧端口 30002 已释放：再分配应分到 30002（最小的空闲端口）。
	port, err := s.allocateUserPortForPanel("rm-1334")
	if err != nil || port != 30002 {
		t.Fatalf("旧端口释放后应分到 30002，实际 port=%d err=%v", port, err)
	}
}

// ---------- 撞远程已占端口时拒绝，原转发不动 ----------

func TestChangeUserForwardPortCollisionRemote1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30001}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 400 || resp["ok"] != false {
		t.Fatalf("撞端口应 400 拒绝：code=%d resp=%v", r.Code, resp)
	}
	errMsg, _ := resp["error"].(string)
	if !strings.Contains(errMsg, "30001") || !strings.Contains(errMsg, "已被占用") || !strings.Contains(errMsg, "合并二") {
		t.Fatalf("错误应写清端口与是谁在用，实际 %q", errMsg)
	}
	f := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.1")
	if f == nil || f.ListenPort != 30002 {
		t.Fatalf("拒绝后原转发应不动（30002），实际 %+v", f)
	}
	if len(remote.posts) != 0 {
		t.Fatalf("拒绝后不应向远程下发，实际 posts=%v", remote.posts)
	}
}

// ---------- 超出端口段、与当前端口相同都拒绝 ----------

func TestChangeUserForwardPortOutOfRange1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	for _, body := range []string{
		`{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":25000}`,
		`{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":40000}`,
		`{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30002}`,
	} {
		r := callChangePort1334(t, s, "merged", "ms-test", body)
		resp := decodeChangePort1334(t, r)
		if r.Code != 400 || resp["ok"] != false {
			t.Fatalf("应 400 拒绝 %s：code=%d resp=%v", body, r.Code, resp)
		}
	}
	f := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.1")
	if f == nil || f.ListenPort != 30002 {
		t.Fatalf("拒绝后原转发应不动（30002），实际 %+v", f)
	}
	if len(remote.posts) != 0 {
		t.Fatalf("拒绝后不应向远程下发，实际 posts=%v", remote.posts)
	}
}

// ---------- 留空自动分配 ----------

func TestChangeUserForwardPortAuto1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":0}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 200 || resp["ok"] != true {
		t.Fatalf("自动分配应成功：code=%d resp=%v", r.Code, resp)
	}
	// 远程已占 30000/30001、旧端口 30002 不算：自动分到 30003。
	if resp["new_port"] != float64(30003) {
		t.Fatalf("自动应分到 30003，实际 resp=%v", resp)
	}
}

// ---------- 远程问不到时回退本地校验，不阻断 ----------

func TestChangeUserForwardPortRemoteDown1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	remote.get404 = true // 旧远程：没有只读接口
	s := newChangePortServer1334(t, remote)
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 200 || resp["ok"] != true {
		t.Fatalf("远程问不到时应回退本地校验、仍成功：code=%d resp=%v", r.Code, resp)
	}
	if resp["remote_checked"] != false {
		t.Fatalf("remote_checked 应为 false，实际 resp=%v", resp)
	}
	warn, _ := resp["warning"].(string)
	if !strings.Contains(warn, "未确认远程占用") {
		t.Fatalf("响应应带“未确认远程占用”提示，实际 resp=%v", resp)
	}
	if len(remote.posts) != 1 {
		t.Fatalf("下发仍应推到远程（POST 接口 1.3.25 就有），实际 posts=%d", len(remote.posts))
	}
}

// ---------- 远程下发跳过时回滚两边 ----------

func TestChangeUserForwardPortRemoteSkipRollback1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	remote.skipPorts[30005] = true // 模拟竞态：问到时空着，下发时已被别人占了
	s := newChangePortServer1334(t, remote)
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 400 || resp["ok"] != false {
		t.Fatalf("远程跳过时应 400 报错：code=%d resp=%v", r.Code, resp)
	}
	// 本机记录回滚到旧端口。
	f := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.1")
	if f == nil || f.ListenPort != 30002 {
		t.Fatalf("回滚后本机记录应是旧端口 30002，实际 %+v", f)
	}
	// 远程收到两次下发：第一次是新端口（被跳过），第二次是旧集合恢复。
	if len(remote.posts) != 2 {
		t.Fatalf("远程应收到 2 次下发（新端口 + 旧集合恢复），实际 %d 次：%v", len(remote.posts), remote.posts)
	}
	foundOld := false
	if fwds, ok := remote.posts[1]["forwards"].([]centralUserForward); ok {
		for _, cf := range fwds {
			if cf.ListenPort == 30002 && cf.LandingAddr == "203.0.113.1" {
				foundOld = true
			}
		}
	}
	if !foundOld {
		t.Fatalf("第二次下发应是旧集合（含 30002），实际 posts=%v", remote.posts)
	}
	// 远程存量里这条转发还在（没被全量替换丢掉）。
	stillThere := false
	for _, fw := range remote.forwards {
		if fw.Key == "merged:ms-test" && fw.ListenPort == 30002 {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatalf("远程存量里 ms-test 的 30002 转发应还在，实际 %+v", remote.forwards)
	}
}

// ---------- 多落地只换其中一条 ----------

func TestChangeUserForwardPortMultiLanding1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	// 远程再加一条 ms-test 在 203.0.113.2 上的转发。
	remote.forwards = append(remote.forwards, UserForward{Key: "merged:ms-test", SubKind: "merged", SubID: "ms-test", SubName: "测试一番", Panel: "local", LandingAddr: "203.0.113.2", ListenPort: 30003, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	s := newChangePortServer1334(t, remote)
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-test", SubKind: "merged", SubID: "ms-test", SubName: "测试一番", Panel: "rm-1334", LandingAddr: "203.0.113.2", ListenPort: 30003, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 200 || resp["ok"] != true {
		t.Fatalf("多落地换其中一条应成功：code=%d resp=%v", r.Code, resp)
	}
	f1 := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.1")
	f2 := s.findRemoteUserForward("merged:ms-test", "rm-1334", "203.0.113.2")
	if f1 == nil || f1.ListenPort != 30005 {
		t.Fatalf("203.0.113.1 应换成 30005，实际 %+v", f1)
	}
	if f2 == nil || f2.ListenPort != 30003 {
		t.Fatalf("203.0.113.2 应不动（30003），实际 %+v", f2)
	}
	// 下发集合里两条都在（新端口 + 不动的那条）。
	if len(remote.posts) != 1 {
		t.Fatalf("远程应收到 1 次下发，实际 %d 次", len(remote.posts))
	}
	ports := map[int]bool{}
	if fwds, ok := remote.posts[0]["forwards"].([]centralUserForward); ok {
		for _, cf := range fwds {
			ports[cf.ListenPort] = true
		}
	}
	if !ports[30005] || !ports[30003] {
		t.Fatalf("下发集合应含 30005 与 30003，实际 posts=%v", remote.posts)
	}
}

// ---------- 没有转发时拒绝 ----------

func TestChangeUserForwardPortNoForward1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	s.cfg.UserForwards = nil
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"rm-1334","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 400 || resp["ok"] != false {
		t.Fatalf("没有转发时应 400 拒绝：code=%d resp=%v", r.Code, resp)
	}
}

// ---------- 转发列表接口 ----------

func TestSubUserForwardsList1334(t *testing.T) {
	remote := newFakeChangePortRemote1334(t)
	s := newChangePortServer1334(t, remote)
	// 快照里给落地起个显示名（内存，不读文件）。
	s.centralMu.Lock()
	s.centralCache["rm-1334"] = &RemoteStatusSnapshot{ID: "rm-1334", Name: "测试远程", Landings: []remoteLandingSummary{{Index: 1, Name: "智利", Landing: "203.0.113.1"}}}
	s.centralMu.Unlock()
	req := httptest.NewRequest("GET", "/api/v1/subscriptions/merged/ms-test/forwards", nil)
	req.SetPathValue("id", "ms-test")
	rec := httptest.NewRecorder()
	s.handleSubUserForwards("merged")(rec, req)
	if rec.Code != 200 {
		t.Fatalf("列表应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool `json:"ok"`
		PortStart int  `json:"port_start"`
		PortEnd   int  `json:"port_end"`
		Forwards  []struct {
			PanelID     string `json:"panel_id"`
			PanelName   string `json:"panel_name"`
			LandingAddr string `json:"landing_addr"`
			LandingName string `json:"landing_name"`
			ListenPort  int    `json:"listen_port"`
		} `json:"forwards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if !resp.OK || len(resp.Forwards) != 1 {
		t.Fatalf("应返回 1 条转发，实际 %+v", resp)
	}
	fw := resp.Forwards[0]
	if fw.PanelName != "测试远程" || fw.LandingName != "智利" || fw.ListenPort != 30002 {
		t.Fatalf("转发信息不对，实际 %+v", fw)
	}
	if resp.PortStart != 30000 || resp.PortEnd != 39999 {
		t.Fatalf("端口段应是 30000–39999，实际 %d–%d", resp.PortStart, resp.PortEnd)
	}
	// 不存在的订阅 404。
	req2 := httptest.NewRequest("GET", "/api/v1/subscriptions/merged/ms-nope/forwards", nil)
	req2.SetPathValue("id", "ms-nope")
	rec2 := httptest.NewRecorder()
	s.handleSubUserForwards("merged")(rec2, req2)
	if rec2.Code != 404 {
		t.Fatalf("不存在的订阅应 404，实际 %d", rec2.Code)
	}
}

// ---------- 本机转发：下发失败时回滚 ----------

func TestChangeUserForwardPortLocalRollback1334(t *testing.T) {
	// 本机有 relay.conf + config.toml，但没有 realm 二进制：applyUserForwards
	// 写完 toml 后重启 realm 失败，换口应报错、本机记录回滚。
	s := newTestSubServer(t)
	s.cfg.MergedSubs = append(s.cfg.MergedSubs, MergedSub{ID: "ms-test", Name: "测试一番", TrafficLimitBytes: 1073741824, TrafficLimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	s.cfg.UserForwards = append(s.cfg.UserForwards, UserForward{Key: "merged:ms-test", SubKind: "merged", SubID: "ms-test", SubName: "测试一番", Panel: "local", LandingAddr: "203.0.113.1", ListenPort: 30002, LimitBytes: 1073741824, LimitMode: "total", ResetMode: "monthly", ResetDay: 1})
	r := callChangePort1334(t, s, "merged", "ms-test", `{"panel_id":"local","landing_addr":"203.0.113.1","new_port":30005}`)
	resp := decodeChangePort1334(t, r)
	if r.Code != 400 || resp["ok"] != false {
		t.Fatalf("本机重启失败时应 400 报错：code=%d resp=%v", r.Code, resp)
	}
	f := s.findUserForward("merged:ms-test", "203.0.113.1")
	if f == nil || f.ListenPort != 30002 {
		t.Fatalf("回滚后本机记录应是旧端口 30002，实际 %+v", f)
	}
}
