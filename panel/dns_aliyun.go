package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ---------- 阿里云 DNS（v1.3.49） ----------
//
// API 依据：阿里云官方文档《AddDomainRecord》《DescribeDomainRecords》
// 《DescribeDomains》与《RPC 签名方法》。
// 端点 https://dns.aliyuncs.com/，RPC v1.0：GET 请求，参数放查询串；
// 签名算法 HMAC-SHA1（SignatureMethod=HMAC-SHA1、SignatureVersion=1.0、
// Version=2015-01-09）；参数按 key 排序后用 RFC 3986 百分比编码拼成
// 规范查询串，StringToSign = "GET&%2F&" + 编码后的规范查询串，
// 签名 = base64(HMAC-SHA1(AccessKeySecret+"&", StringToSign))。
// 错误是 JSON {"Code","Message","RequestId"}。

const aliyunDNSAPI = "https://dns.aliyuncs.com/"

// aliyunEncode 是 RFC 3986 百分比编码（QueryEscape 会把空格编成 +、
// 把 ~ 编成 %7E、把 * 留原样，都不符合阿里云的签名要求）。
func aliyunEncode(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "%7E", "~")
	e = strings.ReplaceAll(e, "*", "%2A")
	return e
}

// aliyunSign 算 RPC 签名（params 里不含 Signature；返回签名字符串）。
func aliyunSign(secret string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, aliyunEncode(k)+"="+aliyunEncode(params[k]))
	}
	canonical := strings.Join(parts, "&")
	stringToSign := "GET&" + aliyunEncode("/") + "&" + aliyunEncode(canonical)
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// aliyunCall 调一次阿里云 DNS API：params 是业务参数（Action 等），
// 内部补签名公共参数；返回响应体（HTTP 非 200 或业务 Code 非空都报错，
// 报错里带上对方的 Code 与 Message 摘要）。
func aliyunCall(s *Server, params map[string]string) ([]byte, error) {
	id := s.cfg.AliyunAccessKeyID
	secret := s.cfg.AliyunAccessKeySecret
	if id == "" || secret == "" {
		return nil, fmt.Errorf("未配置阿里云 AccessKey（设置里填 AccessKey ID 与 AccessKey Secret）")
	}
	all := map[string]string{
		"AccessKeyId":      id,
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int63()),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2015-01-09",
	}
	for k, v := range params {
		all[k] = v
	}
	all["Signature"] = aliyunSign(secret, all)
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, aliyunEncode(k)+"="+aliyunEncode(all[k]))
	}
	code, b, err := dnsHTTPGet(aliyunDNSAPI+"?"+strings.Join(parts, "&"), nil)
	if err != nil {
		return nil, fmt.Errorf("阿里云 DNS 请求失败：%v", err)
	}
	if code != 200 {
		return nil, aliyunErr(code, b)
	}
	var apiErr struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if json.Unmarshal(b, &apiErr) == nil && apiErr.Code != "" {
		return nil, fmt.Errorf("阿里云 DNS 报错（%s）：%s", apiErr.Code, truncateProviderBody([]byte(apiErr.Message)))
	}
	return b, nil
}

// aliyunErr 把阿里云的错误响应拼成带 Code 与 Message 摘要的错误。
func aliyunErr(code int, b []byte) error {
	var apiErr struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if json.Unmarshal(b, &apiErr) == nil && apiErr.Code != "" {
		return fmt.Errorf("阿里云 DNS 报错（HTTP %d，%s）：%s", code, apiErr.Code, truncateProviderBody([]byte(apiErr.Message)))
	}
	return fmt.Errorf("阿里云 DNS 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// aliyunFindZone 找域名所在的 zone：DescribeDomains 一次取回后按最长后缀匹配。
func aliyunFindZone(s *Server, domain string) (string, error) {
	b, err := aliyunCall(s, map[string]string{"Action": "DescribeDomains", "PageSize": "100"})
	if err != nil {
		return "", err
	}
	var resp struct {
		Domains struct {
			Domain []struct {
				DomainName string `json:"DomainName"`
			} `json:"Domain"`
		} `json:"Domains"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return "", fmt.Errorf("阿里云 DNS 返回解析失败")
	}
	best := ""
	for _, d := range resp.Domains.Domain {
		if domain == d.DomainName || strings.HasSuffix(domain, "."+d.DomainName) {
			if len(d.DomainName) > len(best) {
				best = d.DomainName
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("阿里云 DNS 里找不到域名 %s 的解析（请先在阿里云 DNS 控制台添加该域名）", domain)
	}
	return best, nil
}

// aliyunRelativeHost 把 _acme-challenge.<domain> 换成 zone 下的相对主机头。
func aliyunRelativeHost(domain, zone string) string {
	return strings.TrimSuffix("_acme-challenge."+domain, "."+zone)
}

type aliyunProvider struct{}

func (aliyunProvider) ID() string { return "aliyun" }

func (aliyunProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zone, err := aliyunFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	rr := aliyunRelativeHost(domain, zone)
	b, err := aliyunCall(s, map[string]string{
		"Action": "DescribeDomainRecords", "DomainName": zone,
		"RRKeyWord": rr, "TypeKeyWord": "TXT", "PageSize": "100",
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		DomainRecords struct {
			Record []struct {
				RecordId string `json:"RecordId"`
				RR       string `json:"RR"`
				Type     string `json:"Type"`
				Value    string `json:"Value"`
			} `json:"Record"`
		} `json:"DomainRecords"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("阿里云 DNS 返回解析失败")
	}
	var out []dnsTXTRecord
	for _, r := range resp.DomainRecords.Record {
		if r.Type != "TXT" || r.RR != rr {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.RecordId, Value: r.Value})
	}
	return out, nil
}

func (p aliyunProvider) Create(s *Server, domain, value string) (string, error) {
	zone, err := aliyunFindZone(s, domain)
	if err != nil {
		return "", err
	}
	// 先清残留（v1.3.48 的做法通用化）：上次签发留下的同名 TXT 不删，再建会报错。
	// best-effort，删失败只记日志，不阻塞建记录。
	if stale, err := p.ListStale(s, domain); err == nil {
		for _, r := range stale {
			_, _ = aliyunCall(s, map[string]string{"Action": "DeleteDomainRecord", "RecordId": r.ID})
		}
	}
	rr := aliyunRelativeHost(domain, zone)
	b, err := aliyunCall(s, map[string]string{
		"Action": "AddDomainRecord", "DomainName": zone,
		"RR": rr, "Type": "TXT", "Value": value, "TTL": "600",
	})
	if err != nil {
		return "", fmt.Errorf("阿里云 DNS 写 TXT 记录失败：%v。%s", err, providerManualHint("aliyun", domain))
	}
	var resp struct {
		RecordId string `json:"RecordId"`
	}
	if json.Unmarshal(b, &resp) != nil || resp.RecordId == "" {
		return "", fmt.Errorf("阿里云 DNS 写 TXT 记录失败：返回里没有 RecordId。%s", providerManualHint("aliyun", domain))
	}
	return resp.RecordId, nil
}

func (aliyunProvider) Delete(s *Server, domain, id, value string) error {
	if id == "" {
		return nil
	}
	_, err := aliyunCall(s, map[string]string{"Action": "DeleteDomainRecord", "RecordId": id})
	return err
}
