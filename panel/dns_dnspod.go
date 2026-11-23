package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---------- 腾讯云 DNSPod 经典 API（v1.3.49） ----------
//
// API 依据：DNSPod 经典版 API（dnsapi.cn）文档与多个 Go 客户端实现。
// POST https://dnsapi.cn/{Action}，表单编码；公共参数
// login_token={TokenID},{Token}、format=json。
// Domain.Info（domain=zone）取域名 id；Record.List（domain_id、
// sub_domain、record_type=TXT）查记录；Record.Create（domain_id、
// sub_domain、record_type=TXT、record_line=默认、value、ttl=600）建记录；
// Record.Remove（domain_id、record_id）删记录。
// 成功标志 status.code=="1"，否则 status.message 里有原因。

const dnspodAPI = "https://dnsapi.cn/"

// dnspodCall 调一次 DNSPod 经典 API（action 是 Record.List 这类动作名），
// 返回响应体；status.code != "1" 时报错，报错里带上 status.message 摘要。
func dnspodCall(s *Server, action string, extra map[string]string) ([]byte, error) {
	id := s.cfg.DNSPodTokenID
	token := s.cfg.DNSPodToken
	if id == "" || token == "" {
		return nil, fmt.Errorf("未配置 DNSPod Token（设置里填 Token ID 与 Token）")
	}
	form := url.Values{}
	form.Set("login_token", id+","+token)
	form.Set("format", "json")
	for k, v := range extra {
		form.Set(k, v)
	}
	code, b, err := dnsHTTPPost(dnspodAPI+action, []byte(form.Encode()), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return nil, fmt.Errorf("DNSPod 请求失败：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("DNSPod 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var status struct {
		Status struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	}
	if json.Unmarshal(b, &status) != nil {
		return nil, fmt.Errorf("DNSPod 返回解析失败")
	}
	if status.Status.Code != "1" {
		return nil, fmt.Errorf("DNSPod 报错（%s）：%s", status.Status.Code, truncateProviderBody([]byte(status.Status.Message)))
	}
	return b, nil
}

// dnspodFindDomainID 找域名所在的 zone id：从完整域名逐级往上试 Domain.Info。
func dnspodFindDomainID(s *Server, domain string) (zone string, domainID string, err error) {
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone = strings.Join(parts[i:], ".")
		b, err := dnspodCall(s, "Domain.Info", map[string]string{"domain": zone})
		if err != nil {
			continue
		}
		var resp struct {
			Domain struct {
				ID string `json:"id"`
			} `json:"domain"`
		}
		if json.Unmarshal(b, &resp) == nil && resp.Domain.ID != "" {
			return zone, resp.Domain.ID, nil
		}
	}
	return "", "", fmt.Errorf("DNSPod 里找不到域名 %s（请先在 DNSPod 控制台添加该域名）", domain)
}

// dnspodRelativeHost 把 _acme-challenge.<domain> 换成 zone 下的相对主机头。
func dnspodRelativeHost(domain, zone string) string {
	return strings.TrimSuffix("_acme-challenge."+domain, "."+zone)
}

type dnspodProvider struct{}

func (dnspodProvider) ID() string { return "dnspod" }

func (dnspodProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, domainID, err := dnspodFindDomainID(s, domain)
	if err != nil {
		return nil, err
	}
	sub := dnspodRelativeHost(domain, zone)
	b, err := dnspodCall(s, "Record.List", map[string]string{
		"domain_id": domainID, "sub_domain": sub, "record_type": "TXT",
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Records []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"records"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("DNSPod 返回解析失败")
	}
	var out []dnsTXTRecord
	for _, r := range resp.Records {
		if r.Type != "TXT" || r.Name != sub {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.ID, Value: r.Value})
	}
	return out, nil
}

func (p dnspodProvider) Create(s *Server, domain, value string) (string, error) {
	zone, domainID, err := dnspodFindDomainID(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留（v1.3.48 的做法通用化），best-effort。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _ = dnspodCall(s, "Record.Remove", map[string]string{"domain_id": domainID, "record_id": r.ID})
		}
	}
	sub := dnspodRelativeHost(domain, zone)
	b, err := dnspodCall(s, "Record.Create", map[string]string{
		"domain_id": domainID, "sub_domain": sub, "record_type": "TXT",
		"record_line": "默认", "value": value, "ttl": "600",
	})
	if err != nil {
		return "", fmt.Errorf("DNSPod 写 TXT 记录失败：%v。%s", err, providerManualHint("dnspod", domain))
	}
	var resp struct {
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	if json.Unmarshal(b, &resp) != nil || resp.Record.ID == "" {
		return "", fmt.Errorf("DNSPod 写 TXT 记录失败：返回里没有记录 id。%s", providerManualHint("dnspod", domain))
	}
	return resp.Record.ID, nil
}

func (dnspodProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	_, domainID, err := dnspodFindDomainID(s, domain)
	if err != nil {
		return err
	}
	_, err = dnspodCall(s, "Record.Remove", map[string]string{"domain_id": domainID, "record_id": id})
	return err
}
