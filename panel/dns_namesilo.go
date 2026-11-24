package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---------- NameSilo（v1.4.15） ----------
//
// API 依据：NameSilo 官方 API 文档（namesilo.com/api-reference）。
// 端点 https://www.namesilo.com/api，GET 式 API：全部操作都走 GET，
// 参数放查询串（version=1&type=json&key=…）。创建 dnsAddRecord
// {domain, rrtype:TXT, rrhost:_acme-challenge, rrvalue:<值>, rrttl:3600}；
// 查 dnsListRecords；删除 dnsDeleteRecord 按 rrid 删。
// 成功判定是 reply.code=300（不是 HTTP 状态码）。
// 限速：官方文档未公布数字，不编造；每次签发只发 3–5 次请求。

const namesiloAPI = "https://www.namesilo.com/api"

// namesiloKey 取 NameSilo 凭证（缺了就报清楚去哪填）。
func namesiloKey(s *Server) (string, error) {
	key := s.cfg.NameSiloAPIKey
	if key == "" {
		return "", fmt.Errorf("未配置 NameSilo 凭证（设置里填 API Key，在 namesilo.com/account/api-manager 生成）")
	}
	return key, nil
}

// namesiloReply 是 NameSilo API 的通用 JSON 信封。
type namesiloReply struct {
	Request struct {
		Operation string `json:"operation"`
	} `json:"request"`
	Reply struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
		Record []struct {
			RecordID string `json:"record_id"`
			Type     string `json:"type"`
			Host     string `json:"host"`
			Value    string `json:"value"`
			TTL      string `json:"ttl"`
		} `json:"resource_record"`
		RecordID string `json:"record_id"`
	} `json:"reply"`
}

// namesiloCall 调一次 NameSilo API（op 是操作名，params 是业务参数），
// 返回解析后的信封；code 不是 300 就报错（带 detail 摘要）。
func namesiloCall(s *Server, op string, params map[string]string) (*namesiloReply, error) {
	key, err := namesiloKey(s)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("version", "1")
	q.Set("type", "json")
	q.Set("key", key)
	for k, v := range params {
		q.Set(k, v)
	}
	code, b, err := dnsHTTPGet(namesiloAPI+"/"+op+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("NameSilo 请求失败：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("NameSilo 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var resp namesiloReply
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("NameSilo 返回解析失败")
	}
	if resp.Reply.Code != "300" {
		return nil, fmt.Errorf("NameSilo 报错（code %s）：%s", resp.Reply.Code, truncateProviderBody([]byte(resp.Reply.Detail)))
	}
	return &resp, nil
}

// namesiloFindZone 找域名所在的 zone：从完整域名逐级往上试，
// dnsListRecords 成功（code 300）的第一个就是。
func namesiloFindZone(s *Server, domain string) (zone, relHost string, err error) {
	if _, err := namesiloKey(s); err != nil {
		return "", "", err
	}
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		relHost = strings.TrimSuffix(acmeChallengeName(domain), "."+zone)
		if _, err := namesiloCall(s, "dnsListRecords", map[string]string{"domain": zone}); err != nil {
			// 凭证错直接报出来，不用继续试；域名不在名下就试更短的后缀。
			if strings.Contains(err.Error(), "未配置") {
				return "", "", err
			}
			continue
		}
		return zone, relHost, nil
	}
	return "", "", fmt.Errorf("NameSilo 里找不到域名 %s 的解析（请先确认该域名在 NameSilo 注册）", domain)
}

type namesiloProvider struct{}

func (namesiloProvider) ID() string { return "namesilo" }

func (namesiloProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, relHost, err := namesiloFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	resp, err := namesiloCall(s, "dnsListRecords", map[string]string{"domain": zone})
	if err != nil {
		return nil, err
	}
	var out []dnsTXTRecord
	for _, r := range resp.Reply.Record {
		if r.Type != "TXT" || r.Host != relHost {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.RecordID, Value: r.Value})
	}
	return out, nil
}

func (p namesiloProvider) Create(s *Server, domain, value string) (string, error) {
	zone, relHost, err := namesiloFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留：单条 dnsDeleteRecord，不动其它记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _ = namesiloCall(s, "dnsDeleteRecord", map[string]string{"domain": zone, "rrid": r.ID})
		}
	}
	resp, err := namesiloCall(s, "dnsAddRecord", map[string]string{
		"domain": zone, "rrtype": "TXT", "rrhost": relHost, "rrvalue": value, "rrttl": "3600",
	})
	if err != nil {
		return "", fmt.Errorf("NameSilo 写 TXT 记录失败：%v。%s", err, providerManualHint("namesilo", domain))
	}
	return resp.Reply.RecordID, nil
}

func (namesiloProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	zone, _, err := namesiloFindZone(s, domain)
	if err != nil {
		return err
	}
	_, err = namesiloCall(s, "dnsDeleteRecord", map[string]string{"domain": zone, "rrid": id})
	return err
}
