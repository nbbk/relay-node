package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---------- Porkbun（v1.4.15） ----------
//
// API 依据：Porkbun API v3 官方文档（api.porkbun.com/api/json/v3）。
// 端点 https://api.porkbun.com/api/json/v3，认证是 API Key（pk1_ 前缀）
// ＋ Secret（sk1_ 前缀），放 JSON body 的 apikey / secretapikey。
// zone 即域名本身；创建 POST /dns/create/{domain}
// {name:"_acme-challenge", type:"TXT", content:"<值>", ttl:600}，
// 成功返回 {status:"SUCCESS", id:"..."}；查 POST /dns/retrieve/{domain}；
// 删除按记录 ID：POST /dns/delete/{domain}/{id}。
// ⚠️ 每个域名须先在后台打开 API Access，否则报 API_ACCESS_DISABLED。
// TTL 最小 600。限速每 key 20 请求/2 秒。

const porkbunAPI = "https://api.porkbun.com/api/json/v3"

// porkbunCreds 取 Porkbun 凭证（缺了就报清楚去哪填）。
func porkbunCreds(s *Server) (key, secret string, err error) {
	key = s.cfg.PorkbunAPIKey
	secret = s.cfg.PorkbunSecret
	if key == "" || secret == "" {
		return "", "", fmt.Errorf("未配置 Porkbun 凭证（设置里填 API Key 与 Secret，Account → API Access 生成）")
	}
	return key, secret, nil
}

// porkbunCall 调一次 Porkbun API（POST JSON，凭证放 body），
// 返回响应体；status 不是 SUCCESS 就报错（带 message 摘要）。
func porkbunCall(s *Server, path string, extra map[string]any) ([]byte, error) {
	key, secret, err := porkbunCreds(s)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"apikey": key, "secretapikey": secret}
	for k, v := range extra {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	code, b, err := dnsHTTPPost(porkbunAPI+path, payload, "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("Porkbun 请求失败：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("Porkbun 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var resp struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("Porkbun 返回解析失败")
	}
	if resp.Status != "SUCCESS" {
		err := fmt.Errorf("Porkbun 报错：%s", truncateProviderBody([]byte(resp.Message)))
		// 逐域名开关没开是最常见的拦路项，报错里直接点出来。
		if strings.Contains(resp.Message, "API_ACCESS_DISABLED") {
			return nil, fmt.Errorf("%v。域名须先在 Porkbun 后台打开 API Access（域名管理 → API Access），否则面板写不了记录", err)
		}
		return nil, err
	}
	return b, nil
}

// porkbunFindZone 找域名所在的 zone：从完整域名逐级往上试，
// POST /dns/retrieve/{zone} 成功（status SUCCESS）的第一个就是。
func porkbunFindZone(s *Server, domain string) (string, error) {
	if _, _, err := porkbunCreds(s); err != nil {
		return "", err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone := strings.Join(parts[i:], ".")
		if _, err := porkbunCall(s, "/dns/retrieve/"+zone, nil); err != nil {
			// 开关没开、凭证错直接报出来，不用继续试；域名不在名下就试更短的后缀。
			if strings.Contains(err.Error(), "未配置") || strings.Contains(err.Error(), "API_ACCESS_DISABLED") {
				return "", err
			}
			continue
		}
		return zone, nil
	}
	return "", fmt.Errorf("Porkbun 里找不到域名 %s 的解析（请先确认该域名在 Porkbun 注册且已打开 API Access）", domain)
}

type porkbunRecord struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     string `json:"ttl"`
}

type porkbunProvider struct{}

func (porkbunProvider) ID() string { return "porkbun" }

func (porkbunProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, err := porkbunFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	b, err := porkbunCall(s, "/dns/retrieve/"+zone, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Records []porkbunRecord `json:"records"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("Porkbun 返回解析失败")
	}
	relName := strings.TrimSuffix(acmeChallengeName(domain), "."+zone)
	var out []dnsTXTRecord
	for _, r := range resp.Records {
		if r.Type != "TXT" || (r.Name != relName && r.Name != acmeChallengeName(domain)) {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.ID, Value: r.Content})
	}
	return out, nil
}

func (p porkbunProvider) Create(s *Server, domain, value string) (string, error) {
	zone, err := porkbunFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 delete，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _ = porkbunCall(s, "/dns/delete/"+zone+"/"+r.ID, nil)
		}
	}
	b, err := porkbunCall(s, "/dns/create/"+zone, map[string]any{
		"name": dnsRelativeName(domain, zone), "type": "TXT", "content": value, "ttl": "600",
	})
	if err != nil {
		return "", fmt.Errorf("Porkbun 写 TXT 记录失败：%v。%s", err, providerManualHint("porkbun", domain))
	}
	var resp struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(b, &resp) == nil && resp.ID != "" {
		return resp.ID, nil
	}
	return "", fmt.Errorf("Porkbun 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("porkbun", domain))
}

func (porkbunProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, err := porkbunFindZone(s, domain)
	if err != nil {
		return err
	}
	_, err = porkbunCall(s, "/dns/delete/"+zone+"/"+id, nil)
	return err
}
