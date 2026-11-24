package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Bunny DNS（v1.4.15） ----------
//
// API 依据：Bunny.net API 官方文档（DNS Zone / DNS Records）。
// 端点 https://api.bunny.net，认证 AccessKey 头（账号 API Key）。
// zone 用数字 id：先 GET /dnszone 列表取 id；创建/更新是
// PUT /dnszone/{zoneId}/records {Type:3, Name:"_acme-challenge",
// Value:"<值>", Ttl:60}（Type 是整数枚举，TXT=3），成功 200/201；
// 删除按记录 ID：DELETE /dnszone/{zoneId}/records/{recordId}，成功 200。
// 错误是 JSON {"Message":"..."}。

const bunnyAPI = "https://api.bunny.net"

// bunnyHeaders 返回 Bunny 的请求头（缺 Key 时报清楚去哪填）。
func bunnyHeaders(s *Server) (map[string]string, error) {
	key := s.cfg.BunnyAPIKey
	if key == "" {
		return nil, fmt.Errorf("未配置 Bunny DNS 凭证（设置里填账号 API Key，控制台 Account → API Key 生成）")
	}
	return map[string]string{"AccessKey": key}, nil
}

// bunnyErr 把 Bunny 的 JSON 错误拼成带 Message 摘要的错误。
func bunnyErr(code int, b []byte) error {
	var e struct {
		Message string `json:"Message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return fmt.Errorf("Bunny DNS 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Message)))
	}
	return fmt.Errorf("Bunny DNS 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// bunnyFindZone 找域名所在的 zone：GET /dnszone 一次取回后按最长后缀匹配。
// 返回 zone 的数字 id 与域名（相对主机头要用 zone 名算）。
func bunnyFindZone(s *Server, domain string) (zoneID int64, zoneName string, headers map[string]string, err error) {
	headers, err = bunnyHeaders(s)
	if err != nil {
		return 0, "", nil, err
	}
	code, b, err := dnsHTTPGet(bunnyAPI+"/dnszone", headers)
	if err != nil {
		return 0, "", nil, fmt.Errorf("Bunny DNS 查域名列表失败：%v", err)
	}
	if code >= 300 {
		return 0, "", nil, bunnyErr(code, b)
	}
	var list []struct {
		ID     int64  `json:"Id"`
		Domain string `json:"Domain"`
	}
	if json.Unmarshal(b, &list) != nil {
		return 0, "", nil, fmt.Errorf("Bunny DNS 返回解析失败")
	}
	best := ""
	for _, d := range list {
		if domain == d.Domain || strings.HasSuffix(domain, "."+d.Domain) {
			if len(d.Domain) > len(best) {
				best = d.Domain
				zoneID = d.ID
			}
		}
	}
	if best == "" {
		return 0, "", nil, fmt.Errorf("Bunny DNS 里找不到域名 %s 的解析（请先在 Bunny 控制台添加该域名）", domain)
	}
	return zoneID, best, headers, nil
}

type bunnyRecord struct {
	ID    int64  `json:"Id"`
	Type  int    `json:"Type"`
	Name  string `json:"Name"`
	Value string `json:"Value"`
	TTL   int    `json:"Ttl"`
}

type bunnyProvider struct{}

func (bunnyProvider) ID() string { return "bunny" }

func (bunnyProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zoneID, zoneName, headers, err := bunnyFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(fmt.Sprintf("%s/dnszone/%d/records", bunnyAPI, zoneID), headers)
	if err != nil {
		return nil, fmt.Errorf("Bunny DNS 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, bunnyErr(code, b)
	}
	var list []bunnyRecord
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Bunny DNS 返回解析失败")
	}
	relName := dnsRelativeName(domain, zoneName)
	var out []dnsTXTRecord
	for _, r := range list {
		// Type 是整数枚举，TXT=3。
		if r.Type != 3 || r.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: fmt.Sprintf("%d", r.ID), Value: r.Value})
	}
	return out, nil
}

func (p bunnyProvider) Create(s *Server, domain, value string) (string, error) {
	zoneID, zoneName, headers, err := bunnyFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 DELETE，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _, _ = dnsHTTPDelete(fmt.Sprintf("%s/dnszone/%d/records/%s", bunnyAPI, zoneID, r.ID), headers)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"Type": 3, "Name": dnsRelativeName(domain, zoneName), "Value": value, "Ttl": 60,
	})
	code, b, err := dnsHTTPPut(fmt.Sprintf("%s/dnszone/%d/records", bunnyAPI, zoneID), payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("Bunny DNS 写 TXT 记录失败：%v。%s", err, providerManualHint("bunny", domain))
	}
	if code != 200 && code != 201 {
		return "", fmt.Errorf("Bunny DNS 写 TXT 记录失败：%v。%s", bunnyErr(code, b), providerManualHint("bunny", domain))
	}
	var rec bunnyRecord
	if json.Unmarshal(b, &rec) == nil && rec.ID != 0 {
		return fmt.Sprintf("%d", rec.ID), nil
	}
	return "", fmt.Errorf("Bunny DNS 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("bunny", domain))
}

func (bunnyProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zoneID, _, headers, err := bunnyFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(fmt.Sprintf("%s/dnszone/%d/records/%s", bunnyAPI, zoneID, id), headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code >= 300 {
		return bunnyErr(code, b)
	}
	return nil
}
