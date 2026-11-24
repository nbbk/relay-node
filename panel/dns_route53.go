package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ---------- AWS Route 53（v1.3.49） ----------
//
// API 依据：AWS Route 53 API Reference（ChangeResourceRecordSets、
// ListHostedZonesByName、ListResourceRecordSets）。
// 端点 https://route53.amazonaws.com/2013-04-01/，签名 SigV4
// （service=route53、region=us-east-1）；Hosted Zone 按域名找
// （ListHostedZonesByName）；改记录走 ChangeResourceRecordSets
// （UPSERT/DELETE，XML 体；TXT 的 Value 必须加双引号；DELETE 必须带与
// 创建时完全相同的值，所以删记录要先读现有值）。
// Route 53 没有记录 id 的概念：Create 返回 ""，Delete 按 value 删
// （只剩本条值时整组 DELETE，还有别的值时先 DELETE 整组再 UPSERT 回剩余值）。

const route53Endpoint = "https://route53.amazonaws.com/2013-04-01"

// route53Sign 给请求做 SigV4 签名（body 是请求体原文）。
func route53Sign(req *http.Request, body []byte, accessKey, secret string, t time.Time) {
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	payloadHash := sha256.Sum256(body)
	payloadHashHex := hex.EncodeToString(payloadHash[:])
	// 规范请求：方法、URI、已排序编码的查询串、host;x-amz-date 头、payload 哈希。
	q := req.URL.Query()
	qKeys := make([]string, 0, len(q))
	for k := range q {
		qKeys = append(qKeys, k)
	}
	sort.Strings(qKeys)
	var qParts []string
	for _, k := range qKeys {
		for _, v := range q[k] {
			qParts = append(qParts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	canonical := strings.Join([]string{
		req.Method,
		escapeURI(req.URL.EscapedPath()),
		strings.Join(qParts, "&"),
		"host:" + req.URL.Host + "\n" + "x-amz-date:" + amzDate + "\n",
		"host;x-amz-date",
		payloadHashHex,
	}, "\n")
	scope := dateStamp + "/us-east-1/route53/aws4_request"
	h := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	kDate := route53HMACSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := route53HMACSHA256(kDate, "us-east-1")
	kService := route53HMACSHA256(kRegion, "route53")
	kSigning := route53HMACSHA256(kService, "aws4_request")
	sig := hex.EncodeToString(route53HMACSHA256(kSigning, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders=host;x-amz-date, Signature="+sig)
}

func route53HMACSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// escapeURI 把路径按 SigV4 要求编码（/ 不编码，其余按 RFC 3986）。
func escapeURI(p string) string {
	if p == "" {
		return "/"
	}
	var b strings.Builder
	for _, c := range []byte(p) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == '/' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// route53Creds 取 Route53 凭证（缺了就报清楚去哪填）。
func route53Creds(s *Server) (string, string, error) {
	id := s.cfg.Route53AccessKeyID
	secret := s.cfg.Route53SecretAccessKey
	if id == "" || secret == "" {
		return "", "", fmt.Errorf("未配置 Route 53 凭证（设置里填 AccessKey ID 与 Secret Access Key）")
	}
	return id, secret, nil
}

// route53Err 把 Route53 的 XML 错误响应拼成带 Code 与 Message 摘要的错误。
func route53Err(code int, b []byte) error {
	var e struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if xml.Unmarshal(b, &e) == nil && e.Error.Code != "" {
		return fmt.Errorf("Route 53 报错（HTTP %d，%s）：%s", code, e.Error.Code, truncateProviderBody([]byte(e.Error.Message)))
	}
	return fmt.Errorf("Route 53 报错（HTTP %d）：%s", code, truncateProviderBody(b))
}

// route53Do 发一次带 SigV4 签名的 Route53 请求（method、path、
// 查询参数、XML 体）；非 2xx 时返回 route53Err。
func route53Do(s *Server, method, path string, query map[string]string, body []byte) ([]byte, error) {
	id, secret, err := route53Creds(s)
	if err != nil {
		return nil, err
	}
	u := route53Endpoint + path
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	var req *http.Request
	if body != nil {
		req, err = http.NewRequest(method, u, strings.NewReader(string(body)))
	} else {
		req, err = http.NewRequest(method, u, nil)
	}
	if err != nil {
		return nil, err
	}
	route53Sign(req, body, id, secret, time.Now())
	code, b, err := dnsHTTPDo(req)
	if err != nil {
		return nil, fmt.Errorf("Route 53 请求失败：%v", err)
	}
	if code >= 300 {
		return nil, route53Err(code, b)
	}
	return b, nil
}

// route53FindZoneID 找域名所在的 Hosted Zone id：从完整域名逐级往上试
// ListHostedZonesByName，Name 必须精确等于候选 zone（带尾点）且不是私有 zone。
func route53FindZoneID(s *Server, domain string) (string, error) {
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone := strings.Join(parts[i:], ".")
		b, err := route53Do(s, "GET", "/hostedzonesbyname", map[string]string{"dnsname": zone, "maxitems": "1"}, nil)
		if err != nil {
			continue
		}
		var resp struct {
			XMLName     xml.Name `xml:"ListHostedZonesByNameResponse"`
			HostedZones []struct {
				ID     string `xml:"Id"`
				Name   string `xml:"Name"`
				Config struct {
					PrivateZone bool `xml:"PrivateZone"`
				} `xml:"Config"`
			} `xml:"HostedZones>HostedZone"`
		}
		if xml.Unmarshal(b, &resp) != nil {
			continue
		}
		for _, z := range resp.HostedZones {
			if z.Name == zone+"." && !z.Config.PrivateZone {
				return strings.TrimPrefix(z.ID, "/hostedzone/"), nil
			}
		}
	}
	return "", fmt.Errorf("Route 53 里找不到域名 %s 的 Hosted Zone（请先在 Route 53 控制台建该域名的公有 Hosted Zone）", domain)
}

// route53ChangeXML 拼 ChangeResourceRecordSets 的 XML 体：values 是整组值
// （UPSERT 只带新值；DELETE 必须带现有全部值）。
func route53ChangeXML(action, name string, values []string, ttl int) []byte {
	type record struct {
		XMLName xml.Name `xml:"ResourceRecord"`
		Value   string   `xml:"Value"`
	}
	type rrset struct {
		XMLName xml.Name `xml:"ResourceRecordSet"`
		Name    string   `xml:"Name"`
		Type    string   `xml:"Type"`
		TTL     int      `xml:"TTL"`
		Records []record `xml:"ResourceRecords>ResourceRecord"`
	}
	type change struct {
		XMLName xml.Name `xml:"Change"`
		Action  string   `xml:"Action"`
		RRSet   rrset    `xml:"ResourceRecordSet"`
	}
	type batch struct {
		XMLName xml.Name `xml:"ChangeBatch"`
		Changes []change `xml:"Changes>Change"`
	}
	type req struct {
		XMLName xml.Name `xml:"ChangeResourceRecordSetsRequest"`
		Xmlns   string   `xml:"xmlns,attr"`
		Batch   batch    `xml:"ChangeBatch"`
	}
	recs := make([]record, 0, len(values))
	for _, v := range values {
		// TXT 的 Value 必须加双引号（Route53 规定）。
		recs = append(recs, record{Value: `"` + v + `"`})
	}
	out, _ := xml.Marshal(req{
		Xmlns: "https://route53.amazonaws.com/doc/2013-04-01/",
		Batch: batch{Changes: []change{{Action: action, RRSet: rrset{
			Name: name, Type: "TXT", TTL: ttl, Records: recs,
		}}}},
	})
	return append([]byte(xml.Header), out...)
}

// route53ListValues 读 _acme-challenge.<domain> TXT 记录的现有值（去引号后）。
func route53ListValues(s *Server, zoneID, name string) ([]string, error) {
	b, err := route53Do(s, "GET", "/hostedzone/"+zoneID+"/rrset", map[string]string{"name": name, "type": "TXT", "maxitems": "100"}, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		XMLName xml.Name `xml:"ListResourceRecordSetsResponse"`
		Sets    []struct {
			Name    string `xml:"Name"`
			Type    string `xml:"Type"`
			TTL     int    `xml:"TTL"`
			Records []struct {
				Value string `xml:"Value"`
			} `xml:"ResourceRecords>ResourceRecord"`
		} `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	if xml.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("Route 53 返回解析失败")
	}
	for _, set := range resp.Sets {
		if set.Name != name+"." && set.Name != name {
			continue
		}
		if set.Type != "TXT" {
			continue
		}
		var vals []string
		for _, r := range set.Records {
			vals = append(vals, strings.Trim(r.Value, `"`))
		}
		return vals, nil
	}
	return nil, nil
}

type route53Provider struct{}

func (route53Provider) ID() string { return "route53" }

func (route53Provider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	zoneID, err := route53FindZoneID(s, domain)
	if err != nil {
		return nil, err
	}
	name := acmeChallengeName(domain)
	vals, err := route53ListValues(s, zoneID, name)
	if err != nil {
		return nil, err
	}
	var out []dnsTXTRecord
	for _, v := range vals {
		out = append(out, dnsTXTRecord{Value: v})
	}
	return out, nil
}

func (p route53Provider) Create(s *Server, domain, value string) (string, error) {
	zoneID, err := route53FindZoneID(s, domain)
	if err != nil {
		return "", err
	}
	name := acmeChallengeName(domain)
	// UPSERT 会整组替换：同名 TXT 里若有上次签发留下的旧挑战值，会被新值
	// 一并替换掉（等同于 1.3.48 给 Cloudflare 做的“建前清残留”）。
	body := route53ChangeXML("UPSERT", name, []string{value}, 60)
	if _, err := route53Do(s, "POST", "/hostedzone/"+zoneID+"/rrset/", nil, body); err != nil {
		return "", fmt.Errorf("Route 53 写 TXT 记录失败：%v。%s", err, providerManualHint("route53", domain))
	}
	return "", nil // Route53 没有记录 id，删时按值删
}

func (route53Provider) Delete(s *Server, domain, id, value string) error {
	zoneID, err := route53FindZoneID(s, domain)
	if err != nil {
		return err
	}
	name := acmeChallengeName(domain)
	vals, err := route53ListValues(s, zoneID, name)
	if err != nil {
		return err
	}
	found := false
	var rest []string
	for _, v := range vals {
		if v == value {
			found = true
			continue
		}
		rest = append(rest, v)
	}
	if !found {
		return nil // 已经没了
	}
	// DELETE 必须带现有全部值：先整组删，再把剩余值写回去。
	delBody := route53ChangeXML("DELETE", name, vals, 60)
	if _, err := route53Do(s, "POST", "/hostedzone/"+zoneID+"/rrset/", nil, delBody); err != nil {
		return err
	}
	if len(rest) == 0 {
		return nil
	}
	upBody := route53ChangeXML("UPSERT", name, rest, 60)
	_, err = route53Do(s, "POST", "/hostedzone/"+zoneID+"/rrset/", nil, upBody)
	return err
}
