package main

// v1.3.49：ACME DNS-01 多服务商——
// ① 服务商抽象与注册、凭证配齐判断、切服务商不影响旧配置；
// ② 阿里云（HMAC-SHA1 签名验算、建/删/查/清理/报错透出）、DNSPod
// （表单与状态码校验）、Route 53（SigV4 头、UPSERT/DELETE 多值保留）、
// GoDaddy v3（PAT 头、按 recordId 删）、Namecheap（整组回写合并、
// 白名单报错）；③ 手动模式两步走（下单存 pending、重启后读回继续、
// 过期作废、错域名拒绝、取消）；④ 版本断言同步到 1.3.49。
//
// 各服务商的 HTTP 请求都走 dnsHTTPDo（包级变量），测试里替身为桩；
// Cloudflare 仍走 cfReq/cfTXTFunc（1.3.48 的测试覆盖，本文件不重复测）。

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 脚手架 ----------

// stubDNSHTTP1349 把 dnsHTTPDo 替身成桩（按请求返回固定响应），测试结束恢复。
func stubDNSHTTP1349(t *testing.T, fn func(req *http.Request) (int, []byte, error)) {
	t.Helper()
	old := dnsHTTPDo
	dnsHTTPDo = fn
	t.Cleanup(func() { dnsHTTPDo = old })
}

// newTestServer1349 在指定 PanelHome 上建测试服务器（模拟重启时复用同一目录）。
func newTestServer1349(t *testing.T, home string) *Server {
	t.Helper()
	t.Setenv("PANEL_INITIAL_PASSWORD", "test-password-123")
	s, err := newServer(Env{PanelHome: home, RealmDir: t.TempDir(), KitDir: t.TempDir(), Port: 11111})
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	return s
}

// stubACMESleep1349 把 ACME 里的等待全部跳过（DNS 传播 10 秒与轮询间隔）。
func stubACMESleep1349(t *testing.T) {
	t.Helper()
	old := acmeSleep
	acmeSleep = func(time.Duration) {}
	t.Cleanup(func() { acmeSleep = old })
}

// ---------- 1. 服务商注册与凭证配齐 ----------

func TestDNSProviderRegistry1349(t *testing.T) {
	// 七个服务商 id 都能解析出来，显示名非空。
	for _, id := range []string{"cloudflare", "aliyun", "dnspod", "route53", "godaddy", "namecheap", "manual"} {
		p := dnsProviderFor(id)
		if p.ID() != id {
			t.Fatalf("dnsProviderFor(%q) 返回的 ID 是 %q", id, p.ID())
		}
		if dnsProviderName(id) == "" {
			t.Fatalf("dnsProviderName(%q) 为空", id)
		}
	}
	// 未知 id 回落到 Cloudflare（老配置兼容）。
	if dnsProviderFor("bogus").ID() != "cloudflare" {
		t.Fatal("未知服务商应回落到 Cloudflare")
	}
	if !validDNSProvider("manual") || validDNSProvider("bogus") {
		t.Fatal("validDNSProvider 判断不对")
	}
}

func TestDNSProviderConfigured1349(t *testing.T) {
	s := newTestServer144(t)
	// 默认都没配（manual 不需要凭证，恒 true）。
	if s.dnsProviderConfigured("cloudflare") || s.dnsProviderConfigured("aliyun") ||
		s.dnsProviderConfigured("dnspod") || s.dnsProviderConfigured("route53") ||
		s.dnsProviderConfigured("godaddy") || s.dnsProviderConfigured("namecheap") {
		t.Fatal("默认应都没配齐")
	}
	if !s.dnsProviderConfigured("manual") {
		t.Fatal("手动模式不需要凭证，应恒配齐")
	}
	if s.dnsProviderID() != "cloudflare" {
		t.Fatalf("默认服务商应是 cloudflare，实际 %q", s.dnsProviderID())
	}
	// 配齐一家只影响这一家。
	s.cfg.AliyunAccessKeyID = "id-1"
	if s.dnsProviderConfigured("aliyun") {
		t.Fatal("只填 ID 没填 Secret 应算没配齐")
	}
	s.cfg.AliyunAccessKeySecret = "secret-1"
	if !s.dnsProviderConfigured("aliyun") {
		t.Fatal("ID 与 Secret 都填了应算配齐")
	}
	// 切服务商不影响旧 CF 配置（任务明确要求）。
	s.cfg.CloudflareToken = "cfut_oldtoken1349"
	s.cfg.DNSProvider = "aliyun"
	if s.cfg.CloudflareToken != "cfut_oldtoken1349" {
		t.Fatal("切到阿里云后旧 CF Token 不应被动")
	}
	if !s.dnsProviderConfigured("cloudflare") {
		t.Fatal("切走后 Cloudflare 仍应算配齐（Token 还在）")
	}
	if s.dnsProviderID() != "aliyun" {
		t.Fatalf("dnsProviderID 应为 aliyun，实际 %q", s.dnsProviderID())
	}
	// 未知值回落 cloudflare。
	s.cfg.DNSProvider = "bogus"
	if s.dnsProviderID() != "cloudflare" {
		t.Fatal("配了未知服务商应回落 cloudflare")
	}
}

// ---------- 2. 阿里云：签名验算 ----------

func TestAliyunSign1349(t *testing.T) {
	// 签名构造独立验算：按阿里云文档的算法（排序→RFC3986 编码→
	// StringToSign→HMAC-SHA1→base64）重算一遍，必须与 aliyunSign 一致。
	params := map[string]string{
		"AccessKeyId": "test-id", "Action": "AddDomainRecord",
		"DomainName": "example.com", "RR": "_acme-challenge.ccpanel",
		"Type": "TXT", "Value": "txt-value", "TTL": "600",
		"Format": "JSON", "SignatureMethod": "HMAC-SHA1",
		"SignatureNonce": "nonce-1", "SignatureVersion": "1.0",
		"Timestamp": "2026-11-23T10:00:00Z", "Version": "2015-01-09",
	}
	got := aliyunSign("test-secret", params)
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, aliyunEncode(k)+"="+aliyunEncode(params[k]))
	}
	stringToSign := "GET&" + aliyunEncode("/") + "&" + aliyunEncode(strings.Join(parts, "&"))
	mac := hmac.New(sha1.New, []byte("test-secret&"))
	mac.Write([]byte(stringToSign))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("签名不对：got %s want %s", got, want)
	}
	// 编码边角：空格 → %20，~ 不编码，* → %2A。
	if aliyunEncode("a b~c*d") != "a%20b~c%2Ad" {
		t.Fatalf("aliyunEncode 边角不对：%s", aliyunEncode("a b~c*d"))
	}
}

// ---------- 3. 阿里云：建/删/查/清理/报错 ----------

func TestAliyunCreateDelete1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.AliyunAccessKeyID = "test-id"
	s.cfg.AliyunAccessKeySecret = "test-secret"
	var mu sync.Mutex
	var calls []string
	var capturedAdd url.Values
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		q := req.URL.Query()
		action := q.Get("Action")
		mu.Lock()
		calls = append(calls, action)
		mu.Unlock()
		switch action {
		case "DescribeDomains":
			return 200, []byte(`{"Domains":{"Domain":[{"DomainName":"example.com"}]}}`), nil
		case "DescribeDomainRecords":
			return 200, []byte(`{"DomainRecords":{"Record":[{"RecordId":"old-1","RR":"_acme-challenge.ccpanel","Type":"TXT","Value":"old-val"}]}}`), nil
		case "DeleteDomainRecord":
			return 200, []byte(`{"RequestId":"r1"}`), nil
		case "AddDomainRecord":
			mu.Lock()
			capturedAdd = q
			mu.Unlock()
			// 签名参数必须齐全。
			for _, k := range []string{"AccessKeyId", "SignatureMethod", "SignatureVersion", "SignatureNonce", "Timestamp", "Signature"} {
				if q.Get(k) == "" {
					t.Errorf("AddDomainRecord 缺签名参数 %s", k)
				}
			}
			if q.Get("SignatureMethod") != "HMAC-SHA1" || q.Get("SignatureVersion") != "1.0" || q.Get("Version") != "2015-01-09" {
				t.Errorf("签名公共参数不对：%v", q)
			}
			return 200, []byte(`{"RecordId":"new-1","RequestId":"r2"}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := aliyunProvider{}
	// ListStale 查到 1 条残留。
	stale, err := p.ListStale(s, "ccpanel.example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "old-1" || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-1，实际 %+v", stale)
	}
	// Create：先清残留（删 old-1）再建，返回新 RecordId。
	id, err := p.Create(s, "ccpanel.example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "new-1" {
		t.Fatalf("Create 应返回 new-1，实际 %q", id)
	}
	if capturedAdd.Get("RR") != "_acme-challenge.ccpanel" || capturedAdd.Get("Type") != "TXT" ||
		capturedAdd.Get("Value") != "new-val" || capturedAdd.Get("TTL") != "600" ||
		capturedAdd.Get("DomainName") != "example.com" {
		t.Fatalf("AddDomainRecord 参数不对：%v", capturedAdd)
	}
	// 顺序：ListStale（查 zone + 查残留）→ Create（查 zone + ListStale
	// 再查 zone 与残留 + 删残留 + 建记录）。
	// 注意：锁只包住读 calls 的那一下（不能 defer 到函数尾——后面还要
	// 调 p.Delete 进桩拿同一把锁，defer 会死锁）。
	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	mu.Unlock()
	want := []string{"DescribeDomains", "DescribeDomainRecords", "DescribeDomains", "DescribeDomains", "DescribeDomainRecords", "DeleteDomainRecord", "AddDomainRecord"}
	if len(gotCalls) != len(want) {
		t.Fatalf("调用顺序不对：%v want %v", gotCalls, want)
	}
	for i := range want {
		if gotCalls[i] != want[i] {
			t.Fatalf("调用顺序不对：%v want %v", gotCalls, want)
		}
	}
	// Delete：按 RecordId 删。
	calls = nil
	if err := p.Delete(s, "ccpanel.example.com", "new-1", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	if len(calls) != 1 || calls[0] != "DeleteDomainRecord" {
		t.Fatalf("Delete 应只调一次 DeleteDomainRecord，实际 %v", calls)
	}
}

func TestAliyunCreateError1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.AliyunAccessKeyID = "bad-id"
	s.cfg.AliyunAccessKeySecret = "bad-secret"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		q := req.URL.Query()
		switch q.Get("Action") {
		case "DescribeDomains":
			return 200, []byte(`{"Domains":{"Domain":[{"DomainName":"example.com"}]}}`), nil
		case "DescribeDomainRecords":
			return 200, []byte(`{"DomainRecords":{"Record":[]}}`), nil
		case "AddDomainRecord":
			return 200, []byte(`{"Code":"InvalidAccessKeyId.NotFound","Message":"The AccessKeyId is not found","RequestId":"r9"}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	_, err := (aliyunProvider{}).Create(s, "ccpanel.example.com", "v")
	if err == nil {
		t.Fatal("AddDomainRecord 报错时 Create 应返回错误")
	}
	// 报错透出：对方的 Code 与 Message 都要在，另带手动清理提醒。
	if !strings.Contains(err.Error(), "InvalidAccessKeyId.NotFound") || !strings.Contains(err.Error(), "AccessKeyId is not found") {
		t.Fatalf("错误应透出对方的 Code 与 Message，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "阿里云 DNS 删除残留") {
		t.Fatalf("错误应带手动清理提醒，实际：%v", err)
	}
}

// ---------- 4. DNSPod：表单与状态码 ----------

func TestDNSPodCreateDelete1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DNSPodTokenID = "12345"
	s.cfg.DNSPodToken = "token-abc"
	var mu sync.Mutex
	var calls []string
	var capturedCreate url.Values
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		// 公共参数：login_token 与 format=json 每次都要带。
		if form.Get("login_token") != "12345,token-abc" || form.Get("format") != "json" {
			t.Errorf("DNSPod 公共参数不对：%v", form)
		}
		action := strings.TrimPrefix(req.URL.Path, "/")
		mu.Lock()
		calls = append(calls, action)
		mu.Unlock()
		switch action {
		case "Domain.Info":
			// 真实 DNSPod：zone 不存在时报错，探测才能继续往短后缀试；
			// 桩也按这个语义来（只有 example.com 才有）。
			if form.Get("domain") == "example.com" {
				return 200, []byte(`{"status":{"code":"1","message":"ok"},"domain":{"id":"99"}}`), nil
			}
			return 200, []byte(`{"status":{"code":"0","message":"域名不存在"}}`), nil
		case "Record.List":
			if form.Get("domain_id") != "99" || form.Get("sub_domain") != "_acme-challenge.ccpanel" || form.Get("record_type") != "TXT" {
				t.Errorf("Record.List 参数不对：%v", form)
			}
			return 200, []byte(`{"status":{"code":"1"},"records":[{"id":"9","name":"_acme-challenge.ccpanel","type":"TXT","value":"old"}]}`), nil
		case "Record.Remove":
			return 200, []byte(`{"status":{"code":"1"}}`), nil
		case "Record.Create":
			mu.Lock()
			capturedCreate = form
			mu.Unlock()
			return 200, []byte(`{"status":{"code":"1"},"record":{"id":"10"}}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := dnspodProvider{}
	stale, err := p.ListStale(s, "ccpanel.example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "9" {
		t.Fatalf("ListStale 应返回 1 条残留 id=9，实际 %+v", stale)
	}
	id, err := p.Create(s, "ccpanel.example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "10" {
		t.Fatalf("Create 应返回 10，实际 %q", id)
	}
	if capturedCreate.Get("sub_domain") != "_acme-challenge.ccpanel" || capturedCreate.Get("record_type") != "TXT" ||
		capturedCreate.Get("value") != "new-val" || capturedCreate.Get("record_line") != "默认" || capturedCreate.Get("ttl") != "600" {
		t.Fatalf("Record.Create 参数不对：%v", capturedCreate)
	}
	if err := p.Delete(s, "ccpanel.example.com", "10", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// ListStale（Domain.Info 失败一次再成功 + Record.List）→ Create
	// （Domain.Info×2 + ListStale 再调 Domain.Info×2 与 Record.List +
	// Record.Remove + Record.Create）→ Delete（Domain.Info×2 + Record.Remove）。
	want := []string{
		"Domain.Info", "Domain.Info", "Record.List",
		"Domain.Info", "Domain.Info",
		"Domain.Info", "Domain.Info", "Record.List",
		"Record.Remove", "Record.Create",
		"Domain.Info", "Domain.Info", "Record.Remove",
	}
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：%v want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：%v want %v", calls, want)
		}
	}
}

func TestDNSPodError1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DNSPodTokenID = "1"
	s.cfg.DNSPodToken = "bad"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		action := strings.TrimPrefix(req.URL.Path, "/")
		switch action {
		case "Domain.Info":
			return 200, []byte(`{"status":{"code":"1"},"domain":{"id":"99"}}`), nil
		case "Record.List":
			return 200, []byte(`{"status":{"code":"1"},"records":[]}`), nil
		case "Record.Create":
			return 200, []byte(`{"status":{"code":"0","message":"Token 错误，请检查"}}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	_, err := (dnspodProvider{}).Create(s, "ccpanel.example.com", "v")
	if err == nil {
		t.Fatal("status.code != 1 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "Token 错误") {
		t.Fatalf("错误应透出 status.message，实际：%v", err)
	}
}

// ---------- 5. Route 53：SigV4 头与 UPSERT/DELETE ----------

func TestRoute53Sign1349(t *testing.T) {
	// SigV4 签名：同一请求＋同一时间＋同一 body → 签名相同（确定性）；
	// body 一变签名就变（签名绑定了 payload 哈希）；头格式符合规范。
	sign := func(body string) string {
		req, _ := http.NewRequest("POST", "https://route53.amazonaws.com/2013-04-01/hostedzone/Z123/rrset/", strings.NewReader(body))
		route53Sign(req, []byte(body), "AKIDTEST", "secret-test", time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC))
		return req.Header.Get("Authorization")
	}
	a1 := sign("<Change/>")
	a2 := sign("<Change/>")
	a3 := sign("<Change2/>")
	if a1 != a2 {
		t.Fatal("同一请求签名应相同")
	}
	if a1 == a3 {
		t.Fatal("body 变了签名应跟着变")
	}
	if !strings.HasPrefix(a1, "AWS4-HMAC-SHA256 Credential=AKIDTEST/20261123/us-east-1/route53/aws4_request, SignedHeaders=host;x-amz-date, Signature=") {
		t.Fatalf("Authorization 头格式不对：%s", a1)
	}
	sig := strings.Split(a1, "Signature=")[1]
	if len(sig) != 64 {
		t.Fatalf("签名应为 64 位十六进制，实际 %q", sig)
	}
}

func TestRoute53CreateDelete1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.Route53AccessKeyID = "AKIDTEST"
	s.cfg.Route53SecretAccessKey = "secret-test"
	var mu sync.Mutex
	var changeBodies []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		// 每个请求都必须带 SigV4 头。
		auth := req.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDTEST/") || !strings.Contains(auth, "/us-east-1/route53/aws4_request") {
			t.Errorf("Route53 请求缺 SigV4 头：%s", auth)
		}
		if req.Header.Get("X-Amz-Date") == "" {
			t.Errorf("Route53 请求缺 X-Amz-Date 头")
		}
		switch {
		case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/hostedzonesbyname"):
			return 200, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListHostedZonesByNameResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><HostedZones><HostedZone><Id>/hostedzone/Z123</Id><Name>example.com.</Name><Config><PrivateZone>false</PrivateZone></Config></HostedZone></HostedZones><IsTruncated>false</IsTruncated><MaxItems>1</MaxItems></ListHostedZonesByNameResponse>`), nil
		case req.Method == "GET" && strings.HasSuffix(req.URL.Path, "/rrset"):
			return 200, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListResourceRecordSetsResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><ResourceRecordSets><ResourceRecordSet><Name>_acme-challenge.ccpanel.example.com.</Name><Type>TXT</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>"our-val"</Value></ResourceRecord><ResourceRecord><Value>"other-val"</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></ResourceRecordSets><IsTruncated>false</IsTruncated><MaxItems>100</MaxItems></ListResourceRecordSetsResponse>`), nil
		case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/rrset/"):
			body, _ := io.ReadAll(req.Body)
			mu.Lock()
			changeBodies = append(changeBodies, string(body))
			mu.Unlock()
			return 200, []byte(`<?xml version="1.0" encoding="UTF-8"?><ChangeResourceRecordSetsResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><ChangeInfo><Id>/change/C1</Id><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := route53Provider{}
	// ListStale 读到两条值（本条 + 别的值）。
	stale, err := p.ListStale(s, "ccpanel.example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("ListStale 应返回 2 条值，实际 %+v", stale)
	}
	// Create：UPSERT 只带新值，TXT 值必须加双引号（Go 的 encoding/xml 会把
	// 引号写成 &#34;，AWS 解析后一样是引号，断言前先还原）。
	changeBodies = nil
	if _, err := p.Create(s, "ccpanel.example.com", "new-val"); err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if len(changeBodies) != 1 {
		t.Fatalf("Create 应调 1 次 ChangeResourceRecordSets，实际 %d 次", len(changeBodies))
	}
	upsertBody := strings.ReplaceAll(changeBodies[0], "&#34;", `"`)
	if !strings.Contains(upsertBody, "<Action>UPSERT</Action>") || !strings.Contains(upsertBody, `<Value>"new-val"</Value>`) {
		t.Fatalf("UPSERT 体不对：%s", changeBodies[0])
	}
	// Delete：先整组 DELETE（带现有全部值），再把剩余值 UPSERT 回去。
	changeBodies = nil
	if err := p.Delete(s, "ccpanel.example.com", "", "our-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	if len(changeBodies) != 2 {
		t.Fatalf("Delete 应调 2 次 ChangeResourceRecordSets（DELETE + UPSERT 回剩余值），实际 %d 次", len(changeBodies))
	}
	delBody := strings.ReplaceAll(changeBodies[0], "&#34;", `"`)
	upBody := strings.ReplaceAll(changeBodies[1], "&#34;", `"`)
	if !strings.Contains(delBody, "<Action>DELETE</Action>") ||
		!strings.Contains(delBody, `<Value>"our-val"</Value>`) ||
		!strings.Contains(delBody, `<Value>"other-val"</Value>`) {
		t.Fatalf("DELETE 体必须带现有全部值：%s", changeBodies[0])
	}
	if !strings.Contains(upBody, "<Action>UPSERT</Action>") ||
		!strings.Contains(upBody, `<Value>"other-val"</Value>`) ||
		strings.Contains(upBody, `"our-val"`) {
		t.Fatalf("回写 UPSERT 应只剩 other-val：%s", changeBodies[1])
	}
}

func TestRoute53Error1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.Route53AccessKeyID = "AKIDTEST"
	s.cfg.Route53SecretAccessKey = "secret-test"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if strings.HasSuffix(req.URL.Path, "/hostedzonesbyname") {
			return 200, []byte(`<?xml version="1.0" encoding="UTF-8"?><ListHostedZonesByNameResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><HostedZones><HostedZone><Id>/hostedzone/Z123</Id><Name>example.com.</Name><Config><PrivateZone>false</PrivateZone></Config></HostedZone></HostedZones></ListHostedZonesByNameResponse>`), nil
		}
		return 400, []byte(`<?xml version="1.0" encoding="UTF-8"?><ErrorResponse xmlns="https://route53.amazonaws.com/doc/2013-04-01/"><Error><Type>Sender</Type><Code>InvalidChangeBatch</Code><Message>Invalid Resource Record Set</Message></Error><RequestId>r1</RequestId></ErrorResponse>`), nil
	})
	_, err := (route53Provider{}).Create(s, "ccpanel.example.com", "v")
	if err == nil {
		t.Fatal("ChangeResourceRecordSets 报错时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "InvalidChangeBatch") || !strings.Contains(err.Error(), "Invalid Resource Record Set") {
		t.Fatalf("错误应透出对方的 Code 与 Message，实际：%v", err)
	}
}

// ---------- 6. GoDaddy v3：PAT 头与按 recordId 删 ----------

func TestGoDaddyCreateDelete1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.GoDaddyPAT = "pat-test-123"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer pat-test-123" {
			t.Errorf("GoDaddy 必须用 Bearer PAT，实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		zone := strings.TrimPrefix(req.URL.Path, "/v3/domains/zones/")
		zone = strings.Split(zone, "/")[0]
		switch {
		case req.Method == "GET" && zone == "ccpanel.example.com":
			return 404, []byte(`{"code":"NOT_FOUND","message":"Zone not found"}`), nil
		case req.Method == "GET":
			return 200, []byte(`{"items":[{"recordId":5,"name":"_acme-challenge.ccpanel","type":"TXT","data":"old-val","ttl":600}]}`), nil
		case req.Method == "POST":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["name"] != "_acme-challenge.ccpanel" || payload["type"] != "TXT" || payload["data"] != "new-val" || payload["ttl"] != float64(600) {
				t.Errorf("GoDaddy 建记录参数不对：%s", string(body))
			}
			return 201, []byte(`{"recordId":7,"name":"_acme-challenge.ccpanel","type":"TXT","data":"new-val","ttl":600}`), nil
		case req.Method == "DELETE":
			return 204, []byte(``), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := godaddyProvider{}
	stale, err := p.ListStale(s, "ccpanel.example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "5" || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 recordId=5，实际 %+v", stale)
	}
	id, err := p.Create(s, "ccpanel.example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "7" {
		t.Fatalf("Create 应返回 recordId 7，实际 %q", id)
	}
	if err := p.Delete(s, "ccpanel.example.com", "7", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// zone 探测：ccpanel.example.com 404 后试 example.com；ListStale 是
	// 探测（2 次 GET）+ 再列表（1 次 GET）；Create 先 ListStale 再删残留
	// （DELETE /5）再建（POST）；Delete 探测后 DELETE /7。
	want := []string{
		"GET /v3/domains/zones/ccpanel.example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"GET /v3/domains/zones/ccpanel.example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"GET /v3/domains/zones/ccpanel.example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"DELETE /v3/domains/zones/example.com/dns-records/5",
		"POST /v3/domains/zones/example.com/dns-records",
		"GET /v3/domains/zones/ccpanel.example.com/dns-records",
		"GET /v3/domains/zones/example.com/dns-records",
		"DELETE /v3/domains/zones/example.com/dns-records/7",
	}
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestGoDaddyError1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.GoDaddyPAT = "pat-test-123"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Method == "GET" {
			return 200, []byte(`{"items":[]}`), nil
		}
		return 400, []byte(`{"code":"INVALID_BODY","message":"TTL must be between 600 and 86400"}`), nil
	})
	_, err := (godaddyProvider{}).Create(s, "ccpanel.example.com", "v")
	if err == nil {
		t.Fatal("POST 报错时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "TTL must be between 600 and 86400") {
		t.Fatalf("错误应透出对方的 message，实际：%v", err)
	}
}

// ---------- 7. Namecheap：整组回写合并与白名单报错 ----------

const namecheapHostsOK1349 = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <CommandResponse Type="namecheap.domains.dns.getHosts">
    <DomainDNSGetHostsResult Domain="example.com" IsUsingOurDNS="true">
      <host HostId="1" Name="www" Type="A" Address="203.0.113.1" MXPref="10" TTL="120" />
      <host HostId="2" Name="_acme-challenge.ccpanel" Type="TXT" Address="old-val" MXPref="10" TTL="120" />
    </DomainDNSGetHostsResult>
  </CommandResponse>
</ApiResponse>`

func TestNamecheapCreateDelete1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.NamecheapAPIUser = "apiuser"
	s.cfg.NamecheapAPIKey = "apikey"
	s.cfg.NamecheapClientIP = "203.0.113.9"
	var mu sync.Mutex
	var setHostsCalls []url.Values
	// 有状态的桩：getHosts 返回当前的 TXT 值，setHosts 回写后更新它
	// （否则 Delete 按值匹配不到 Create 建的新值）。
	currentTXT := "old-val"
	hostsXML := func() []byte {
		return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <CommandResponse Type="namecheap.domains.dns.getHosts">
    <DomainDNSGetHostsResult Domain="example.com" IsUsingOurDNS="true">
      <host HostId="1" Name="www" Type="A" Address="203.0.113.1" MXPref="10" TTL="120" />
      <host HostId="2" Name="_acme-challenge.ccpanel" Type="TXT" Address="%s" MXPref="10" TTL="120" />
    </DomainDNSGetHostsResult>
  </CommandResponse>
</ApiResponse>`, currentTXT))
	}
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		q := req.URL.Query()
		if q.Get("ApiUser") != "apiuser" || q.Get("ApiKey") != "apikey" || q.Get("UserName") != "apiuser" || q.Get("ClientIp") != "203.0.113.9" {
			t.Errorf("Namecheap 公共参数不对：%v", q)
		}
		switch q.Get("Command") {
		case "namecheap.domains.dns.getHosts":
			// 真实 Namecheap：SLD/TLD 不对时报错，探测才能继续往短后缀试；
			// 桩也按这个语义来（只有 example.com 才有）。
			if q.Get("SLD") != "example" || q.Get("TLD") != "com" {
				return 200, []byte(`<?xml version="1.0" encoding="utf-8"?><ApiResponse Status="ERROR" xmlns="http://api.namecheap.com/xml.response"><Errors><Error Number="1011101">Domain name not found</Error></Errors></ApiResponse>`), nil
			}
			return 200, hostsXML(), nil
		case "namecheap.domains.dns.setHosts":
			mu.Lock()
			setHostsCalls = append(setHostsCalls, q)
			// 回写后更新当前 TXT 值（没有这条 TXT 时置空）。
			currentTXT = ""
			for i := 1; i <= 10; i++ {
				n := fmt.Sprintf("%d", i)
				if q.Get("HostName"+n) == "_acme-challenge.ccpanel" && q.Get("RecordType"+n) == "TXT" {
					currentTXT = q.Get("Address" + n)
				}
			}
			mu.Unlock()
			return 200, []byte(`<?xml version="1.0" encoding="utf-8"?><ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response"><CommandResponse Type="namecheap.domains.dns.setHosts"><DomainDNSSetHostsResult Domain="example.com" IsSuccess="true" /></CommandResponse></ApiResponse>`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := namecheapProvider{}
	stale, err := p.ListStale(s, "ccpanel.example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-val，实际 %+v", stale)
	}
	// Create：setHosts 必须把 www 的 A 记录原样带回去，只换 TXT 的值。
	if _, err := p.Create(s, "ccpanel.example.com", "new-val"); err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	mu.Lock()
	if len(setHostsCalls) != 1 {
		mu.Unlock()
		t.Fatalf("Create 应调 1 次 setHosts，实际 %d 次", len(setHostsCalls))
	}
	q := setHostsCalls[0]
	mu.Unlock()
	hosts := map[string]string{}
	for i := 1; i <= 3; i++ {
		n := fmt.Sprintf("%d", i)
		if name := q.Get("HostName" + n); name != "" {
			hosts[name] = q.Get("RecordType"+n) + ":" + q.Get("Address"+n)
		}
	}
	if hosts["www"] != "A:203.0.113.1" {
		t.Fatalf("setHosts 丢了原有的 www A 记录：%v", hosts)
	}
	if hosts["_acme-challenge.ccpanel"] != "TXT:new-val" {
		t.Fatalf("setHosts 里的 TXT 应是新值：%v", hosts)
	}
	// Delete：setHosts 里不应再有这条 TXT，www 还在。
	mu.Lock()
	setHostsCalls = nil
	mu.Unlock()
	if err := p.Delete(s, "ccpanel.example.com", "", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(setHostsCalls) != 1 {
		t.Fatalf("Delete 应调 1 次 setHosts，实际 %d 次", len(setHostsCalls))
	}
	q = setHostsCalls[0]
	if q.Get("HostName1") != "www" || q.Get("Address1") != "203.0.113.1" {
		t.Fatalf("Delete 回写应只剩 www：%v", q)
	}
	if q.Get("HostName2") != "" {
		t.Fatalf("Delete 回写不应再有 TXT 记录：%v", q)
	}
}

func TestNamecheapErrors1349(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.NamecheapAPIUser = "apiuser"
	s.cfg.NamecheapAPIKey = "apikey"
	s.cfg.NamecheapClientIP = "203.0.113.9"
	// 白名单报错：提到 IP 时要明说白名单。
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		return 200, []byte(`<?xml version="1.0" encoding="utf-8"?><ApiResponse Status="ERROR" xmlns="http://api.namecheap.com/xml.response"><Errors><Error Number="1011102">IP 203.0.113.9 is not whitelisted for API access</Error></Errors></ApiResponse>`), nil
	})
	_, err := (namecheapProvider{}).ListStale(s, "ccpanel.example.com")
	if err == nil {
		t.Fatal("Status=ERROR 时应返回错误")
	}
	if !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("IP 报错应明说白名单，实际：%v", err)
	}
	// 没用 BasicDNS 的域名：报错里写清楚。
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		return 200, []byte(`<?xml version="1.0" encoding="utf-8"?><ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response"><CommandResponse Type="namecheap.domains.dns.getHosts"><DomainDNSGetHostsResult Domain="example.com" IsUsingOurDNS="false"></DomainDNSGetHostsResult></CommandResponse></ApiResponse>`), nil
	})
	_, err = (namecheapProvider{}).ListStale(s, "ccpanel.example.com")
	if err == nil || !strings.Contains(err.Error(), "BasicDNS") {
		t.Fatalf("IsUsingOurDNS=false 时应报 BasicDNS，实际：%v", err)
	}
}

// ---------- 8. 设置接口：保存服务商与凭证 ----------

func TestHandleDNSProvider1349(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	s.cfg.CloudflareToken = "cfut_oldtoken1349"

	// 未知服务商 → 400。
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"bogus"}`)
	if code != 400 {
		t.Fatalf("未知服务商应 400，实际 %d：%v", code, resp)
	}
	// 阿里云凭证没填齐 → 400，且不留半截状态、不切换服务商。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"aliyun","access_key_id":"id-1"}`)
	if code != 400 {
		t.Fatalf("凭证没填齐应 400，实际 %d", code)
	}
	if s.cfg.DNSProvider == "aliyun" || s.cfg.AliyunAccessKeyID == "id-1" {
		t.Fatal("400 时不应切换服务商、也不应留半截凭证")
	}
	// 填齐 → 200，旧 CF Token 不动。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"aliyun","access_key_id":"id-1","access_key_secret":"sec-1"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("填齐应 200，实际 %d：%v", code, resp)
	}
	if s.cfg.DNSProvider != "aliyun" || s.cfg.AliyunAccessKeyID != "id-1" || s.cfg.CloudflareToken != "cfut_oldtoken1349" {
		t.Fatalf("保存后状态不对：provider=%q cf=%q", s.cfg.DNSProvider, s.cfg.CloudflareToken)
	}
	// Namecheap Client IP 不是 IPv4 → 400。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"namecheap","api_user":"u","api_key":"k","client_ip":"not-an-ip"}`)
	if code != 400 {
		t.Fatalf("Client IP 不是 IPv4 应 400，实际 %d", code)
	}
	// 切到 manual 不用凭证 → 200。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"manual"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("切 manual 应 200，实际 %d：%v", code, resp)
	}
	// 设置页回填：dns_provider、dns_configured、cloudflare 旧字段都在。
	code, settings := getJSON144(t, h, cookie, "/api/v1/settings")
	if code != 200 {
		t.Fatalf("设置页应 200，实际 %d", code)
	}
	if settings["dns_provider"] != "manual" {
		t.Fatalf("dns_provider 应为 manual，实际 %v", settings["dns_provider"])
	}
	cfgd, ok := settings["dns_configured"].(map[string]any)
	if !ok || cfgd["aliyun"] != true || cfgd["cloudflare"] != true || cfgd["route53"] != false {
		t.Fatalf("dns_configured 不对：%v", settings["dns_configured"])
	}
	cf, ok := settings["cloudflare"].(map[string]any)
	if !ok || cf["has_token"] != true {
		t.Fatalf("cloudflare 旧字段应还在且 has_token=true：%v", settings["cloudflare"])
	}
}

// ---------- 9. 手动模式两步走 ----------

func TestManualModeTwoStep1349(t *testing.T) {
	home := t.TempDir()
	s := newTestServer1349(t, home)
	h := s.routes()
	cookie := loginCookie144(t, h)
	restarts := stubRestart144(t)
	// 切到手动模式。
	code, _ := postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"manual"}`)
	if code != 200 {
		t.Fatalf("切 manual 应 200，实际 %d", code)
	}
	// 假 ACME + 跳过等待。
	m := newMockACME145(t, 201)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	t.Cleanup(func() { acmeDirectoryURL = oldDir })
	stubACMESleep1349(t)
	// handleHTTPS 在手动模式下带域名点开启 → 400（不许走自动签发）。
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/https", `{"enable":true,"domain":"example.com"}`)
	if code != 400 || !strings.Contains(fmt.Sprint(resp["error"]), "手动模式") {
		t.Fatalf("手动模式下 handleHTTPS 应 400，实际 %d：%v", code, resp)
	}
	// 第一步：下单，返回 TXT 名与值，pending 落盘（600 权限）。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/https/manual/start", `{"domain":"example.com"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("manual/start 应 200，实际 %d：%v", code, resp)
	}
	txtName, _ := resp["txt_name"].(string)
	txtValue, _ := resp["txt_value"].(string)
	if txtName != "_acme-challenge.example.com" {
		t.Fatalf("txt_name 应为 _acme-challenge.example.com，实际 %q", txtName)
	}
	if len(txtValue) != 43 {
		t.Fatalf("txt_value 应为 43 字符的 keyAuthorization 哈希，实际 %q", txtValue)
	}
	pendPath := filepath.Join(home, "certs", "acme-pending.json")
	st, err := os.Stat(pendPath)
	if err != nil {
		t.Fatalf("pending 文件应落盘：%v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("pending 文件权限应为 600，实际 %o", st.Mode().Perm())
	}
	// 模拟面板重启：新 Server 实例读同一 PanelHome，pending 还在。
	s2 := newTestServer1349(t, home)
	p2, err := s2.loadPendingOrder()
	if err != nil {
		t.Fatalf("重启后 pending 应能读回：%v", err)
	}
	if p2.Domain != "example.com" || len(p2.Authz) != 1 || p2.Authz[0].TXTValue != txtValue {
		t.Fatalf("重启后读回的 pending 不对：%+v", p2)
	}
	// 第二步：DNS 查到 TXT 后继续验证，证书落盘，pending 删除，面板重启。
	oldLookup := acmeLookupTXT
	acmeLookupTXT = func(name string) ([]string, error) {
		if name == "_acme-challenge.example.com" {
			return []string{txtValue}, nil
		}
		return nil, fmt.Errorf("no such host")
	}
	t.Cleanup(func() { acmeLookupTXT = oldLookup })
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/https/manual/confirm", `{"domain":"example.com"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("manual/confirm 应 200，实际 %d：%v", code, resp)
	}
	certB, err := os.ReadFile(filepath.Join(home, "certs", "cert.pem"))
	if err != nil {
		t.Fatalf("cert.pem 没写下来：%v", err)
	}
	if string(certB) != string(m.certPEM) {
		t.Fatal("cert.pem 不是假服务端发的证书")
	}
	if _, err := os.Stat(pendPath); !os.IsNotExist(err) {
		t.Fatal("签发成功后 pending 文件应删除")
	}
	if !s.cfg.HTTPSEnabled || s.cfg.HTTPSDomain != "example.com" {
		t.Fatalf("签发成功后 HTTPS 应开启：enabled=%v domain=%q", s.cfg.HTTPSEnabled, s.cfg.HTTPSDomain)
	}
	waitRestart144(t, restarts, 1)
	// 设置页里 manual_pending 应已消失。
	_, settings := getJSON144(t, h, cookie, "/api/v1/settings")
	if settings["manual_pending"] != nil {
		t.Fatalf("签发成功后 manual_pending 应为空，实际 %v", settings["manual_pending"])
	}
}

func TestManualModeExpiryAndCancel1349(t *testing.T) {
	home := t.TempDir()
	s := newTestServer1349(t, home)
	h := s.routes()
	cookie := loginCookie144(t, h)
	stubRestart144(t)
	code, _ := postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"manual"}`)
	if code != 200 {
		t.Fatalf("切 manual 应 200，实际 %d", code)
	}
	m := newMockACME145(t, 201)
	oldDir := acmeDirectoryURL
	acmeDirectoryURL = m.url("/directory")
	t.Cleanup(func() { acmeDirectoryURL = oldDir })
	stubACMESleep1349(t)
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/https/manual/start", `{"domain":"example.com"}`)
	if code != 200 {
		t.Fatalf("manual/start 应 200，实际 %d：%v", code, resp)
	}
	// 错域名确认 → 400。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/https/manual/confirm", `{"domain":"other.example.com"}`)
	if code != 400 || !strings.Contains(fmt.Sprint(resp["error"]), "不是") {
		t.Fatalf("错域名确认应 400，实际 %d：%v", code, resp)
	}
	// 过期作废：把 pending 的过期时间改到过去再确认 → 400。
	p, err := s.loadPendingOrder()
	if err != nil {
		t.Fatalf("loadPendingOrder 失败：%v", err)
	}
	p.ExpiresAt = time.Now().Add(-time.Minute)
	if err := s.savePendingOrder(p); err != nil {
		t.Fatalf("savePendingOrder 失败：%v", err)
	}
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/https/manual/confirm", `{"domain":"example.com"}`)
	if code != 400 || !strings.Contains(fmt.Sprint(resp["error"]), "过期") {
		t.Fatalf("过期订单确认应 400 报过期，实际 %d：%v", code, resp)
	}
	// 取消：删 pending 文件。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/https/manual/cancel", `{}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("manual/cancel 应 200，实际 %d：%v", code, resp)
	}
	if _, err := s.loadPendingOrder(); err == nil {
		t.Fatal("取消后 pending 应已删除")
	}
}

func TestObtainACMECertManualRejected1349(t *testing.T) {
	s := newTestServer144(t)
	// 手动模式：obtainACMECert 直接拒绝（handleHTTPS 会提前拦，这里是第二道防线）。
	s.cfg.DNSProvider = "manual"
	if err := s.obtainACMECert("example.com"); err == nil || !strings.Contains(err.Error(), "手动模式") {
		t.Fatalf("手动模式下 obtainACMECert 应报手动模式，实际：%v", err)
	}
	// API 服务商凭证没配齐：先报错，不浪费一次 ACME 下单。
	s.cfg.DNSProvider = "aliyun"
	if err := s.obtainACMECert("example.com"); err == nil || !strings.Contains(err.Error(), "凭证") {
		t.Fatalf("凭证没配齐时应报凭证，实际：%v", err)
	}
}

func TestMaybeRenewManualSkipped1349(t *testing.T) {
	s := newTestServer144(t)
	got := captureTelegram140(t)
	enableTelegram140(s)
	restarts := stubRestart144(t)
	writeCAleaf144(t, s, 10) // 真证书剩 10 天，本应续
	s.cfg.HTTPSEnabled = true
	s.cfg.HTTPSDomain = "example.com"
	s.cfg.DNSProvider = "manual"
	s.maybeRenewPanelCert()
	if len(*got) != 0 || s.cfg.PanelCertLastErr != "" || restarts.Load() != 0 {
		t.Fatalf("手动模式下自动续签应什么都不做：telegram=%v err=%q restarts=%d", *got, s.cfg.PanelCertLastErr, restarts.Load())
	}
}

// ---------- 10. 版本断言 ----------

func TestVersion1349(t *testing.T) {
	if Version != "1.4.19" {
		t.Fatalf("VERSION 应是 1.4.19，实际 %q", Version)
	}
}
