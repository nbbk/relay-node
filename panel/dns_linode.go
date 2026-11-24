package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Linode（v1.4.15） ----------
//
// API 依据：Linode API v4 官方文档（Domains / Domain Records）。
// 端点 https://api.linode.com/v4，认证 Authorization: Bearer <Token>，
// scope 须含 domains:read_write。zone 用数字 id：先 GET /v4/domains
// 列表取 id；创建 POST /v4/domains/{id}/records
// {name:"_acme-challenge", type:"TXT", target:"<值>", ttl_sec:300}，
// 成功 200；删除按记录 ID：DELETE /v4/domains/{id}/records/{record_id}，
// 成功 200。错误是 JSON {"errors":[{"reason":"..."}]}。

const linodeAPI = "https://api.linode.com/v4"

// linodeHeaders 返回 Linode 的请求头（缺 Token 时报清楚去哪填）。
func linodeHeaders(s *Server) (map[string]string, error) {
	token := s.cfg.LinodeToken
	if token == "" {
		return nil, fmt.Errorf("未配置 Linode 凭证（设置里填 Personal Access Token，scope 要含 domains:read_write）")
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// linodeErr 把 Linode 的 JSON 错误拼成带 reason 摘要的错误。
func linodeErr(code int, b []byte) error {
	var e struct {
		Errors []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if json.Unmarshal(b, &e) == nil && len(e.Errors) > 0 && e.Errors[0].Reason != "" {
		return fmt.Errorf("Linode 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Errors[0].Reason)))
	}
	return fmt.Errorf("Linode 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// linodeFindZone 找域名所在的 zone：GET /v4/domains 一次取回后按最长后缀匹配。
// 返回 zone 的数字 id 与域名（相对主机头要用 zone 名算）。
func linodeFindZone(s *Server, domain string) (zoneID int, zoneName string, headers map[string]string, err error) {
	headers, err = linodeHeaders(s)
	if err != nil {
		return 0, "", nil, err
	}
	code, b, err := dnsHTTPGet(linodeAPI+"/domains?page_size=500", headers)
	if err != nil {
		return 0, "", nil, fmt.Errorf("Linode 查域名列表失败：%v", err)
	}
	if code >= 300 {
		return 0, "", nil, linodeErr(code, b)
	}
	var list struct {
		Data []struct {
			ID     int    `json:"id"`
			Domain string `json:"domain"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &list) != nil {
		return 0, "", nil, fmt.Errorf("Linode 返回解析失败")
	}
	best := ""
	for _, d := range list.Data {
		if domain == d.Domain || strings.HasSuffix(domain, "."+d.Domain) {
			if len(d.Domain) > len(best) {
				best = d.Domain
				zoneID = d.ID
			}
		}
	}
	if best == "" {
		return 0, "", nil, fmt.Errorf("Linode 里找不到域名 %s 的解析（请先在 Linode 控制台添加该域名）", domain)
	}
	return zoneID, best, headers, nil
}

type linodeRecord struct {
	ID     int    `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Target string `json:"target"`
	TTL    int    `json:"ttl_sec"`
}

type linodeProvider struct{}

func (linodeProvider) ID() string { return "linode" }

func (linodeProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zoneID, zoneName, headers, err := linodeFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(fmt.Sprintf("%s/domains/%d/records?page_size=500", linodeAPI, zoneID), headers)
	if err != nil {
		return nil, fmt.Errorf("Linode 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, linodeErr(code, b)
	}
	var list struct {
		Data []linodeRecord `json:"data"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Linode 返回解析失败")
	}
	relName := dnsRelativeName(domain, zoneName)
	var out []dnsTXTRecord
	for _, r := range list.Data {
		if r.Type != "TXT" || r.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: fmt.Sprintf("%d", r.ID), Value: r.Target})
	}
	return out, nil
}

func (p linodeProvider) Create(s *Server, domain, value string) (string, error) {
	zoneID, zoneName, headers, err := linodeFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 DELETE，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _, _ = dnsHTTPDelete(fmt.Sprintf("%s/domains/%d/records/%s", linodeAPI, zoneID, r.ID), headers)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"name": dnsRelativeName(domain, zoneName), "type": "TXT", "target": value, "ttl_sec": 300,
	})
	code, b, err := dnsHTTPPost(fmt.Sprintf("%s/domains/%d/records", linodeAPI, zoneID), payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("Linode 写 TXT 记录失败：%v。%s", err, providerManualHint("linode", domain))
	}
	if code != 200 {
		return "", fmt.Errorf("Linode 写 TXT 记录失败：%v。%s", linodeErr(code, b), providerManualHint("linode", domain))
	}
	var rec linodeRecord
	if json.Unmarshal(b, &rec) == nil && rec.ID != 0 {
		return fmt.Sprintf("%d", rec.ID), nil
	}
	return "", fmt.Errorf("Linode 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("linode", domain))
}

func (linodeProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zoneID, _, headers, err := linodeFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(fmt.Sprintf("%s/domains/%d/records/%s", linodeAPI, zoneID, id), headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code >= 300 {
		return linodeErr(code, b)
	}
	return nil
}
