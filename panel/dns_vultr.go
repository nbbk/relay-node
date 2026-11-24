package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Vultr（v1.4.15） ----------
//
// API 依据：Vultr API v2 官方文档（DNS / Domain Records）。
// 端点 https://api.vultr.com/v2，认证 Authorization: Bearer <API Key>。
// zone 即域名本身；创建 POST /v2/domains/{domain}/records
// {name:"_acme-challenge", type:"TXT", data:"\"<值>\"", ttl:300, priority:0}，
// 成功 201；删除按记录 ID：DELETE /v2/domains/{domain}/records/{id}，
// 成功 204。注意：TXT 值须包双引号（官方示例如此），data 传 "\"<值>\""。
// 错误是 JSON {"error":"..."}。

const vultrAPI = "https://api.vultr.com/v2"

// vultrHeaders 返回 Vultr 的请求头（缺 Key 时报清楚去哪填）。
func vultrHeaders(s *Server) (map[string]string, error) {
	key := s.cfg.VultrAPIKey
	if key == "" {
		return nil, fmt.Errorf("未配置 Vultr 凭证（设置里填 API Key）")
	}
	return map[string]string{"Authorization": "Bearer " + key}, nil
}

// vultrErr 把 Vultr 的 JSON 错误拼成带 error 摘要的错误。
func vultrErr(code int, b []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return fmt.Errorf("Vultr 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Error)))
	}
	return fmt.Errorf("Vultr 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// vultrFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET /v2/domains/{zone} 返回 200 的第一个就是（不在 Vultr 名下时 404）。
func vultrFindZone(s *Server, domain string) (zone string, headers map[string]string, err error) {
	headers, err = vultrHeaders(s)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		code, b, err := dnsHTTPGet(vultrAPI+"/domains/"+zone, headers)
		if err != nil {
			continue
		}
		if code == 404 {
			continue
		}
		if code >= 300 {
			return "", nil, vultrErr(code, b)
		}
		return zone, headers, nil
	}
	return "", nil, fmt.Errorf("Vultr 里找不到域名 %s 的解析（请先在 Vultr 控制台添加该域名）", domain)
}

type vultrRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
}

type vultrProvider struct{}

func (vultrProvider) ID() string { return "vultr" }

func (vultrProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, headers, err := vultrFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(vultrAPI+"/domains/"+zone+"/records?per_page=500", headers)
	if err != nil {
		return nil, fmt.Errorf("Vultr 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, vultrErr(code, b)
	}
	var list struct {
		Records []vultrRecord `json:"records"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Vultr 返回解析失败")
	}
	relName := dnsRelativeName(domain, zone)
	var out []dnsTXTRecord
	for _, r := range list.Records {
		if r.Type != "TXT" || r.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.ID, Value: r.Data})
	}
	return out, nil
}

func (p vultrProvider) Create(s *Server, domain, value string) (string, error) {
	zone, headers, err := vultrFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 DELETE，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _, _ = dnsHTTPDelete(vultrAPI+"/domains/"+zone+"/records/"+r.ID, headers)
		}
	}
	// TXT 值须包双引号（官方示例如此）；删时按 ID 删，值里的引号不影响。
	payload, _ := json.Marshal(map[string]any{
		"name": dnsRelativeName(domain, zone), "type": "TXT", "data": "\"" + value + "\"", "ttl": 300, "priority": 0,
	})
	code, b, err := dnsHTTPPost(vultrAPI+"/domains/"+zone+"/records", payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("Vultr 写 TXT 记录失败：%v。%s", err, providerManualHint("vultr", domain))
	}
	if code != 201 {
		return "", fmt.Errorf("Vultr 写 TXT 记录失败：%v。%s", vultrErr(code, b), providerManualHint("vultr", domain))
	}
	var rec struct {
		Record vultrRecord `json:"record"`
	}
	if json.Unmarshal(b, &rec) == nil && rec.Record.ID != "" {
		return rec.Record.ID, nil
	}
	return "", fmt.Errorf("Vultr 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("vultr", domain))
}

func (vultrProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, headers, err := vultrFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(vultrAPI+"/domains/"+zone+"/records/"+id, headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code != 204 && code >= 300 {
		return vultrErr(code, b)
	}
	return nil
}
