package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- 1.4.15：第一批 11 家 DNS 服务商（mock HTTP，不调真实 API） ----------
// 桩沿用 panel_1349_test.go 的 stubDNSHTTP1349 与 newTestServer144。

func TestVersion1415(t *testing.T) {
	if Version != "1.4.37" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// ---------- 1. 注册：18 个 id 都能解析，manual 排最后 ----------

func TestDNSProviderRegistry1415(t *testing.T) {
	want := []string{
		"cloudflare", "aliyun", "dnspod", "route53", "godaddy", "namecheap",
		"digitalocean", "linode", "vultr", "bunny", "hetzner",
		"spaceship", "gandi", "namesilo", "desec", "porkbun", "cloudns",
		"manual",
	}
	if len(dnsProviderIDs) != len(want) {
		t.Fatalf("服务商应为 %d 个，实际 %d：%v", len(want), len(dnsProviderIDs), dnsProviderIDs)
	}
	for i, id := range want {
		if dnsProviderIDs[i] != id {
			t.Fatalf("dnsProviderIDs[%d] 应为 %q，实际 %q", i, id, dnsProviderIDs[i])
		}
		if dnsProviderNames[id] == "" {
			t.Fatalf("服务商 %q 没有显示名", id)
		}
	}
	for _, id := range want {
		if dnsProviderFor(id) == nil {
			t.Fatalf("dnsProviderFor(%q) 应有实现", id)
		}
	}
	// 未知 id 回落到 Cloudflare（dnsProviderFor 的默认分支）。
	if dnsProviderFor("bogus").ID() != "cloudflare" {
		t.Fatal("dnsProviderFor(bogus) 应回落到 cloudflare")
	}
	if validDNSProvider("bogus") {
		t.Fatal("validDNSProvider(bogus) 应为 false")
	}
}

// ---------- 2. 凭证配齐：每家填了算已配置，缺了算未配置 ----------

func TestDNSProviderConfigured1415(t *testing.T) {
	s := newTestServer144(t)
	all := []string{"digitalocean", "linode", "vultr", "bunny", "hetzner", "spaceship", "gandi", "namesilo", "desec", "porkbun", "cloudns"}
	for _, id := range all {
		if s.dnsProviderConfigured(id) {
			t.Fatalf("%s 没填凭证时应为未配置", id)
		}
	}
	s.cfg.DigitalOceanToken = "tok"
	s.cfg.LinodeToken = "tok"
	s.cfg.VultrAPIKey = "key"
	s.cfg.BunnyAPIKey = "key"
	s.cfg.HetznerToken = "tok"
	s.cfg.SpaceshipAPIKey = "k"
	s.cfg.SpaceshipSecret = "s"
	s.cfg.GandiToken = "tok"
	s.cfg.NameSiloAPIKey = "key"
	s.cfg.DesecToken = "tok"
	s.cfg.PorkbunAPIKey = "k"
	s.cfg.PorkbunSecret = "s"
	s.cfg.CloudNSAuthID = "id"
	s.cfg.CloudNSPassword = "pw"
	for _, id := range all {
		if !s.dnsProviderConfigured(id) {
			t.Fatalf("%s 填齐凭证后应为已配置", id)
		}
	}
	// Spaceship 只填一半不算；CloudNS 只填子账号不算（须 auth-id＋密码）。
	s.cfg.SpaceshipSecret = ""
	if s.dnsProviderConfigured("spaceship") {
		t.Fatal("Spaceship 只填 API Key 不应算已配置")
	}
	s.cfg.SpaceshipSecret = "s"
	s.cfg.CloudNSAuthID = ""
	s.cfg.CloudNSPassword = ""
	s.cfg.CloudNSSubUser = "sub"
	if s.dnsProviderConfigured("cloudns") {
		t.Fatal("CloudNS 只填子账号不应算已配置")
	}
}

// ---------- 3. DigitalOcean：按 ID 删 ----------

func TestDigitalOceanCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DigitalOceanToken = "do-tok"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer do-tok" {
			t.Errorf("DO 必须用 Bearer Token，实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/v2/domains/example.com":
			return 200, []byte(`{"domain":{"name":"example.com"}}`), nil
		case req.Method == "GET" && req.URL.Path == "/v2/domains/example.com/records":
			return 200, []byte(`{"domain_records":[{"id":123,"type":"TXT","name":"_acme-challenge","data":"old-val","ttl":300},{"id":124,"type":"A","name":"www","data":"203.0.113.1"}]}`), nil
		case req.Method == "POST" && req.URL.Path == "/v2/domains/example.com/records":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["type"] != "TXT" || payload["name"] != "_acme-challenge" || payload["data"] != "new-val" || payload["ttl"] != float64(300) {
				t.Errorf("DO 建记录参数不对：%s", string(body))
			}
			return 201, []byte(`{"domain_record":{"id":456,"type":"TXT","name":"_acme-challenge","data":"new-val"}}`), nil
		case req.Method == "DELETE":
			return 204, []byte(``), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := digitaloceanProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "123" || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 id=123，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "456" {
		t.Fatalf("Create 应返回 record id 456，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "456", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com/records",
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com/records",
		"DELETE /v2/domains/example.com/records/123",
		"POST /v2/domains/example.com/records",
		"GET /v2/domains/example.com",
		"DELETE /v2/domains/example.com/records/456",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestDigitalOceanError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DigitalOceanToken = "do-tok"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Method == "GET" && req.URL.Path == "/v2/domains/example.com" {
			return 200, []byte(`{"domain":{"name":"example.com"}}`), nil
		}
		if req.Method == "GET" {
			return 200, []byte(`{"domain_records":[]}`), nil
		}
		return 500, []byte(`{"id":"server_error","message":"Something went wrong"}`), nil
	})
	_, err := (digitaloceanProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("POST 500 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "Something went wrong") {
		t.Fatalf("错误应透出对方的 message，实际：%v", err)
	}
}

// ---------- 4. Linode：按 ID 删，zone 是数字 id ----------

func TestLinodeCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.LinodeToken = "linode-tok"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer linode-tok" {
			t.Errorf("Linode 必须用 Bearer Token，实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/v4/domains":
			return 200, []byte(`{"data":[{"id":7,"domain":"example.com"},{"id":8,"domain":"other.com"}]}`), nil
		case req.Method == "GET" && req.URL.Path == "/v4/domains/7/records":
			return 200, []byte(`{"data":[{"id":55,"type":"TXT","name":"_acme-challenge","target":"old-val"},{"id":56,"type":"A","name":"www","target":"203.0.113.1"}]}`), nil
		case req.Method == "POST" && req.URL.Path == "/v4/domains/7/records":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["type"] != "TXT" || payload["name"] != "_acme-challenge" || payload["target"] != "new-val" || payload["ttl_sec"] != float64(300) {
				t.Errorf("Linode 建记录参数不对：%s", string(body))
			}
			return 200, []byte(`{"id":66,"type":"TXT","name":"_acme-challenge","target":"new-val"}`), nil
		case req.Method == "DELETE":
			return 200, []byte(`{}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := linodeProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "55" {
		t.Fatalf("ListStale 应返回 1 条残留 id=55，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "66" {
		t.Fatalf("Create 应返回 record id 66，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "66", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /v4/domains",
		"GET /v4/domains/7/records",
		"GET /v4/domains",
		"GET /v4/domains",
		"GET /v4/domains/7/records",
		"DELETE /v4/domains/7/records/55",
		"POST /v4/domains/7/records",
		"GET /v4/domains",
		"DELETE /v4/domains/7/records/66",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

// ---------- 5. Vultr：TXT 值须包双引号，按 ID 删 ----------

func TestVultrCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.VultrAPIKey = "vultr-key"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer vultr-key" {
			t.Errorf("Vultr 必须用 Bearer Key，实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/v2/domains/example.com":
			return 200, []byte(`{"domain":{"domain":"example.com"}}`), nil
		case req.Method == "GET" && req.URL.Path == "/v2/domains/example.com/records":
			return 200, []byte(`{"records":[{"id":"abc","type":"TXT","name":"_acme-challenge","data":"\"old-val\""},{"id":"abd","type":"A","name":"www","data":"203.0.113.1"}]}`), nil
		case req.Method == "POST" && req.URL.Path == "/v2/domains/example.com/records":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["type"] != "TXT" || payload["name"] != "_acme-challenge" || payload["data"] != `"new-val"` || payload["ttl"] != float64(300) {
				t.Errorf("Vultr 建记录参数不对（data 须包双引号）：%s", string(body))
			}
			return 201, []byte(`{"record":{"id":"def","type":"TXT","name":"_acme-challenge","data":"\"new-val\""}}`), nil
		case req.Method == "DELETE":
			return 204, []byte(``), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := vultrProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "abc" {
		t.Fatalf("ListStale 应返回 1 条残留 id=abc，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "def" {
		t.Fatalf("Create 应返回 record id def，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "def", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com/records",
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com",
		"GET /v2/domains/example.com/records",
		"DELETE /v2/domains/example.com/records/abc",
		"POST /v2/domains/example.com/records",
		"GET /v2/domains/example.com",
		"DELETE /v2/domains/example.com/records/def",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

// ---------- 6. Bunny DNS：AccessKey 头、Type 整数枚举、按 ID 删 ----------

func TestBunnyCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.BunnyAPIKey = "bunny-key"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("AccessKey") != "bunny-key" {
			t.Errorf("Bunny 必须用 AccessKey 头，实际 %q", req.Header.Get("AccessKey"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/dnszone":
			return 200, []byte(`[{"Id":9,"Domain":"example.com"}]`), nil
		case req.Method == "GET" && req.URL.Path == "/dnszone/9/records":
			return 200, []byte(`[{"Id":77,"Type":3,"Name":"_acme-challenge","Value":"old-val","Ttl":60},{"Id":78,"Type":0,"Name":"www","Value":"203.0.113.1"}]`), nil
		case req.Method == "PUT" && req.URL.Path == "/dnszone/9/records":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["Type"] != float64(3) || payload["Name"] != "_acme-challenge" || payload["Value"] != "new-val" || payload["Ttl"] != float64(60) {
				t.Errorf("Bunny 建记录参数不对（Type 须是整数 3）：%s", string(body))
			}
			return 200, []byte(`{"Id":88,"Type":3,"Name":"_acme-challenge","Value":"new-val"}`), nil
		case req.Method == "DELETE":
			return 200, []byte(`{}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := bunnyProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "77" {
		t.Fatalf("ListStale 应返回 1 条残留 id=77，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "88" {
		t.Fatalf("Create 应返回 record id 88，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "88", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /dnszone",
		"GET /dnszone/9/records",
		"GET /dnszone",
		"GET /dnszone",
		"GET /dnszone/9/records",
		"DELETE /dnszone/9/records/77",
		"PUT /dnszone/9/records",
		"GET /dnszone",
		"DELETE /dnszone/9/records/88",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

// ---------- 7. Hetzner：Cloud API 的 RRSet 模型＋异步 Action 轮询 ----------

func stubHetzner1415(t *testing.T, rrsetsBody string) (*sync.Mutex, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer hetzner-tok" {
			t.Errorf("Hetzner 必须用 Bearer Token（Cloud API），实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/v1/zones":
			return 200, []byte(`{"zones":[{"id":1,"name":"example.com"}]}`), nil
		case req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/v1/zones/example.com/rrsets"):
			return 200, []byte(rrsetsBody), nil
		case req.Method == "POST" && req.URL.Path == "/v1/zones/example.com/rrsets":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["name"] != "_acme-challenge" || payload["type"] != "TXT" {
				t.Errorf("Hetzner 建 RRSet 参数不对：%s", string(body))
			}
			return 201, []byte(`{"action":{"id":5,"status":"running"}}`), nil
		case req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/actions/set_records"):
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			recs, _ := payload["records"].([]any)
			if len(recs) != 1 {
				t.Errorf("Hetzner set_records 应只写 1 条值：%s", string(body))
			}
			return 201, []byte(`{"action":{"id":5,"status":"running"}}`), nil
		case req.Method == "GET" && req.URL.Path == "/v1/actions/5":
			return 200, []byte(`{"action":{"id":5,"status":"success"}}`), nil
		case req.Method == "DELETE" && req.URL.Path == "/v1/zones/example.com/rrsets/_acme-challenge/TXT":
			return 201, []byte(`{"action":{"id":6,"status":"success"}}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	return &mu, &calls
}

func TestHetznerSetRecords1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.HetznerToken = "hetzner-tok"
	// 轮询不要真睡。
	old := hetznerPollSleep
	hetznerPollSleep = func(time.Duration) {}
	t.Cleanup(func() { hetznerPollSleep = old })
	mu, calls := stubHetzner1415(t, `{"rrsets":[{"name":"_acme-challenge","type":"TXT","ttl":60,"records":[{"value":"old-val"}]}]}`)
	p := hetznerProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-val，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "" {
		t.Fatalf("Hetzner 按 RRSet 删，Create 应返回空 ID，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /v1/zones",
		"GET /v1/zones/example.com/rrsets",
		"GET /v1/zones",
		"GET /v1/zones/example.com/rrsets",
		"POST /v1/zones/example.com/rrsets/_acme-challenge/TXT/actions/set_records",
		"GET /v1/actions/5",
		"GET /v1/zones",
		"DELETE /v1/zones/example.com/rrsets/_acme-challenge/TXT",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", *calls, want)
	}
	for i := range want {
		if (*calls)[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", *calls, want)
		}
	}
}

func TestHetznerCreateNew1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.HetznerToken = "hetzner-tok"
	old := hetznerPollSleep
	hetznerPollSleep = func(time.Duration) {}
	t.Cleanup(func() { hetznerPollSleep = old })
	_, calls := stubHetzner1415(t, `{"rrsets":[]}`)
	if _, err := (hetznerProvider{}).Create(s, "example.com", "new-val"); err != nil {
		t.Fatalf("RRSet 不存在时 Create 应走 POST 新建，失败：%v", err)
	}
	want := []string{
		"GET /v1/zones",
		"GET /v1/zones/example.com/rrsets",
		"POST /v1/zones/example.com/rrsets",
		"GET /v1/actions/5",
	}
	if len(*calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", *calls, want)
	}
	for i := range want {
		if (*calls)[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", *calls, want)
		}
	}
}

func TestHetznerError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.HetznerToken = "hetzner-tok"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.URL.Path == "/v1/zones" {
			return 200, []byte(`{"zones":[{"id":1,"name":"example.com"}]}`), nil
		}
		if strings.HasPrefix(req.URL.Path, "/v1/zones/example.com/rrsets") && req.Method == "GET" {
			return 200, []byte(`{"rrsets":[]}`), nil
		}
		return 400, []byte(`{"error":{"message":"zone not found","code":"not_found"}}`), nil
	})
	_, err := (hetznerProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("POST 400 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "zone not found") {
		t.Fatalf("错误应透出对方的 message，实际：%v", err)
	}
}

// ---------- 8. Spaceship：PUT 不覆盖旧值，删必须传原值 ----------

func TestSpaceshipCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.SpaceshipAPIKey = "sp-key"
	s.cfg.SpaceshipSecret = "sp-secret"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("X-API-Key") != "sp-key" || req.Header.Get("X-API-Secret") != "sp-secret" {
			t.Errorf("Spaceship 必须用 X-API-Key＋X-API-Secret，实际 %q / %q", req.Header.Get("X-API-Key"), req.Header.Get("X-API-Secret"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Query().Get("take") == "1":
			return 200, []byte(`{"items":[],"total":0}`), nil
		case req.Method == "GET" && req.URL.Query().Get("take") == "500":
			return 200, []byte(`{"items":[{"type":"TXT","name":"_acme-challenge","address":"old-val","ttl":60},{"type":"A","name":"www","address":"203.0.113.1"}],"total":2}`), nil
		case req.Method == "PUT":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["force"] != true {
				t.Errorf("Spaceship PUT 须带 force=true：%s", string(body))
			}
			items, _ := payload["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("Spaceship PUT 应只写 1 条：%s", string(body))
			}
			it, _ := items[0].(map[string]any)
			if it["type"] != "TXT" || it["name"] != "_acme-challenge" || it["address"] != "new-val" || it["ttl"] != float64(60) {
				t.Errorf("Spaceship PUT 参数不对：%s", string(body))
			}
			return 200, []byte(`{"items":[{"type":"TXT","name":"_acme-challenge","address":"new-val"}]}`), nil
		case req.Method == "DELETE":
			body, _ := io.ReadAll(req.Body)
			var items []map[string]string
			_ = json.Unmarshal(body, &items)
			if len(items) != 1 || items[0]["type"] != "TXT" || items[0]["address"] == "" {
				t.Errorf("Spaceship DELETE 必须传当初写入的原值：%s", string(body))
			}
			return 200, []byte(`{"items":[]}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := spaceshipProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-val，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "" {
		t.Fatalf("Spaceship 按值删，Create 应返回空 ID，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	// Create 先按值删残留（DELETE 传 old-val），再 PUT；Delete 传 new-val。
	// 只校验调用次数与顺序的大框，不逐条比 body（body 已在桩里断言）。
	want := []string{
		"GET /api/v1/dns/records/example.com",
		"GET /api/v1/dns/records/example.com",
		"GET /api/v1/dns/records/example.com",
		"GET /api/v1/dns/records/example.com",
		"GET /api/v1/dns/records/example.com",
		"DELETE /api/v1/dns/records/example.com",
		"PUT /api/v1/dns/records/example.com",
		"GET /api/v1/dns/records/example.com",
		"DELETE /api/v1/dns/records/example.com",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestSpaceshipError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.SpaceshipAPIKey = "sp-key"
	s.cfg.SpaceshipSecret = "sp-secret"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Method == "GET" {
			return 200, []byte(`{"items":[],"total":0}`), nil
		}
		return 400, []byte(`{"detail":"Invalid API credentials","status":400}`), nil
	})
	_, err := (spaceshipProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("PUT 400 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "Invalid API credentials") {
		t.Fatalf("错误应透出 problem+json 的 detail，实际：%v", err)
	}
}

// ---------- 9. Gandi：RRSet 模型，先 DELETE 整条再 POST ----------

func TestGandiCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.GandiToken = "gandi-tok"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Bearer gandi-tok" {
			t.Errorf("Gandi 必须用 Bearer Token，实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/v5/livedns/domains/example.com":
			return 200, []byte(`{"fqdn":"example.com"}`), nil
		case req.Method == "GET" && req.URL.Path == "/v5/livedns/domains/example.com/records/_acme-challenge/TXT":
			return 200, []byte(`[{"rrset_name":"_acme-challenge","rrset_type":"TXT","rrset_ttl":300,"rrset_values":["\"old-val\""]}]`), nil
		case req.Method == "DELETE":
			return 204, []byte(``), nil
		case req.Method == "POST":
			body, _ := io.ReadAll(req.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			vals, _ := payload["rrset_values"].([]any)
			if len(vals) != 1 || vals[0] != `"new-val"` || payload["rrset_ttl"] != float64(300) {
				t.Errorf("Gandi POST 参数不对（值须包双引号、TTL 显式 300）：%s", string(body))
			}
			return 201, []byte(`{"rrset_name":"_acme-challenge","rrset_values":["\"new-val\""]}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := gandiProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-val（值去双引号），实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "" {
		t.Fatalf("Gandi 按 RRSet 删，Create 应返回空 ID，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /v5/livedns/domains/example.com",
		"GET /v5/livedns/domains/example.com/records/_acme-challenge/TXT",
		"GET /v5/livedns/domains/example.com",
		"DELETE /v5/livedns/domains/example.com/records/_acme-challenge/TXT",
		"POST /v5/livedns/domains/example.com/records/_acme-challenge/TXT",
		"GET /v5/livedns/domains/example.com",
		"DELETE /v5/livedns/domains/example.com/records/_acme-challenge/TXT",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

// ---------- 10. NameSilo：GET 式 API，成功只认 reply.code=300 ----------

func TestNameSiloCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.NameSiloAPIKey = "ns-key"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		q := req.URL.Query()
		if q.Get("key") != "ns-key" || q.Get("version") != "1" || q.Get("type") != "json" {
			t.Errorf("NameSilo key 须放查询串（version=1&type=json）：%v", q)
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/api/dnsListRecords":
			return 200, []byte(`{"reply":{"code":"300","resource_record":[{"record_id":"99","type":"TXT","host":"_acme-challenge","value":"old-val"},{"record_id":"98","type":"A","host":"www","value":"203.0.113.1"}]}}`), nil
		case "/api/dnsAddRecord":
			if q.Get("rrhost") != "_acme-challenge" || q.Get("rrvalue") != "new-val" || q.Get("rrtype") != "TXT" || q.Get("rrttl") != "3600" {
				t.Errorf("NameSilo 加记录参数不对：%v", q)
			}
			return 200, []byte(`{"reply":{"code":"300","record_id":"100"}}`), nil
		case "/api/dnsDeleteRecord":
			if q.Get("rrid") == "" {
				t.Errorf("NameSilo 删记录须传 rrid：%v", q)
			}
			return 200, []byte(`{"reply":{"code":"300"}}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := namesiloProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "99" {
		t.Fatalf("ListStale 应返回 1 条残留 rrid=99，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "100" {
		t.Fatalf("Create 应返回 record_id 100，实际 %q", id)
	}
	// Delete 按 Create 返回的 id 直接删（dnsDeleteRecord?rrid=100）。
	if err := p.Delete(s, "example.com", "100", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	// ListStale 是 findZone（1 次列表）＋再列表（1 次）；Create 是
	// findZone（1 次）＋ListStale（2 次）＋删残留（1 次）＋新增（1 次）；
	// Delete 是 findZone（1 次）＋删（1 次）。共 9 次。
	want := []string{
		"GET /api/dnsListRecords",
		"GET /api/dnsListRecords",
		"GET /api/dnsListRecords",
		"GET /api/dnsListRecords",
		"GET /api/dnsListRecords",
		"GET /api/dnsDeleteRecord",
		"GET /api/dnsAddRecord",
		"GET /api/dnsListRecords",
		"GET /api/dnsDeleteRecord",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestNameSiloError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.NameSiloAPIKey = "ns-key"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.URL.Path == "/api/dnsAddRecord" {
			return 200, []byte(`{"reply":{"code":"101","detail":"Invalid API key"}}`), nil
		}
		return 200, []byte(`{"reply":{"code":"300","resource_record":[]}}`), nil
	})
	_, err := (namesiloProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("reply.code 不是 300 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Fatalf("错误应透出 detail，实际：%v", err)
	}
}

// ---------- 11. deSEC：PATCH 空 records 清 RRSet，429 自动退避重试 ----------

func TestDesecCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DesecToken = "desec-tok"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Header.Get("Authorization") != "Token desec-tok" {
			t.Errorf("deSEC 必须用 Authorization: Token（不是 Bearer），实际 %q", req.Header.Get("Authorization"))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch {
		case req.Method == "GET" && req.URL.Path == "/api/v1/domains/":
			return 200, []byte(`[{"name":"example.com"}]`), nil
		case req.Method == "GET" && req.URL.Path == "/api/v1/domains/example.com/rrsets/":
			return 200, []byte(`[{"subname":"_acme-challenge","type":"TXT","records":["\"old-val\""],"ttl":3600}]`), nil
		case req.Method == "PATCH":
			body, _ := io.ReadAll(req.Body)
			var payload []map[string]any
			_ = json.Unmarshal(body, &payload)
			if len(payload) != 1 || payload[0]["subname"] != "_acme-challenge" || payload[0]["type"] != "TXT" {
				t.Errorf("deSEC PATCH 必须是单元素 list：%s", string(body))
			}
			return 200, []byte(`[{"subname":"_acme-challenge","type":"TXT","records":["\"new-val\""]}]`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := desecProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].Value != "old-val" {
		t.Fatalf("ListStale 应返回 1 条残留 old-val，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "" {
		t.Fatalf("deSEC 按 RRSet 删，Create 应返回空 ID，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	want := []string{
		"GET /api/v1/domains/",
		"GET /api/v1/domains/example.com/rrsets/",
		"GET /api/v1/domains/",
		"PATCH /api/v1/domains/example.com/rrsets/",
		"GET /api/v1/domains/",
		"PATCH /api/v1/domains/example.com/rrsets/",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestDesecRetry1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.DesecToken = "desec-tok"
	old := desecRetrySleep
	desecRetrySleep = func(time.Duration) {}
	t.Cleanup(func() { desecRetrySleep = old })
	n := 0
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if req.Method == "GET" && req.URL.Path == "/api/v1/domains/" {
			return 200, []byte(`[{"name":"example.com"}]`), nil
		}
		if req.Method == "PATCH" {
			n++
			if n == 1 {
				// 429 带 Retry-After：退避后重试一次。
				return 429, []byte(`{"detail":"Request was throttled."}`), nil
			}
			return 200, []byte(`[{"subname":"_acme-challenge","type":"TXT","records":["\"v\""]}]`), nil
		}
		return 404, []byte(`{}`), nil
	})
	if _, err := (desecProvider{}).Create(s, "example.com", "v"); err != nil {
		t.Fatalf("429 后重试一次应成功，失败：%v", err)
	}
	if n != 2 {
		t.Fatalf("PATCH 应调 2 次（1 次 429＋1 次成功），实际 %d 次", n)
	}
}

// ---------- 12. Porkbun：凭证放 body，须先开 API Access ----------

func TestPorkbunCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.PorkbunAPIKey = "pk1_test"
	s.cfg.PorkbunSecret = "sk1_test"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		body, _ := io.ReadAll(req.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["apikey"] != "pk1_test" || payload["secretapikey"] != "sk1_test" {
			t.Errorf("Porkbun 凭证须放 body 的 apikey／secretapikey：%s", string(body))
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/api/json/v3/dns/retrieve/example.com":
			return 200, []byte(`{"status":"SUCCESS","records":[{"id":"42","type":"TXT","name":"_acme-challenge","content":"old-val"},{"id":"43","type":"A","name":"www","content":"203.0.113.1"}]}`), nil
		case "/api/json/v3/dns/create/example.com":
			if payload["name"] != "_acme-challenge" || payload["type"] != "TXT" || payload["content"] != "new-val" || payload["ttl"] != "600" {
				t.Errorf("Porkbun 建记录参数不对：%s", string(body))
			}
			return 200, []byte(`{"status":"SUCCESS","id":"44"}`), nil
		case "/api/json/v3/dns/delete/example.com/42", "/api/json/v3/dns/delete/example.com/44":
			return 200, []byte(`{"status":"SUCCESS"}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := porkbunProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "42" {
		t.Fatalf("ListStale 应返回 1 条残留 id=42，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "44" {
		t.Fatalf("Create 应返回 id 44，实际 %q", id)
	}
	// Delete 按 Create 返回的 id 直接删（/dns/delete/{zone}/44）。
	if err := p.Delete(s, "example.com", "44", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	// ListStale 是 findZone（1 次 retrieve）＋再查（1 次）；Create 是
	// findZone（1 次）＋ListStale（2 次）＋删残留（1 次）＋新增（1 次）；
	// Delete 是 findZone（1 次）＋删（1 次）。共 9 次。
	want := []string{
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/delete/example.com/42",
		"POST /api/json/v3/dns/create/example.com",
		"POST /api/json/v3/dns/retrieve/example.com",
		"POST /api/json/v3/dns/delete/example.com/44",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestPorkbunError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.PorkbunAPIKey = "pk1_test"
	s.cfg.PorkbunSecret = "sk1_test"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		if strings.HasSuffix(req.URL.Path, "/dns/retrieve/example.com") {
			return 200, []byte(`{"status":"SUCCESS","records":[]}`), nil
		}
		return 200, []byte(`{"status":"ERROR","message":"API_ACCESS_DISABLED"}`), nil
	})
	_, err := (porkbunProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("API_ACCESS_DISABLED 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "API Access") {
		t.Fatalf("错误应点明须先在后台打开 API Access，实际：%v", err)
	}
}

// ---------- 13. CloudNS：GET 式 API，错误也回 HTTP 200 ----------

func TestCloudNSCreateDelete1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.CloudNSAuthID = "id-1"
	s.cfg.CloudNSPassword = "pw-1"
	var mu sync.Mutex
	var calls []string
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		q := req.URL.Query()
		if q.Get("auth-id") != "id-1" || q.Get("auth-password") != "pw-1" {
			t.Errorf("CloudNS 凭证须放查询串 auth-id／auth-password：%v", q)
		}
		if q.Get("sub-auth-user") != "" {
			t.Errorf("没填子账号时不应传 sub-auth-user：%v", q)
		}
		mu.Lock()
		calls = append(calls, req.Method+" "+req.URL.Path)
		mu.Unlock()
		switch req.URL.Path {
		case "/dns/list-zones.json":
			return 200, []byte(`[{"name":"example.com"}]`), nil
		case "/dns/records.json":
			if q.Get("host") != "_acme-challenge" || q.Get("type") != "TXT" {
				t.Errorf("CloudNS 查记录参数不对：%v", q)
			}
			return 200, []byte(`[{"id":123,"type":"TXT","host":"_acme-challenge","record":"old-val"},{"id":124,"type":"A","host":"www","record":"203.0.113.1"}]`), nil
		case "/dns/add-record.json":
			if q.Get("host") != "_acme-challenge" || q.Get("record") != "new-val" || q.Get("record-type") != "TXT" {
				t.Errorf("CloudNS 加记录参数不对：%v", q)
			}
			return 200, []byte(`{"status":"Success","data":{"id":125}}`), nil
		case "/dns/delete-record.json":
			if q.Get("record-id") == "" {
				t.Errorf("CloudNS 删记录须传 record-id：%v", q)
			}
			return 200, []byte(`{"status":"Success"}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	p := cloudnsProvider{}
	stale, err := p.ListStale(s, "example.com")
	if err != nil {
		t.Fatalf("ListStale 失败：%v", err)
	}
	if len(stale) != 1 || stale[0].ID != "123" {
		t.Fatalf("ListStale 应返回 1 条残留 id=123，实际 %+v", stale)
	}
	id, err := p.Create(s, "example.com", "new-val")
	if err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if id != "125" {
		t.Fatalf("Create 应返回 id 125，实际 %q", id)
	}
	if err := p.Delete(s, "example.com", "125", "new-val"); err != nil {
		t.Fatalf("Delete 失败：%v", err)
	}
	// ListStale 是 findZone（1 次）＋查记录（1 次）；Create 是 findZone
	// （1 次）＋ListStale（2 次）＋删残留（1 次）＋新增（1 次）；
	// Delete 是 findZone（1 次）＋删（1 次）。共 9 次。
	want := []string{
		"GET /dns/list-zones.json",
		"GET /dns/records.json",
		"GET /dns/list-zones.json",
		"GET /dns/list-zones.json",
		"GET /dns/records.json",
		"GET /dns/delete-record.json",
		"GET /dns/add-record.json",
		"GET /dns/list-zones.json",
		"GET /dns/delete-record.json",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(want) {
		t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用顺序不对：\n%v\nwant:\n%v", calls, want)
		}
	}
}

func TestCloudNSError1415(t *testing.T) {
	s := newTestServer144(t)
	s.cfg.CloudNSAuthID = "id-1"
	s.cfg.CloudNSPassword = "pw-1"
	stubDNSHTTP1349(t, func(req *http.Request) (int, []byte, error) {
		switch req.URL.Path {
		case "/dns/list-zones.json":
			return 200, []byte(`[{"name":"example.com"}]`), nil
		case "/dns/records.json":
			return 200, []byte(`[]`), nil
		case "/dns/add-record.json":
			// 错误也回 HTTP 200，须读 body 的 status 字段。
			return 200, []byte(`{"status":"Failed","statusDescription":"Invalid auth"}`), nil
		}
		return 404, []byte(`{}`), nil
	})
	_, err := (cloudnsProvider{}).Create(s, "example.com", "v")
	if err == nil {
		t.Fatal("status=Failed 时 Create 应返回错误")
	}
	if !strings.Contains(err.Error(), "Invalid auth") {
		t.Fatalf("错误应透出 statusDescription，实际：%v", err)
	}
}

// ---------- 14. handler：保存新家凭证＋缺字段 400 ----------

func TestHandleDNSProvider1415(t *testing.T) {
	s := newTestServer144(t)
	h := s.routes()
	cookie := loginCookie144(t, h)
	s.cfg.CloudflareToken = "cfut_oldtoken1415"

	// Spaceship 填齐 → 200，旧 CF Token 不动。
	code, resp := postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"spaceship","api_key":"sp-k","api_secret":"sp-s"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("Spaceship 填齐应 200，实际 %d：%v", code, resp)
	}
	if s.cfg.DNSProvider != "spaceship" || s.cfg.SpaceshipAPIKey != "sp-k" || s.cfg.SpaceshipSecret != "sp-s" || s.cfg.CloudflareToken != "cfut_oldtoken1415" {
		t.Fatalf("保存后状态不对：provider=%q key=%q cf=%q", s.cfg.DNSProvider, s.cfg.SpaceshipAPIKey, s.cfg.CloudflareToken)
	}
	// Porkbun 只填一半 → 400，不切换服务商。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"porkbun","api_key":"pk1_x"}`)
	if code != 400 {
		t.Fatalf("Porkbun 凭证没填齐应 400，实际 %d", code)
	}
	if s.cfg.DNSProvider != "spaceship" || s.cfg.PorkbunAPIKey == "pk1_x" {
		t.Fatal("400 时不应切换服务商、也不应留半截凭证")
	}
	// Porkbun 填齐 → 200。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"porkbun","api_key":"pk1_x","api_secret":"sk1_x"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("Porkbun 填齐应 200，实际 %d：%v", code, resp)
	}
	// CloudNS 只填 auth-id（还没存过完整凭证）→ 400，不切换服务商。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"cloudns","auth_id":"id-2"}`)
	if code != 400 {
		t.Fatalf("CloudNS 凭证没填齐应 400，实际 %d", code)
	}
	if s.cfg.DNSProvider != "porkbun" || s.cfg.CloudNSAuthID == "id-2" {
		t.Fatal("400 时不应切换服务商、也不应留半截凭证")
	}
	// CloudNS 填齐三字段（含子账号）→ 200。
	code, resp = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"cloudns","auth_id":"id-1","auth_password":"pw-1","sub_auth_user":"sub-1"}`)
	if code != 200 || resp["ok"] != true {
		t.Fatalf("CloudNS 填齐应 200，实际 %d：%v", code, resp)
	}
	if s.cfg.CloudNSAuthID != "id-1" || s.cfg.CloudNSSubUser != "sub-1" {
		t.Fatalf("CloudNS 保存不对：id=%q sub=%q", s.cfg.CloudNSAuthID, s.cfg.CloudNSSubUser)
	}
	// 设置页回填：dns_configured 里新家都是 true，未填的 false。
	code, settings := getJSON144(t, h, cookie, "/api/v1/settings")
	if code != 200 {
		t.Fatalf("设置页应 200，实际 %d", code)
	}
	cfgd, ok := settings["dns_configured"].(map[string]any)
	if !ok {
		t.Fatalf("dns_configured 缺失：%v", settings)
	}
	for _, id := range []string{"spaceship", "porkbun", "cloudns"} {
		if cfgd[id] != true {
			t.Fatalf("dns_configured[%q] 应为 true，实际 %v", id, cfgd[id])
		}
	}
	if cfgd["digitalocean"] != false || cfgd["hetzner"] != false {
		t.Fatalf("没填凭证的服务商应为 false：%v", cfgd)
	}
	// 未知服务商 → 400。
	code, _ = postJSON144(t, h, cookie, "/api/v1/settings/dns-provider", `{"provider":"bogus"}`)
	if code != 400 {
		t.Fatalf("未知服务商应 400，实际 %d", code)
	}
}

