package main

// v1.3.48：Cloudflare 写 TXT 记录失败时，错误里带上响应体里的真正原因
//（以前只报 HTTP 状态码，用户真机上只看到“HTTP 400”，没法定位）。
// cfReq 是包级变量，测试里替身为桩。

import (
	"fmt"
	"strings"
	"testing"
)

// ---------- Cloudflare TXT 桩（v1.3.48 自动清理残留） ----------

type cfCall1348 struct{ method, url string }

// stubCF1348 桩 cfReq：zone 查询固定返回 zone-1；列表返回 listBody；
// DELETE 按 deleteCode/deleteErr 走；POST 建记录返回 new-id。返回调用记录。
func stubCF1348(t *testing.T, listBody string, deleteCode int, deleteErr error) *[]cfCall1348 {
	t.Helper()
	orig := cfReq
	t.Cleanup(func() { cfReq = orig })
	calls := &[]cfCall1348{}
	cfReq = func(token, method, url string, payload any) (int, []byte, error) {
		*calls = append(*calls, cfCall1348{method, url})
		switch {
		case method == "GET" && strings.Contains(url, "/zones?name="):
			return 200, []byte(`{"success":true,"result":[{"id":"zone-1"}]}`), nil
		case method == "GET" && strings.Contains(url, "/dns_records?"):
			return 200, []byte(listBody), nil
		case method == "DELETE":
			if deleteErr != nil {
				return 0, nil, deleteErr
			}
			return deleteCode, []byte(`{"success":true}`), nil
		case method == "POST":
			return 200, []byte(`{"success":true,"result":{"id":"new-id"}}`), nil
		default:
			return 404, nil, nil
		}
	}
	return calls
}

// TestCloudflareTXTDeletesStaleBeforeCreate1348：已有同名 TXT 残留时，
// 先删掉它再建新记录，返回新建记录的 id。
func TestCloudflareTXTDeletesStaleBeforeCreate1348(t *testing.T) {
	listBody := `{"success":true,"result":[{"id":"stale-1","type":"TXT","name":"_acme-challenge.example.com"}]}`
	calls := stubCF1348(t, listBody, 200, nil)
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	id, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err != nil {
		t.Fatalf("建记录应成功，实际：%v", err)
	}
	if id != "new-id" {
		t.Fatalf("应返回新建记录的 id，实际 %q", id)
	}
	delIdx, postIdx := -1, -1
	for i, c := range *calls {
		if c.method == "DELETE" && strings.Contains(c.url, "stale-1") {
			delIdx = i
		}
		if c.method == "POST" {
			postIdx = i
		}
	}
	if delIdx < 0 {
		t.Fatalf("应先删掉残留记录 stale-1，调用记录：%v", *calls)
	}
	if postIdx < 0 || delIdx > postIdx {
		t.Fatalf("应先删残留再建新记录，删=%d 建=%d，调用记录：%v", delIdx, postIdx, *calls)
	}
}

// TestCloudflareTXTDeleteFailureDoesNotBlockCreate1348：删残留失败（500）
// 也不阻塞建新记录。
func TestCloudflareTXTDeleteFailureDoesNotBlockCreate1348(t *testing.T) {
	listBody := `{"success":true,"result":[{"id":"stale-1","type":"TXT","name":"_acme-challenge.example.com"}]}`
	calls := stubCF1348(t, listBody, 500, nil)
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	id, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err != nil {
		t.Fatalf("删残留失败不应阻塞建新记录，实际：%v", err)
	}
	if id != "new-id" {
		t.Fatalf("应返回新建记录的 id，实际 %q", id)
	}
	postSeen := false
	for _, c := range *calls {
		if c.method == "POST" {
			postSeen = true
		}
	}
	if !postSeen {
		t.Fatalf("删失败后仍应建新记录，调用记录：%v", *calls)
	}
}

// TestCloudflareTXTDeleteTransportErrorDoesNotBlockCreate1348：删残留时
// 网络层报错，同样不阻塞建新记录。
func TestCloudflareTXTDeleteTransportErrorDoesNotBlockCreate1348(t *testing.T) {
	listBody := `{"success":true,"result":[{"id":"stale-1","type":"TXT","name":"_acme-challenge.example.com"}]}`
	stubCF1348(t, listBody, 200, fmt.Errorf("connection reset"))
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	id, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err != nil {
		t.Fatalf("删残留报错不应阻塞建新记录，实际：%v", err)
	}
	if id != "new-id" {
		t.Fatalf("应返回新建记录的 id，实际 %q", id)
	}
}

// TestCloudflareTXTLeavesOtherRecordsAlone1348：只删名字与类型都对得上的
// 同名 TXT 残留——别处域名的同名记录、www 的 TXT、同名 A 记录一律不动。
func TestCloudflareTXTLeavesOtherRecordsAlone1348(t *testing.T) {
	listBody := `{"success":true,"result":[
		{"id":"stale-1","type":"TXT","name":"_acme-challenge.example.com"},
		{"id":"other-1","type":"TXT","name":"_acme-challenge.other.com"},
		{"id":"www-1","type":"TXT","name":"www.example.com"},
		{"id":"a-1","type":"A","name":"_acme-challenge.example.com"}]}`
	calls := stubCF1348(t, listBody, 200, nil)
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	if _, err := s.cloudflareTXT("example.com", "txt-value", ""); err != nil {
		t.Fatalf("建记录应成功，实际：%v", err)
	}
	var deleted []string
	for _, c := range *calls {
		if c.method == "DELETE" {
			deleted = append(deleted, c.url)
		}
	}
	if len(deleted) != 1 || !strings.Contains(deleted[0], "stale-1") {
		t.Fatalf("只应删同名 TXT 残留 stale-1，实际删了：%v", deleted)
	}
}

// TestCloudflareTXTCreateFailureHasManualHint1348：TXT 创建失败时，错误里
// 既有 Cloudflare 响应体摘要，也有带本次域名的手动清理提醒（会同时进
// 前端 toast 与 HTTPS 卡片上常驻的失败原因）。
func TestCloudflareTXTCreateFailureHasManualHint1348(t *testing.T) {
	orig := cfReq
	t.Cleanup(func() { cfReq = orig })
	cfReq = func(token, method, url string, payload any) (int, []byte, error) {
		if method == "GET" && strings.Contains(url, "/zones?name=") {
			return 200, []byte(`{"success":true,"result":[{"id":"zone-1"}]}`), nil
		}
		if method == "GET" && strings.Contains(url, "/dns_records?") {
			return 200, []byte(`{"success":true,"result":[]}`), nil
		}
		return 400, []byte(`{"success":false,"errors":[{"code":81057,"message":"Record already exists"}]}`), nil
	}
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	_, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err == nil {
		t.Fatalf("建 TXT 失败应返回错误")
	}
	if !strings.Contains(err.Error(), "Record already exists") {
		t.Fatalf("错误应带上 Cloudflare 响应体摘要，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "_acme-challenge.example.com") {
		t.Fatalf("错误应带上本次域名的手动清理提醒，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "重试签发") {
		t.Fatalf("错误应提示删残留记录后再重试签发，实际：%v", err)
	}
}

// TestCloudflareTXTCreateFailureIncludesBody1348：POST 建 TXT 返回 400 时，
// 错误里既有 HTTP 状态码，也有 Cloudflare 响应体里的真正原因。
func TestCloudflareTXTCreateFailureIncludesBody1348(t *testing.T) {
	orig := cfReq
	t.Cleanup(func() { cfReq = orig })
	cfReq = func(token, method, url string, payload any) (int, []byte, error) {
		if method == "GET" {
			return 200, []byte(`{"success":true,"result":[{"id":"zone-1"}]}`), nil
		}
		return 400, []byte(`{"success":false,"errors":[{"code":9109,"message":"Invalid TTL. TTL must be between 60 and 86400"}]}`), nil
	}
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	_, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err == nil {
		t.Fatalf("建 TXT 失败应返回错误")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("错误应保留 HTTP 状态码，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "Invalid TTL") {
		t.Fatalf("错误应带上 Cloudflare 响应体里的真正原因，实际：%v", err)
	}
}

// TestCloudflareTXTCreateFailureTruncatesBody1348：响应体超长时截断到 500 字符，
// 不把 PanelCertLastErr 和前端 toast 撑爆。
func TestCloudflareTXTCreateFailureTruncatesBody1348(t *testing.T) {
	orig := cfReq
	t.Cleanup(func() { cfReq = orig })
	cfReq = func(token, method, url string, payload any) (int, []byte, error) {
		if method == "GET" {
			return 200, []byte(`{"success":true,"result":[{"id":"zone-1"}]}`), nil
		}
		return 400, []byte(strings.Repeat("x", 1200)), nil
	}
	s := newTestServer144(t)
	s.cfg.CloudflareToken = "test-token"
	_, err := s.cloudflareTXT("example.com", "txt-value", "")
	if err == nil {
		t.Fatalf("建 TXT 失败应返回错误")
	}
	if !strings.Contains(err.Error(), strings.Repeat("x", 500)) {
		t.Fatalf("响应体应保留前 500 字符，实际：%v", err)
	}
	if strings.Contains(err.Error(), strings.Repeat("x", 501)) {
		t.Fatalf("响应体不应保留超过 500 个连续字符，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "…") {
		t.Fatalf("截断处应有省略号，实际：%v", err)
	}
}
