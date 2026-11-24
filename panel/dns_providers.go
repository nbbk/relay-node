package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------- DNS-01 服务商抽象（v1.3.49） ----------
//
// 以前面板申请真证书只有 Cloudflare 一条路（cloudflareTXT/cfTXTFunc），
// 域名不在 Cloudflare 就签不了（报“找不到 Zone”回落自签）。现在把
// cfTXTFunc 那个缝隙通用化成接口，按服务商 id 注册；obtainACMECert 按
// 配置选中的服务商走对应实现，Cloudflare 保持原样。
//
// 每个服务商都要做到四件事（跟 1.3.48 给 Cloudflare 做的一样）：
// ① 建 _acme-challenge.<域名> TXT 记录；② 删本次建的记录；③ 建之前先查
// 同名 TXT 删残留（只删名字与类型都对得上的，其它记录一律不动，删除是
// best-effort、失败只记日志不阻塞建记录）；④ 失败时报错里带上对方返回的
// 原始信息摘要（截断到 500 字符）再加一句手动清理提醒。

// dnsTXTRecord 是一条已存在的 TXT 记录（清理与排障用）：id 是服务商的
// 记录标识（没有标识概念的服务商返回 ""，删时按 value 匹配）。
type dnsTXTRecord struct {
	ID    string
	Value string
}

// dnsTXTProvider 是 DNS-01 的 TXT 记录后端。
type dnsTXTProvider interface {
	// ID 是服务商 id（cloudflare/aliyun/dnspod/route53/godaddy/namecheap/manual）。
	ID() string
	// Create 建一条 _acme-challenge.<domain> TXT 记录（建之前先清同名残留），
	// 返回记录标识，Delete 用；没有标识概念的服务商（Route53/Namecheap）返回 ""。
	Create(s *Server, domain, value string) (string, error)
	// Delete 删掉本次建的那条记录：id 是 Create 返回的标识（CF/阿里云/
	// DNSPod/GoDaddy），没有标识的服务商按 value 删（Route53/Namecheap）。
	Delete(s *Server, domain, id, value string) error
	// ListStale 查出该域名下残留的同名 TXT 记录（清理用）：只返回名字与
	// 类型都对得上的记录，其它记录一律不返回。
	ListStale(s *Server, domain string) ([]dnsTXTRecord, error)
}

// dnsProviderIDs 是设置页下拉框里的全部服务商 id（顺序即下拉框顺序）。
// v1.4.15：第一批扩到 11 家（简报 §5.1 的顺序：5 家易的、5 家中的、
// CloudNS 最后），manual 仍在最后。
var dnsProviderIDs = []string{"cloudflare", "aliyun", "dnspod", "route53", "godaddy", "namecheap", "digitalocean", "linode", "vultr", "bunny", "hetzner", "spaceship", "gandi", "namesilo", "desec", "porkbun", "cloudns", "manual"}

// dnsProviderNames 是服务商 id 到中文名的映射（报错与前端提示用）。
var dnsProviderNames = map[string]string{
	"cloudflare":   "Cloudflare",
	"aliyun":       "阿里云 DNS",
	"dnspod":       "腾讯云 DNSPod",
	"route53":      "AWS Route 53",
	"godaddy":      "GoDaddy",
	"namecheap":    "Namecheap",
	"digitalocean": "DigitalOcean",
	"linode":       "Linode",
	"vultr":        "Vultr",
	"bunny":        "Bunny DNS",
	"hetzner":      "Hetzner",
	"spaceship":    "Spaceship",
	"gandi":        "Gandi",
	"namesilo":     "NameSilo",
	"desec":        "deSEC",
	"porkbun":      "Porkbun",
	"cloudns":      "CloudNS",
	"manual":       "手动模式",
}

// dnsProviderName 返回服务商的中文名（未知 id 返回原 id）。
func dnsProviderName(id string) string {
	if n, ok := dnsProviderNames[id]; ok {
		return n
	}
	return id
}

// validDNSProvider 判断 id 是不是已知服务商。
func validDNSProvider(id string) bool {
	for _, p := range dnsProviderIDs {
		if p == id {
			return true
		}
	}
	return false
}

// dnsProviderFor 按 id 返回服务商实现；未知 id 回落到 Cloudflare（老配置兼容）。
func dnsProviderFor(id string) dnsTXTProvider {
	switch id {
	case "aliyun":
		return aliyunProvider{}
	case "dnspod":
		return dnspodProvider{}
	case "route53":
		return route53Provider{}
	case "godaddy":
		return godaddyProvider{}
	case "namecheap":
		return namecheapProvider{}
	case "digitalocean":
		return digitaloceanProvider{}
	case "linode":
		return linodeProvider{}
	case "vultr":
		return vultrProvider{}
	case "bunny":
		return bunnyProvider{}
	case "hetzner":
		return hetznerProvider{}
	case "spaceship":
		return spaceshipProvider{}
	case "gandi":
		return gandiProvider{}
	case "namesilo":
		return namesiloProvider{}
	case "desec":
		return desecProvider{}
	case "porkbun":
		return porkbunProvider{}
	case "cloudns":
		return cloudnsProvider{}
	case "manual":
		return manualProvider{}
	default:
		return cloudflareProvider{}
	}
}

// dnsProviderID 返回当前配置的服务商 id（没配过 = cloudflare；配了未知值也回落）。
func (s *Server) dnsProviderID() string {
	id := strings.TrimSpace(s.cfg.DNSProvider)
	if !validDNSProvider(id) {
		return "cloudflare"
	}
	return id
}

// dnsProvider 返回当前配置的服务商实现。
func (s *Server) dnsProvider() dnsTXTProvider {
	return dnsProviderFor(s.dnsProviderID())
}

// dnsProviderConfigured 判断某服务商的必填凭证是否填齐（手动模式不需要凭证，恒 true）。
func (s *Server) dnsProviderConfigured(id string) bool {
	switch id {
	case "cloudflare":
		return s.cfg.CloudflareToken != ""
	case "aliyun":
		return s.cfg.AliyunAccessKeyID != "" && s.cfg.AliyunAccessKeySecret != ""
	case "dnspod":
		return s.cfg.DNSPodTokenID != "" && s.cfg.DNSPodToken != ""
	case "route53":
		return s.cfg.Route53AccessKeyID != "" && s.cfg.Route53SecretAccessKey != ""
	case "godaddy":
		return s.cfg.GoDaddyPAT != ""
	case "namecheap":
		return s.cfg.NamecheapAPIUser != "" && s.cfg.NamecheapAPIKey != "" && s.cfg.NamecheapClientIP != ""
	case "digitalocean":
		return s.cfg.DigitalOceanToken != ""
	case "linode":
		return s.cfg.LinodeToken != ""
	case "vultr":
		return s.cfg.VultrAPIKey != ""
	case "bunny":
		return s.cfg.BunnyAPIKey != ""
	case "hetzner":
		return s.cfg.HetznerToken != ""
	case "spaceship":
		return s.cfg.SpaceshipAPIKey != "" && s.cfg.SpaceshipSecret != ""
	case "gandi":
		return s.cfg.GandiToken != ""
	case "namesilo":
		return s.cfg.NameSiloAPIKey != ""
	case "desec":
		return s.cfg.DesecToken != ""
	case "porkbun":
		return s.cfg.PorkbunAPIKey != "" && s.cfg.PorkbunSecret != ""
	case "cloudns":
		return s.cfg.CloudNSAuthID != "" && s.cfg.CloudNSPassword != ""
	case "manual":
		return true
	default:
		return false
	}
}

// dnsConfiguredMap 返回各服务商的“已配置/未配置”（设置页回填用；manual 恒 true）。
func (s *Server) dnsConfiguredMap() map[string]any {
	m := map[string]any{}
	for _, id := range dnsProviderIDs {
		m[id] = s.dnsProviderConfigured(id)
	}
	return m
}

// dnsHTTPDo 执行一个已备好的 DNS 服务商请求，返回状态码与响应体；包级变量
// 只为测试能替身（默认走带 15 秒超时的真请求；测试里替换成按 URL/方法
// 返回固定响应的桩，同 cfReq 的做法）。cfReq 不走它（1.3.48 的测试桩的是 cfReq）。
var dnsHTTPDo = func(req *http.Request) (int, []byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// dnsHTTPGet 备一个 GET 请求（带 15 秒上下文超时）走 dnsHTTPDo。
func dnsHTTPGet(url string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return dnsHTTPDo(req)
}

// dnsHTTPPost 备一个 POST 请求（body 已编码好，contentType 自带）走 dnsHTTPDo。
func dnsHTTPPost(url string, body []byte, contentType string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, rd)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return dnsHTTPDo(req)
}

// dnsHTTPDelete 备一个 DELETE 请求走 dnsHTTPDo。
func dnsHTTPDelete(url string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return dnsHTTPDo(req)
}

// dnsHTTPReq 是通用请求（v1.4.15：PUT/PATCH/带 body 的 DELETE 用它；
// GET/POST/DELETE 沿用上面三个已备好的 helper，不动它们）。
func dnsHTTPReq(method, url string, body []byte, contentType string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return dnsHTTPDo(req)
}

// dnsHTTPPut 备一个 PUT 请求走 dnsHTTPDo。
func dnsHTTPPut(url string, body []byte, contentType string, headers map[string]string) (int, []byte, error) {
	return dnsHTTPReq("PUT", url, body, contentType, headers)
}

// dnsHTTPPatch 备一个 PATCH 请求走 dnsHTTPDo。
func dnsHTTPPatch(url string, body []byte, contentType string, headers map[string]string) (int, []byte, error) {
	return dnsHTTPReq("PATCH", url, body, contentType, headers)
}

// dnsHTTPDeleteBody 备一个带 body 的 DELETE 请求走 dnsHTTPDo
//（Spaceship 的删记录要传记录数组，不能走无 body 的 dnsHTTPDelete）。
func dnsHTTPDeleteBody(url string, body []byte, contentType string, headers map[string]string) (int, []byte, error) {
	return dnsHTTPReq("DELETE", url, body, contentType, headers)
}

// dnsRelativeName 把 _acme-challenge.<domain> 换成 zone 下的相对主机头
//（v1.4.15：11 家新服务商共用；domain 是子域名时相对名带子域名段，
// 如 domain=hy2ceshi.example.com、zone=example.com 时是
// "_acme-challenge.hy2ceshi"，不能写死 "_acme-challenge"）。
func dnsRelativeName(domain, zone string) string {
	return strings.TrimSuffix(acmeChallengeName(domain), "."+zone)
}

// truncateProviderBody 把服务商的错误响应体截断到 500 字符再拼进报错里
// （同 truncateCFBody，1.3.48 的做法通用到所有服务商）。
func truncateProviderBody(b []byte) string {
	return truncateCFBody(b)
}

// providerManualHint 是各 API 服务商写 TXT 失败时追加的手动提醒
// （1.3.48 给 Cloudflare 的那句通用化）：自动清理是第一道，万一它没成功，
// 用户按这句去对应 DNS 后台删残留记录再重试。
func providerManualHint(providerID, domain string) string {
	return "请到 " + dnsProviderName(providerID) + " 删除残留的 " + acmeChallengeName(domain) + " TXT 记录后再重试签发"
}

// ---------- Cloudflare（原有实现，包一层适配器） ----------

type cloudflareProvider struct{}

func (cloudflareProvider) ID() string { return "cloudflare" }

// Create 走 cfTXTFunc（1.3.45 的测试桩的就是它；cloudflareTXT 内部已经
// 在建之前清残留，行为与 1.3.48 完全一致）。
func (cloudflareProvider) Create(s *Server, domain, value string) (string, error) {
	return cfTXTFunc(s, domain, value, "")
}

func (cloudflareProvider) Delete(s *Server, domain, id, value string) error {
	_, err := cfTXTFunc(s, domain, "", id)
	return err
}

// ListStale 查该 zone 里同名 TXT 记录（走 cfReq；Create 不调它，
// cloudflareTXT 内部有自己的清理逻辑，这里只给清理排障与测试用）。
func (cloudflareProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	token := s.cfg.CloudflareToken
	if token == "" {
		return nil, fmt.Errorf("未配置 Cloudflare Token（设置里填 Zone:DNS:Edit 权限的 Token）")
	}
	zoneID := ""
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		zone := strings.Join(parts[i:], ".")
		code, b, err := cfReq(token, "GET", "https://api.cloudflare.com/client/v4/zones?name="+zone, nil)
		if err != nil || code != 200 {
			continue
		}
		var z struct {
			Result []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		if json.Unmarshal(b, &z) == nil && len(z.Result) > 0 {
			zoneID = z.Result[0].ID
			break
		}
	}
	if zoneID == "" {
		return nil, fmt.Errorf("Cloudflare 里找不到域名 %s 的 Zone", domain)
	}
	name := acmeChallengeName(domain)
	code, b, err := cfReq(token, "GET", "https://api.cloudflare.com/client/v4/zones/"+zoneID+"/dns_records?type=TXT&name="+name+"&per_page=100", nil)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare 查 TXT 记录失败：%v", err)
	}
	if code >= 300 {
		return nil, fmt.Errorf("Cloudflare 查 TXT 记录失败（HTTP %d）：%s", code, truncateCFBody(b))
	}
	var list struct {
		Result []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &list) != nil {
		return nil, fmt.Errorf("Cloudflare 返回解析失败")
	}
	var out []dnsTXTRecord
	for _, r := range list.Result {
		if r.Type != "TXT" || r.Name != name {
			continue
		}
		out = append(out, dnsTXTRecord{ID: r.ID})
	}
	return out, nil
}

// ---------- 手动模式（通用兜底）：没有 API，Create/Delete 都不该被调 ----------

type manualProvider struct{}

func (manualProvider) ID() string { return "manual" }

func (manualProvider) Create(s *Server, domain, value string) (string, error) {
	return "", fmt.Errorf("手动模式没有 DNS API：请用设置页 HTTPS 卡片的两步走（先下单拿 TXT 值，去 DNS 后台加上，再点继续验证）")
}

func (manualProvider) Delete(s *Server, domain, id, value string) error {
	return fmt.Errorf("手动模式没有 DNS API：残留的 TXT 记录请去 DNS 后台手动删除")
}

func (manualProvider) ListStale(s *Server, domain string) ([]dnsTXTRecord, error) {
	return nil, fmt.Errorf("手动模式没有 DNS API，查不到残留记录：请去 DNS 后台确认 %s 的 TXT 记录", acmeChallengeName(domain))
}
