package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---------- v1.3.5 ----------

// TestWorkerOriginRemember135 Worker 源站地址填一次就记住：点“生成 Worker
// 代码”时输入框非空就存下来（config.json 的 worker_origin），设置接口下发
// 回填；留空生成时用默认地址，同时把之前存过的清空；明确填 IP 时 400，
// 存过的值不动。
func TestWorkerOriginRemember135(t *testing.T) {
	s := newTestSubServer(t)
	gen := func(origin string) (int, map[string]any) {
		t.Helper()
		target := "/api/v1/subscriptions/worker"
		if origin != "" {
			target += "?origin=" + url.QueryEscape(origin)
		}
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "203.0.113.1:11111"
		rec := httptest.NewRecorder()
		s.handleWorkerCode(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	settingsWorkerOrigin := func() string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/settings", nil)
		rec := httptest.NewRecorder()
		s.handleSettings(rec, req)
		if rec.Code != 200 {
			t.Fatalf("设置接口应 200，实际 %d", rec.Code)
		}
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		sub, _ := m["subscription"].(map[string]any)
		wo, _ := sub["worker_origin"].(string)
		return wo
	}

	// 初始没存过：设置里 worker_origin 为空
	if got := settingsWorkerOrigin(); got != "" {
		t.Fatalf("初始 worker_origin 应为空，实际 %q", got)
	}
	// 填域名生成：200，代码里 ORIGIN 是填的地址，值存下来了
	code, m := gen("http://panel.example.com:11111")
	if code != 200 {
		t.Fatalf("填域名生成应 200，实际 %d：%v", code, m)
	}
	if c, _ := m["code"].(string); !strings.Contains(c, `const ORIGIN = "http://panel.example.com:11111"`) {
		t.Fatalf("生成的代码 ORIGIN 不对：%v", m)
	}
	if s.cfg.WorkerOrigin != "http://panel.example.com:11111" {
		t.Fatalf("应记住填的值，实际 cfg.WorkerOrigin=%q", s.cfg.WorkerOrigin)
	}
	if got := settingsWorkerOrigin(); got != "http://panel.example.com:11111" {
		t.Fatalf("设置接口应下发存过的值，实际 %q", got)
	}
	// 裸域名也照原文存
	code, _ = gen("panel.example.com:22222")
	if code != 200 || s.cfg.WorkerOrigin != "panel.example.com:22222" {
		t.Fatalf("裸域名应照原文记住，实际 code=%d cfg.WorkerOrigin=%q", code, s.cfg.WorkerOrigin)
	}
	// 留空生成：用默认地址（当前面板地址），同时把存过的清空
	code, m = gen("")
	if code != 200 {
		t.Fatalf("留空生成应 200，实际 %d：%v", code, m)
	}
	if o, _ := m["origin"].(string); o != "http://203.0.113.1:11111" {
		t.Fatalf("留空应默认用当前面板地址，实际 origin=%q", o)
	}
	if s.cfg.WorkerOrigin != "" || settingsWorkerOrigin() != "" {
		t.Fatalf("留空生成后存过的值应清空，实际 cfg=%q 设置=%q", s.cfg.WorkerOrigin, settingsWorkerOrigin())
	}
	// 明确填 IP：400，存过的值不动（先存一个，再试 IP）
	_, _ = gen("http://panel.example.com:11111")
	if s.cfg.WorkerOrigin != "http://panel.example.com:11111" {
		t.Fatalf("预存失败，实际 cfg.WorkerOrigin=%q", s.cfg.WorkerOrigin)
	}
	code, _ = gen("http://203.0.113.1:11111")
	if code != 400 {
		t.Fatalf("明确填 IP 应 400，实际 %d", code)
	}
	if s.cfg.WorkerOrigin != "http://panel.example.com:11111" {
		t.Fatalf("填 IP 被拒后存过的值不应动，实际 %q", s.cfg.WorkerOrigin)
	}
}

// TestWorkerOriginPrefillDefault135 没存过时设置接口下发空字符串，
// 前端回填为空、placeholder 仍提示留空等于当前地址（jsdom 侧验证）。
func TestWorkerOriginPrefillDefault135(t *testing.T) {
	s := newTestSubServer(t)
	req := httptest.NewRequest("GET", "/api/v1/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("设置接口应 200，实际 %d", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	sub, _ := m["subscription"].(map[string]any)
	if _, has := sub["worker_origin"]; !has {
		t.Fatalf("设置接口 subscription 里应有 worker_origin 字段，实际：%v", sub)
	}
	if wo, _ := sub["worker_origin"].(string); wo != "" {
		t.Fatalf("没存过时 worker_origin 应为空，实际 %q", wo)
	}
}
