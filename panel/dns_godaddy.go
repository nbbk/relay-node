package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- GoDaddy v3（v1.3.49） ----------
//
// API 依据：GoDaddy 开发者文档《About Authentication》《How to manage
// DNS records》（更新于 2026-11-16）。
// 重要：v1 的 sso-key（Authorization: sso-key <key>:<secret>）已被 GoDaddy
// 官方列为“计划废弃（through 2026）”，Domains v3 于 2026-08-10 发布，
// 官方称新集成应用 PAT（Personal Access Token）。所以这里用 v3，不用 v1。
// v3：Authorization: Bearer <PAT>，PAT 须带 domains.domain:read 与
// domains.dns:update 两个权限；单条记录 CRUD，没有 v1 那种“PUT 整组同名
// 记录”的危险——先 GET 查、再 POST 建、按 recordId DELETE，
// 不会一把冲掉别人的记录。
// GET /v3/domains/zones/{zone}/dns-records?type=TXT&name={相对名}
// POST /v3/domains/zones/{zone}/dns-records {name,type:"TXT",data,ttl}
// DELETE /v3/domains/zones/{zone}/dns-records/{recordId}
// TTL 最小 600、最大 86400，这里用 600。限速约 60 请求/分钟/凭证。
// 错误是 JSON {"code","message"}。

const godaddyAPI = "https://api.godaddy.com"

// godaddyHeaders 返回 GoDaddy v3 的请求头（缺 PAT 时报清楚去哪填）。
func godaddyHeaders(s *Server) (map[string]string, error) {
	pat := s.cfg.GoDaddyPAT
	if pat == "" {
		return nil, fmt.Errorf("未配置 GoDaddy 凭证（设置里填 Personal Access Token，需 domains.domain:read 与 domains.dns:update 权限）")
	}
	return map[string]string{"Authorization": "Bearer " + pat}, nil
}

// godaddyErr 把 GoDaddy 的 JSON 错误拼成带 code 与 message 摘要的错误。
func godaddyErr(code int, b []byte) error {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Code != "" || e.Message != "") {
		return fmt.Errorf("GoDaddy 报错（HTTP %d，%s）：%s", code, e.Code, truncateProviderBody([]byte(e.Message)))
	}
	return fmt.Errorf("GoDaddy 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// godaddyFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET 列表返回 200 的第一个就是（zone 不在 GoDaddy 名下时返回 404）。
func godaddyFindZone(s *Server, domain string) (zone, relName string, headers map[string]string, err error) {
	headers, err = godaddyHeaders(s)
	if err != nil {
		return "", "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		relName = strings.TrimSuffix("_acme-challenge."+domain, "."+zone)
		code, b, err := dnsHTTPGet(godaddyAPI+"/v3/domains/zones/"+zone+"/dns-records?type=TXT&name="+relName, headers)
		if err != nil {
			continue
		}
		if code == 404 {
			continue
		}
		if code >= 300 {
			return "", "", nil, godaddyErr(code, b)
		}
		return zone, relName, headers, nil
	}
	return "", "", nil, fmt.Errorf("GoDaddy 里找不到域名 %s 的 Zone（请先把该域名转到 GoDaddy 的 DNS 托管）", domain)
}

type godaddyRecord struct {
	RecordID int    `json:"recordId"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Data     string `json:"data"`
	TTL      int    `json:"ttl"`
}

type godaddyProvider struct{}

func (godaddyProvider) ID() string { return "godaddy" }

func (godaddyProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, relName, headers, err := godaddyFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := dnsHTTPGet(godaddyAPI+"/v3/domains/zones/"+zone+"/dns-records?type=TXT&name="+relName, headers)
	if err != nil {
		return nil, fmt.Errorf("GoDaddy 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, godaddyErr(code, b)
	}
	var list struct {
		Items []godaddyRecord `json:"items"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("GoDaddy 返回解析失败")
	}
	var out []dnsTXTRecord
	for _, r := range list.Items {
		if r.Type != "TXT" || r.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: fmt.Sprintf("%d", r.RecordID), Value: r.Data})
	}
	return out, nil
}

func (p godaddyProvider) Create(s *Server, domain, value string) (string, error) {
	zone, relName, headers, err := godaddyFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留（v1.3.48 的做法通用化）：单条 DELETE，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _, _ = dnsHTTPDelete(godaddyAPI+"/v3/domains/zones/"+zone+"/dns-records/"+r.ID, headers)
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"name": relName, "type": "TXT", "data": value, "ttl": 600,
	})
	code, b, err := dnsHTTPPost(godaddyAPI+"/v3/domains/zones/"+zone+"/dns-records", payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("GoDaddy 写 TXT 记录失败：%v。%s", err, providerManualHint("godaddy", domain))
	}
	if code != 201 {
		return "", fmt.Errorf("GoDaddy 写 TXT 记录失败：%v。%s", godaddyErr(code, b), providerManualHint("godaddy", domain))
	}
	var rec godaddyRecord
	if json.Unmarshal(b, &rec) == nil && rec.RecordID != 0 {
		return fmt.Sprintf("%d", rec.RecordID), nil
	}
	return "", fmt.Errorf("GoDaddy 写 TXT 记录失败：返回里没有 recordId。%s", providerManualHint("godaddy", domain))
}

func (godaddyProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, _, headers, err := godaddyFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(godaddyAPI+"/v3/domains/zones/"+zone+"/dns-records/"+id, headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code >= 300 {
		return godaddyErr(code, b)
	}
	return nil
}
