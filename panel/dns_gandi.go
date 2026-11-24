package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Gandi LiveDNS（v1.4.15） ----------
//
// API 依据：Gandi 官方文档（api.gandi.net/docs/livedns）。
// 端点 https://api.gandi.net/v5/livedns，认证 Authorization: Bearer
// <Personal Access Token>（旧 Apikey 已废弃）。zone 即域名本身。
// RRSet 模型：创建 POST /v5/livedns/domains/{fqdn}/records/{name}/TXT
// {rrset_values:["\"<值>\""], rrset_ttl:300}，成功 201、同名已存在 409；
// 更新 PUT 同路径会覆盖整条 RRSet；删除是 DELETE 整条 RRSet，成功 204。
// TXT 值须包双引号（官方示例如此）。前提：域名必须用 LiveDNS 域名
// 服务器（NS 指到 Gandi LiveDNS，否则接口写了也不生效）。
// 限速 1000 请求/分钟/同一 IP。令牌有有效期（7 天–1 年），须定期轮换。

const gandiAPI = "https://api.gandi.net/v5/livedns"

// gandiHeaders 返回 Gandi 的请求头（缺 Token 时报清楚去哪填）。
func gandiHeaders(s *Server) (map[string]string, error) {
	token := s.cfg.GandiToken
	if token == "" {
		return nil, fmt.Errorf("未配置 Gandi 凭证（设置里填 Personal Access Token，组织 → Sharing 生成，须勾“Manage domain name technical configurations”）")
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

// gandiErr 把 Gandi 的 JSON 错误拼成带 message 摘要的错误。
func gandiErr(code int, b []byte) error {
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(b, &e) == nil && (e.Message != "" || e.Code != "") {
		return fmt.Errorf("Gandi 报错（HTTP %d，%s）：%s", code, e.Code, truncateProviderBody([]byte(e.Message)))
	}
	return fmt.Errorf("Gandi 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// gandiFindZone 找域名所在的 zone：从完整域名逐级往上试，
// GET /v5/livedns/domains/{zone} 返回 200 的第一个就是。
func gandiFindZone(s *Server, domain string) (zone string, headers map[string]string, err error) {
	headers, err = gandiHeaders(s)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		code, b, err := dnsHTTPGet(gandiAPI+"/domains/"+zone, headers)
		if err != nil {
			continue
		}
		if code == 404 {
			continue
		}
		if code >= 300 {
			return "", nil, gandiErr(code, b)
		}
		return zone, headers, nil
	}
	return "", nil, fmt.Errorf("Gandi 里找不到域名 %s（请先确认该域名在 Gandi 且用 LiveDNS 域名服务器）", domain)
}

type gandiRRSet struct {
	Name   string   `json:"rrset_name"`
	Type   string   `json:"rrset_type"`
	TTL    int      `json:"rrset_ttl"`
	Values []string `json:"rrset_values"`
}

// gandiGetRRSet 读 _acme-challenge 的 TXT RRSet（不存在返回 nil, nil）。
func gandiGetRRSet(s *Server, zone, relName string, headers map[string]string) (*gandiRRSet, error) {
	code, b, err := dnsHTTPGet(gandiAPI+"/domains/"+zone+"/records/"+relName+"/TXT", headers)
	if err != nil {
		return nil, fmt.Errorf("Gandi 查 TXT 记录失败：%v", err)
	}
	if code == 404 {
		return nil, nil
	}
	if code >= 300 {
		return nil, gandiErr(code, b)
	}
	var sets []gandiRRSet
	if json.Unmarshal(b, &sets) != nil {
		return nil, fmt.Errorf("Gandi 返回解析失败")
	}
	for _, r := range sets {
		if r.Name == relName && r.Type == "TXT" {
			return &r, nil
		}
	}
	return nil, nil
}

type gandiProvider struct{}

func (gandiProvider) ID() string { return "gandi" }

func (gandiProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, headers, err := gandiFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	rr, err := gandiGetRRSet(s, zone, dnsRelativeName(domain, zone), headers)
	if err != nil {
		return nil, err
	}
	if rr == nil {
		return nil, nil
	}
	var out []dnsTXTRecord
	for _, v := range rr.Values {
		// Gandi 的 RRSet 值是带双引号的（"token"），面板内部用去引号后的值。
		out = append(out, dnsTXTRecord{Value: strings.Trim(v, "\"")})
	}
	return out, nil
}

func (p gandiProvider) Create(s *Server, domain, value string) (string, error) {
	zone, headers, err := gandiFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// RRSet 模型：同名已存在时 POST 会 409，所以先 DELETE 整条 RRSet
	// 再 POST（_acme-challenge 名下只有 ACME 的值，不会动到别人的记录）。
	relName := dnsRelativeName(domain, zone)
	_, _, _ = dnsHTTPDelete(gandiAPI+"/domains/"+zone+"/records/"+relName+"/TXT", headers)
	// TXT 值须包双引号（官方示例如此）。
	payload, _ := json.Marshal(map[string]any{
		"rrset_values": []string{"\"" + value + "\""},
		"rrset_ttl":    300,
	})
	code, b, err := dnsHTTPPost(gandiAPI+"/domains/"+zone+"/records/"+relName+"/TXT", payload, "application/json", headers)
	if err != nil {
		return "", fmt.Errorf("Gandi 写 TXT 记录失败：%v。%s", err, providerManualHint("gandi", domain))
	}
	if code != 201 {
		return "", fmt.Errorf("Gandi 写 TXT 记录失败：%v。%s", gandiErr(code, b), providerManualHint("gandi", domain))
	}
	return "", nil // RRSet 模型没有单条记录 id，删时按名删整条 RRSet
}

func (gandiProvider) Delete(s *Server, domain, id, value string) error {
	zone, headers, err := gandiFindZone(s, domain)
	if err != nil {
		return err
	}
	code, b, err := dnsHTTPDelete(gandiAPI+"/domains/"+zone+"/records/"+dnsRelativeName(domain, zone)+"/TXT", headers)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code != 204 && code >= 300 {
		return gandiErr(code, b)
	}
	return nil
}
