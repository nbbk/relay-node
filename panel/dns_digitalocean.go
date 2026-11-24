package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- DigitalOcean（v1.4.15） ----------
//
// API 依据：DigitalOcean 官方 API 文档（Domains / Domain Records）。
// 端点 https://api.digitalocean.com/v2，认证 Authorization: Bearer <Token>。
// zone 即域名本身；创建 POST /v2/domains/{domain}/records
// {type:"TXT", name:"_acme-challenge", data:"<值>", ttl:300}，成功 201；
// 删除按记录 ID：DELETE /v2/domains/{domain}/records/{id}，成功 204。
// 错误是 JSON {"id","message"}。

const digitaloceanAPI = "https://api.digitalocean.com/v2"

// digitaloceanHeaders 返回 DigitalOcean 的请求头（缺 Token 时报清楚去哪填）。
func digitaloceanHeaders(s *Server) (map[string]string, error) {
	token := s.cfg.DigitalOceanToken
	if token == "" {
		return nil, fmt.Errorf("未配置 DigitalOcean 凭证（设置里填 Personal Access Token，权限要含 domain 读写）")
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// digitaloceanErr 把 DigitalOcean 的 JSON 错误拼成带 message 摘要的错误。
func digitaloceanErr(code int, b []byte) error {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return fmt.Errorf("DigitalOcean 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Message)))
	}
	return fmt.Errorf("DigitalOcean 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// digitaloceanFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET /v2/domains/{zone} 返回 200 的第一个就是（不在 DO 名下时 404）。
func digitaloceanFindZone(s *Server, domain string) (zone string, headers map[string]string, err error) {
	headers, err = digitaloceanHeaders(s)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		code, b, err := dnsHTTPGet(digitaloceanAPI+"/domains/"+zone, headers)
		if err != nil {
			continue
		}
		if code == 404 {
			continue
		}
		if code >= 300 {
			return "", nil, digitaloceanErr(code, b)
		}
		return zone, headers, nil
	}
	return "", nil, fmt.Errorf("DigitalOcean 里找不到域名 %s 的解析（请先在 DigitalOcean 控制台添加该域名）", domain)
}

type digitaloceanRecord struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

type digitaloceanProvider struct{}

func (digitaloceanProvider) ID() string { return "digitalocean" }

func (digitaloceanProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, headers, err := digitaloceanFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(digitaloceanAPI+"/domains/"+zone+"/records?per_page=200", headers)
	if err != nil {
		return nil, fmt.Errorf("DigitalOcean 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, digitaloceanErr(code, b)
	}
	var list struct {
		Records []digitaloceanRecord `json:"domain_records"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("DigitalOcean 返回解析失败")
	}
	var out []dnsTXTRecord
	relName := dnsRelativeName(domain, zone)
	for _, r := range list.Records {
		if r.Type != "TXT" || r.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: fmt.Sprintf("%d", r.ID), Value: r.Data})
	}
	return out, nil
}

func (p digitaloceanProvider) Create(s *Server, domain, value string) (string, error) {
	zone, headers, err := digitaloceanFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：上次签发留下的同名 TXT 不删，再建会与旧值并存（验证虽能过，
	// 但 zone 会变脏）；单条 DELETE，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _, _ = dnsHTTPDelete(digitaloceanAPI+"/domains/"+zone+"/records/"+r.ID, headers)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "TXT", "name": dnsRelativeName(domain, zone), "data": value, "ttl": 300,
	})
	code, b, err := dnsHTTPPost(digitaloceanAPI+"/domains/"+zone+"/records", payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("DigitalOcean 写 TXT 记录失败：%v。%s", err, providerManualHint("digitalocean", domain))
	}
	if code != 201 {
		return "", fmt.Errorf("DigitalOcean 写 TXT 记录失败：%v。%s", digitaloceanErr(code, b), providerManualHint("digitalocean", domain))
	}
	var rec struct {
		Record digitaloceanRecord `json:"domain_record"`
	}
	if json.Unmarshal(b, &rec) == nil && rec.Record.ID != 0 {
		return fmt.Sprintf("%d", rec.Record.ID), nil
	}
	return "", fmt.Errorf("DigitalOcean 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("digitalocean", domain))
}

func (digitaloceanProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, headers, err := digitaloceanFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(digitaloceanAPI+"/domains/"+zone+"/records/"+id, headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code != 204 && code >= 300 {
		return digitaloceanErr(code, b)
	}
	return nil
}
