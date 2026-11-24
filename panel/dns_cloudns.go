package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---------- CloudNS（v1.4.15，第一批里最难的一家） ----------
//
// API 依据：ClouDNS API 官方文档（api.cloudns.net/dns）。
// 端点 https://api.cloudns.net/dns/，GET 式 API：认证与业务参数都放
// 查询串。主账号用 auth-id + auth-password；子账号用 sub-auth-user
// （或 sub-auth-id）+ auth-password。zone 即域名本身；创建
// add-record.json?domain-name=<域名>&host=_acme-challenge&record=<值>
// &record-type=TXT&ttl=60；查 records.json；删除按记录 ID：
// delete-record.json?domain-name=<域名>&record-id=<id>。
// ⚠️ 错误也回 HTTP 200，须读 body 的 status 字段判成功（status
// 不是 "Success" 就是失败），不能只看状态码。另注意：账号可能有
// IP 白名单，面板服务器 IP 一变 API 就断。

const cloudnsAPI = "https://api.cloudns.net/dns/"

// cloudnsAuthParams 取 CloudNS 的认证查询参数（缺了就报清楚去哪填）。
// 子账号填了 sub-auth-user 就用子账号方式，否则用主账号 auth-id。
func cloudnsAuthParams(s *Server) (url.Values, error) {
	q := url.Values{}
	if sub := strings.TrimSpace(s.cfg.CloudNSSubUser); sub != "" {
		if s.cfg.CloudNSPassword == "" {
			return nil, fmt.Errorf("未配置 CloudNS 凭证（设置里填 Auth Password；子账号还要填 Sub-auth user）")
		}
		q.Set("sub-auth-user", sub)
	} else {
		if s.cfg.CloudNSAuthID == "" || s.cfg.CloudNSPassword == "" {
			return nil, fmt.Errorf("未配置 CloudNS 凭证（设置里填 Auth ID 与 Auth Password，控制台生成）")
		}
		q.Set("auth-id", s.cfg.CloudNSAuthID)
	}
	q.Set("auth-password", s.cfg.CloudNSPassword)
	return q, nil
}

// cloudnsCall 调一次 CloudNS API（GET，参数放查询串），返回响应体；
// status 不是 "Success" 就报错（带 statusDescription 摘要）。
func cloudnsCall(s *Server, op string, params map[string]string) ([]byte, error) {
	q, err := cloudnsAuthParams(s)
	if err != nil {
		return nil, err
	}
	for k, v := range params {
		q.Set(k, v)
	}
	code, b, err := dnsHTTPGet(cloudnsAPI+op+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("CloudNS 请求失败：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("CloudNS 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var status struct {
		Status            string `json:"status"`
		StatusDescription string `json:"statusDescription"`
	}
	if json.Unmarshal(b, &status) == nil && status.Status != "" && status.Status != "Success" {
		err := fmt.Errorf("CloudNS 报错：%s", truncateProviderBody([]byte(status.StatusDescription)))
		if strings.Contains(strings.ToLower(status.StatusDescription), "ip") {
			return nil, fmt.Errorf("%v。CloudNS 账号可能开了 IP 白名单：面板服务器 IP 不在白名单里时 API 直接拒绝，请去 CloudNS 后台把本机公网 IP 加进白名单", err)
		}
		return nil, err
	}
	return b, nil
}

// cloudnsFindZone 找域名所在的 zone：GET list-zones.json 一次取回后
// 按最长后缀匹配。
func cloudnsFindZone(s *Server, domain string) (string, error) {
	b, err := cloudnsCall(s, "list-zones.json", map[string]string{"page": "1", "rows-per-page": "100"})
	if err != nil {
		return "", err
	}
	var list []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &list) != nil {
		return "", fmt.Errorf("CloudNS 返回解析失败")
	}
	best := ""
	for _, z := range list {
		if domain == z.Name || strings.HasSuffix(domain, "."+z.Name) {
			if len(z.Name) > len(best) {
				best = z.Name
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("CloudNS 里找不到域名 %s 的解析（请先在 CloudNS 控制台添加该域名）", domain)
	}
	return best, nil
}

type cloudnsRecord struct {
	ID     json.Number `json:"id"` // CloudNS 返回数字 id，用 json.Number 接再转字符串。
	Type   string      `json:"type"`
	Host   string      `json:"host"`
	Record string      `json:"record"`
	TTL    string      `json:"ttl"`
}

type cloudnsProvider struct{}

func (cloudnsProvider) ID() string { return "cloudns" }

func (cloudnsProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, err := cloudnsFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	b, err := cloudnsCall(s, "records.json", map[string]string{
		"domain-name": zone, "host": dnsRelativeName(domain, zone), "type": "TXT",
	})
	if err != nil {
		return nil, err
	}
	var list []cloudnsRecord
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("CloudNS 返回解析失败")
	}
	relName := dnsRelativeName(domain, zone)
	var out []dnsTXTRecord
	for _, r := range list {
		if r.Type != "TXT" || r.Host != relName {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.ID.String(), Value: r.Record})
	}
	return out, nil
}

func (p cloudnsProvider) Create(s *Server, domain, value string) (string, error) {
	zone, err := cloudnsFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 delete-record，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _ = cloudnsCall(s, "delete-record.json", map[string]string{"domain-name": zone, "record-id": r.ID})
		}
	}
	b, err := cloudnsCall(s, "add-record.json", map[string]string{
		"domain-name": zone, "host": dnsRelativeName(domain, zone), "record": value, "record-type": "TXT", "ttl": "60",
	})
	if err != nil {
		return "", fmt.Errorf("CloudNS 写 TXT 记录失败：%v。%s", err, providerManualHint("cloudns", domain))
	}
	var resp struct {
		Data struct {
			ID json.Number `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &resp) == nil && resp.Data.ID != "" {
		return resp.Data.ID.String(), nil
	}
	return "", fmt.Errorf("CloudNS 写 TXT 记录失败：返回里没有记录 ID。%s", providerManualHint("cloudns", domain))
}

func (cloudnsProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, err := cloudnsFindZone(s, domain)
	if err != nil {
		return err
	}
	_, err = cloudnsCall(s, "delete-record.json", map[string]string{"domain-name": zone, "record-id": id})
	return err
}
