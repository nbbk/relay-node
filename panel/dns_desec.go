package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---------- deSEC（v1.4.15） ----------
//
// API 依据：deSEC 官方 API 文档（desec.io/api/v1）。
// 端点 https://desec.io/api/v1，认证 Authorization: Token <Token>
// （注意不是 Bearer）。zone 发现用 GET /domains/?owns_qname=<域名>。
// RRSet 模型：创建/更新是 PATCH /domains/{zone}/rrsets/，body 必须是
// list：[{subname:"_acme-challenge", type:"TXT",
// records:["\"<值>\""], ttl:3600}]（值须包双引号；PATCH 是整条 RRSet
// 替换，不是追加）；删除是 PATCH 同一 RRSet 且 records:[]（清空即删）。
// ⚠️ 限速按域名计，很紧：2/秒、15/分钟、100/小时、300/天——
// 遇 429 自动退避重试（读 Retry-After，没有就 2/4/8 秒），
// 否则续签高峰易被拦。Token 可限到 _acme-challenge*（最小权限友好）。

const desecAPI = "https://desec.io/api/v1"

// desecRetrySleep 是 429 退避重试的等待（包级变量只为测试能跳过等待）。
var desecRetrySleep = time.Sleep

// desecHeaders 返回 deSEC 的请求头（缺 Token 时报清楚去哪填）。
func desecHeaders(s *Server) (map[string]string, error) {
	token := s.cfg.DesecToken
	if token == "" {
		return nil, fmt.Errorf("未配置 deSEC 凭证（设置里填 Token，Token Management 可建带策略的 token，建议限到 _acme-challenge*）")
	}
	return map[string]string{"Authorization": "Token " + token}, nil
}

// desecErr 把 deSEC 的 JSON 错误拼成带 detail 摘要的错误。
func desecErr(code int, b []byte) error {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil && e.Detail != "" {
		return fmt.Errorf("deSEC 报错（HTTP %d）：%s", code, truncateProviderBody([]byte(e.Detail)))
	}
	return fmt.Errorf("deSEC 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// desecReq 发一次 deSEC 请求；遇 429 按 Retry-After（没有就 2/4/8 秒）
// 退避重试，最多 3 次——deSEC 限速很紧（2/秒），不重试续签高峰易被拦。
func desecReq(s *Server, method, url string, body []byte) (int, []byte, error) {
	headers, err := desecHeaders(s)
	if err != nil {
		return 0, nil, err
	}
	backoffs := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	var code int
	var b []byte
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		code, b, err = dnsHTTPReq(method, url, body, "application/json", headers)
		if err != nil {
			return 0, nil, fmt.Errorf("deSEC 请求失败：%v", err)
		}
		if code != 429 {
			return code, b, nil
		}
		if attempt == len(backoffs) {
			break
		}
		// 读 Retry-After（秒），没有就用固定退避。
		wait := backoffs[attempt]
		if ra := desecRetryAfter(b); ra > 0 {
			wait = ra
		}
		desecRetrySleep(wait)
	}
	return code, b, desecErr(code, b)
}

// desecRetryAfter 从 429 响应体里读 Retry-After 秒数（读不到返回 0）。
func desecRetryAfter(b []byte) time.Duration {
	var e struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) != nil {
		return 0
	}
	// detail 形如 "Request was throttled. Expected available in 12 seconds."
	for _, f := range strings.Fields(e.Detail) {
		if n, err := strconv.Atoi(strings.Trim(f, ".")); err == nil && n > 0 && n <= 3600 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

// desecFindZone 找域名所在的 zone：GET /domains/?owns_qname=<域名>，
// 返回拥有该名的 zone 列表，取最长后缀匹配的那个。
func desecFindZone(s *Server, domain string) (string, error) {
	if _, err := desecHeaders(s); err != nil {
		return "", err
	}
	code, b, err := desecReq(s, "GET", desecAPI+"/domains/?owns_qname="+domain, nil)
	if err != nil {
		return "", err
	}
	if code >= 300 {
		return "", desecErr(code, b)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(b, &list) != nil {
		return "", fmt.Errorf("deSEC 返回解析失败")
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
		return "", fmt.Errorf("deSEC 里找不到域名 %s 的解析（请先在 deSEC 添加该域名）", domain)
	}
	return best, nil
}

type desecRRSet struct {
	Subname string   `json:"subname"`
	Type    string   `json:"type"`
	Records []string `json:"records"`
	TTL     int      `json:"ttl"`
}

type desecProvider struct{}

func (desecProvider) ID() string { return "desec" }

func (desecProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, err := desecFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	code, b, err := desecReq(s, "GET", desecAPI+"/domains/"+zone+"/rrsets/?subname=_acme-challenge&type=TXT", nil)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, desecErr(code, b)
	}
	var list []desecRRSet
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("deSEC 返回解析失败")
	}
	relName := dnsRelativeName(domain, zone)
	var out []dnsTXTRecord
	for _, r := range list {
		if r.Type != "TXT" || r.Subname != relName {
			continue
		}
		for _, v := range r.Records {
			// 值存的时候包了双引号，显示时剥一层。
			out = append(out, dnsTXTRecord{Value: strings.Trim(v, "\"")})
		}
	}
	return out, nil
}

func (p desecProvider) Create(s *Server, domain, value string) (string, error) {
	zone, err := desecFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// PATCH 是整条 RRSet 替换：直接写新值，旧值自然被换掉，不用先删。
	// TXT 值须包双引号。
	payload, _ := json.Marshal([]map[string]any{
		{"subname": dnsRelativeName(domain, zone), "type": "TXT", "records": []string{"\"" + value + "\""}, "ttl": 3600},
	})
	code, b, err := desecReq(s, "PATCH", desecAPI+"/domains/"+zone+"/rrsets/", payload)
	if err != nil {
		return "", fmt.Errorf("deSEC 写 TXT 记录失败：%v。%s", err, providerManualHint("desec", domain))
	}
	if code >= 300 {
		return "", fmt.Errorf("deSEC 写 TXT 记录失败：%v。%s", desecErr(code, b), providerManualHint("desec", domain))
	}
	return "", nil // RRSet 模型没有单条记录 id，删时 PATCH 空 records 清整条 RRSet
}

func (desecProvider) Delete(s *Server, domain, id, value string) error {
	zone, err := desecFindZone(s, domain)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal([]map[string]any{
		{"subname": dnsRelativeName(domain, zone), "type": "TXT", "records": []string{}, "ttl": 3600},
	})
	code, b, err := desecReq(s, "PATCH", desecAPI+"/domains/"+zone+"/rrsets/", payload)
	if err != nil {
		return err
	}
	if code == 404 {
		return nil // 已经没了
	}
	if code >= 300 {
		return desecErr(code, b)
	}
	return nil
}
