package main

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ---------- Namecheap（v1.3.49） ----------
//
// API 依据：Namecheap API 文档（namecheap.domains.dns.getHosts / setHosts）
// 与 domain-sdk.dev（2026-10-30 核对）。
// 端点 https://api.namecheap.com/xml.response，GET 请求，参数：
// ApiUser、ApiKey、UserName（通常与 ApiUser 相同）、ClientIp、Command、
// SLD、TLD。返回 XML，根节点 ApiResponse 的 Status 是 OK 或 ERROR。
// 关键语义：
// ① setHosts 是替换整组记录——必须先 getHosts 读全量，合并后再整体回写，
//    否则会删掉用户其它记录（建与删都走“读→改→回写”）；
// ② 只支持用 Namecheap BasicDNS 的域名（PremiumDNS/FreeDNS、或指到别家
//    DNS 的管不了，getHosts 会报 IsUsingOurDNS=false）；
// ③ 调用方 IPv4 必须在 Namecheap 后台白名单里（ClientIp 参数也要传该 IP），
//    否则报错，报错里要明说白名单这一点。
// TTL 用 120（取值范围 60–86400）。

const namecheapAPI = "https://api.namecheap.com/xml.response"

// namecheapCreds 取 Namecheap 凭证（缺了就报清楚去哪填；ClientIp 必须是 IPv4）。
func namecheapCreds(s *Server) (apiUser, apiKey, clientIP string, err error) {
	apiUser = s.cfg.NamecheapAPIUser
	apiKey = s.cfg.NamecheapAPIKey
	clientIP = s.cfg.NamecheapClientIP
	if apiUser == "" || apiKey == "" {
		return "", "", "", fmt.Errorf("未配置 Namecheap 凭证（设置里填 API User 与 API Key）")
	}
	if clientIP == "" {
		return "", "", "", fmt.Errorf("未填 Namecheap 的 Client IP：填本机公网 IPv4，且必须先在 Namecheap 后台（Profile → Security → API Access）把它加进白名单，否则 API 会拒绝")
	}
	if ip := net.ParseIP(clientIP); ip == nil || ip.To4() == nil {
		return "", "", "", fmt.Errorf("Namecheap 的 Client IP 必须是 IPv4 地址，当前是 %q", clientIP)
	}
	return apiUser, apiKey, clientIP, nil
}

// namecheapResponse 是 Namecheap API 的通用 XML 信封。
type namecheapResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  []struct {
		Number  string `xml:"Number,attr"`
		Message string `xml:",chardata"`
	} `xml:"Errors>Error"`
	Hosts *struct {
		Domain        string `xml:"Domain,attr"`
		IsUsingOurDNS bool   `xml:"IsUsingOurDNS,attr"`
		HostList      []struct {
			HostID  string `xml:"HostId,attr"`
			Name    string `xml:"Name,attr"`
			Type    string `xml:"Type,attr"`
			Address string `xml:"Address,attr"`
			MXPref  string `xml:"MXPref,attr"`
			TTL     string `xml:"TTL,attr"`
		} `xml:"host"`
	} `xml:"CommandResponse>DomainDNSGetHostsResult"`
}

// namecheapErr 把 Namecheap 的 ERROR 响应拼成错误；提到 IP 时追加白名单提醒。
func namecheapErr(resp *namecheapResponse) error {
	msgs := make([]string, 0, len(resp.Errors))
	for _, e := range resp.Errors {
		msgs = append(msgs, strings.TrimSpace(e.Message))
	}
	msg := strings.Join(msgs, "；")
	if msg == "" {
		msg = "未知错误"
	}
	err := fmt.Errorf("Namecheap 报错：%s", truncateProviderBody([]byte(msg)))
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "ip") && (strings.Contains(lower, "whitelist") || strings.Contains(lower, "not authorized") || strings.Contains(lower, "invalid") || strings.Contains(lower, "blocked")) {
		return fmt.Errorf("%v。调用方 IP 必须在 Namecheap 后台白名单里（Profile → Security → API Access），且 Client IP 要填本机公网 IPv4", err)
	}
	return err
}

// namecheapGetHosts 调 getHosts 读整组记录（返回信封；Status != OK 时报错）。
func namecheapGetHosts(s *Server, sld, tld string) (*namecheapResponse, error) {
	apiUser, apiKey, clientIP, err := namecheapCreds(s)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("ApiUser", apiUser)
	q.Set("ApiKey", apiKey)
	q.Set("UserName", apiUser)
	q.Set("ClientIp", clientIP)
	q.Set("Command", "namecheap.domains.dns.getHosts")
	q.Set("SLD", sld)
	q.Set("TLD", tld)
	code, b, err := dnsHTTPGet(namecheapAPI+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("Namecheap 请求失败：%v", err)
	}
	if code != 200 {
		return nil, fmt.Errorf("Namecheap 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var resp namecheapResponse
	if xml.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("Namecheap 返回解析失败")
	}
	if resp.Status != "OK" {
		return nil, namecheapErr(&resp)
	}
	return &resp, nil
}

// namecheapSetHosts 调 setHosts 整体回写一组记录（hosts 是 getHosts 的 host 列表
// 格式：每条含 Name/Type/Address/MXPref/TTL；不传 HostId）。
func namecheapSetHosts(s *Server, sld, tld string, hosts []map[string]string) error {
	apiUser, apiKey, clientIP, err := namecheapCreds(s)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("ApiUser", apiUser)
	q.Set("ApiKey", apiKey)
	q.Set("UserName", apiUser)
	q.Set("ClientIp", clientIP)
	q.Set("Command", "namecheap.domains.dns.setHosts")
	q.Set("SLD", sld)
	q.Set("TLD", tld)
	for i, h := range hosts {
		n := strconv.Itoa(i + 1)
		q.Set("HostName"+n, h["Name"])
		q.Set("RecordType"+n, h["Type"])
		q.Set("Address"+n, h["Address"])
		q.Set("MXPref"+n, h["MXPref"])
		q.Set("TTL"+n, h["TTL"])
	}
	code, b, err := dnsHTTPGet(namecheapAPI+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("Namecheap 请求失败：%v", err)
	}
	if code != 200 {
		return fmt.Errorf("Namecheap 报错（HTTP %d）：%s", code, truncateProviderBody(b))
	}
	var resp namecheapResponse
	if xml.Unmarshal(b, &resp) != nil {
		return fmt.Errorf("Namecheap 返回解析失败")
	}
	if resp.Status != "OK" {
		return namecheapErr(&resp)
	}
	return nil
}

// namecheapFindZone 找域名所在的 zone：SLD=首段、TLD=其余，从完整域名逐级往上试，
// getHosts 成功的第一个就是（域名不在 Namecheap 或没用 BasicDNS 时报错）。
func namecheapFindZone(s *Server, domain string) (sld, tld string, err error) {
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone := parts[i:]
		sld, tld = zone[0], strings.Join(zone[1:], ".")
		resp, err := namecheapGetHosts(s, sld, tld)
		if err != nil {
			// 域名不存在或不在 Namecheap：继续试更短的后缀；其它错误（如凭证、
			// 白名单）直接报出来，不用继续试。
			if strings.Contains(err.Error(), "未配置") || strings.Contains(err.Error(), "白名单") || strings.Contains(err.Error(), "Client IP") {
				return "", "", err
			}
			continue
		}
		if resp.Hosts == nil {
			continue
		}
		if !resp.Hosts.IsUsingOurDNS {
			return "", "", fmt.Errorf("域名 %s 没用 Namecheap 的 DNS（BasicDNS），Namecheap API 管不了它（PremiumDNS/FreeDNS、或指到别家 DNS 的域名请换服务商或走手动模式）", domain)
		}
		return sld, tld, nil
	}
	return "", "", fmt.Errorf("Namecheap 里找不到域名 %s（请先确认该域名在 Namecheap 注册且用 BasicDNS）", domain)
}

// namecheapHostsToMaps 把 getHosts 的记录转成 setHosts 要的 map 列表。
func namecheapHostsToMaps(resp *namecheapResponse) []map[string]string {
	if resp.Hosts == nil {
		return nil
	}
	out := make([]map[string]string, 0, len(resp.Hosts.HostList))
	for _, h := range resp.Hosts.HostList {
		mx := h.MXPref
		if mx == "" {
			mx = "10"
		}
		ttl := h.TTL
		if ttl == "" {
			ttl = "120"
		}
		out = append(out, map[string]string{
			"Name": h.Name, "Type": h.Type, "Address": h.Address, "MXPref": mx, "TTL": ttl,
		})
	}
	return out
}

type namecheapProvider struct{}

func (namecheapProvider) ID() string { return "namecheap" }

func (namecheapProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	sld, tld, err := namecheapFindZone(s, domain)
	if err != nil {
		return nil, err
	}
	resp, err := namecheapGetHosts(s, sld, tld)
	if err != nil {
		return nil, err
	}
	relName := strings.TrimSuffix(acmeChallengeName(domain), "."+sld+"."+tld)
	var out []dnsTXTRecord
	for _, h := range resp.Hosts.HostList {
		if h.Type != "TXT" || h.Name != relName {
			continue
		}
		out = append(out, dnsTXTRecord{Value: h.Address})
	}
	return out, nil
}

func (p namecheapProvider) Create(s *Server, domain, value string) (string, error) {
	sld, tld, err := namecheapFindZone(s, domain)
	if err != nil {
		return "", err
	}
	relName := strings.TrimSuffix(acmeChallengeName(domain), "."+sld+"."+tld)
	resp, err := namecheapGetHosts(s, sld, tld)
	if err != nil {
		return "", err
	}
	// setHosts 是整组替换：先读全量，去掉同名 TXT（残留），再加新值回写。
	// 其它记录原样带回去，一条都不能丢。
	hosts := namecheapHostsToMaps(resp)
	kept := hosts[:0]
	for _, h := range hosts {
		if h["Type"] == "TXT" && h["Name"] == relName {
			continue
		}
		kept = append(kept, h)
	}
	kept = append(kept, map[string]string{
		"Name": relName, "Type": "TXT", "Address": value, "MXPref": "10", "TTL": "120",
	})
	if err := namecheapSetHosts(s, sld, tld, kept); err != nil {
		return "", fmt.Errorf("Namecheap 写 TXT 记录失败：%v。%s", err, providerManualHint("namecheap", domain))
	}
	return "", nil // Namecheap 没有稳定的记录 id，删时按值删
}

func (namecheapProvider) Delete(s *Server, domain, id, value string) error {
	sld, tld, err := namecheapFindZone(s, domain)
	if err != nil {
		return err
	}
	relName := strings.TrimSuffix(acmeChallengeName(domain), "."+sld+"."+tld)
	resp, err := namecheapGetHosts(s, sld, tld)
	if err != nil {
		return err
	}
	hosts := namecheapHostsToMaps(resp)
	kept := hosts[:0]
	removed := false
	for _, h := range hosts {
		if h["Type"] == "TXT" && h["Name"] == relName && h["Address"] == value {
			removed = true
			continue
		}
		kept = append(kept, h)
	}
	if !removed {
		return nil // 已经没了
	}
	return namecheapSetHosts(s, sld, tld, kept)
}
