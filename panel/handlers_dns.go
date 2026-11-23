package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ---------- DNS 服务商设置（v1.3.49） ----------

// handleDNSProvider 保存 DNS-01 服务商的选择与该服务商的凭证：
// 只覆盖所选服务商的字段，其它服务商已存的凭证一概不动（切服务商时旧
// Cloudflare 配置不受影响）；凭证只存、不回显（设置页只返回已配置/未配置）。
// 换服务商或换凭证时，之前签发/续签失败的原因就过时了，清掉。
func (s *Server) handleDNSProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider        string `json:"provider"`
		Token           string `json:"token"`             // cloudflare
		AccessKeyID     string `json:"access_key_id"`     // aliyun / route53
		AccessKeySecret string `json:"access_key_secret"` // aliyun
		SecretAccessKey string `json:"secret_access_key"` // route53
		TokenID         string `json:"token_id"`          // dnspod
		PAT             string `json:"pat"`               // godaddy
		APIUser         string `json:"api_user"`          // namecheap
		APIKey          string `json:"api_key"`           // namecheap
		ClientIP        string `json:"client_ip"`         // namecheap
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	provider := strings.TrimSpace(req.Provider)
	if !validDNSProvider(provider) {
		writeJSON(w, 400, errResp("服务商不对：应为 cloudflare / aliyun / dnspod / route53 / godaddy / namecheap / manual"))
		return
	}
	oldProvider := s.dnsProviderID()
	oldCfg := s.cfg // 快照：下面先合并凭证，配不齐 400 时还原，不留半截状态。
	// 按服务商保存凭证：非空覆盖、留空保持原值（与 Telegram Token 的
	// 做法一致）；只保存所选这一家的字段，其它服务商的凭证一概不动。
	// 选框本身只切 UI，真正切换服务商靠提交这张表单（或手动模式点开启时
	// 先调一次本接口切到 manual）；切到已配过的服务商时空表单也能提交。
	warning := ""
	switch provider {
	case "cloudflare":
		if token := strings.TrimSpace(req.Token); token != "" {
			s.cfg.CloudflareToken = token
			if !strings.HasPrefix(token, "cfut_") && len(token) != 40 {
				warning = "新版令牌以 cfut_ 开头，旧版是 40 位；当前令牌是 " + strconv.Itoa(len(token)) + " 位，请核对是否复制完整"
			}
		}
	case "aliyun":
		if v := strings.TrimSpace(req.AccessKeyID); v != "" {
			s.cfg.AliyunAccessKeyID = v
		}
		if v := strings.TrimSpace(req.AccessKeySecret); v != "" {
			s.cfg.AliyunAccessKeySecret = v
		}
	case "dnspod":
		if v := strings.TrimSpace(req.TokenID); v != "" {
			s.cfg.DNSPodTokenID = v
		}
		if v := strings.TrimSpace(req.Token); v != "" {
			s.cfg.DNSPodToken = v
		}
	case "route53":
		if v := strings.TrimSpace(req.AccessKeyID); v != "" {
			s.cfg.Route53AccessKeyID = v
		}
		if v := strings.TrimSpace(req.SecretAccessKey); v != "" {
			s.cfg.Route53SecretAccessKey = v
		}
	case "godaddy":
		if v := strings.TrimSpace(req.PAT); v != "" {
			s.cfg.GoDaddyPAT = v
		}
	case "namecheap":
		if v := strings.TrimSpace(req.APIUser); v != "" {
			s.cfg.NamecheapAPIUser = v
		}
		if v := strings.TrimSpace(req.APIKey); v != "" {
			s.cfg.NamecheapAPIKey = v
		}
		if v := strings.TrimSpace(req.ClientIP); v != "" {
			if ip := net.ParseIP(v); ip == nil || ip.To4() == nil {
				writeJSON(w, 400, errResp("Namecheap Client IP 必须是 IPv4 地址"))
				return
			}
			s.cfg.NamecheapClientIP = v
		}
	case "manual":
		// 手动模式不用凭证。
	}
	// 保存后仍未配齐（新服务商、凭证一个没填）就报错，不切换服务商、不留半截状态。
	if !s.dnsProviderConfigured(provider) {
		s.cfg = oldCfg
		writeJSON(w, 400, errResp("请把 "+dnsProviderName(provider)+" 的凭证填齐再保存"))
		return
	}
	// 切服务商时旧 pending 订单作废（它是上一个服务商流程之外的东西——
	// 其实 pending 只属于手动模式；从 manual 切走就清掉，免得用户回来
	// 点继续时撞上过期/错域名的旧订单）。
	if oldProvider == "manual" && provider != "manual" {
		s.clearPendingOrder()
	}
	s.cfg.DNSProvider = provider
	if s.cfg.PanelCertLastErr != "" {
		s.cfg.PanelCertLastErr = ""
	}
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	if oldProvider != provider {
		s.audit(s.currentUser(r), "改 DNS 服务商", dnsProviderName(oldProvider)+" → "+dnsProviderName(provider), "成功", "")
	} else {
		s.audit(s.currentUser(r), "保存 DNS 服务商凭证", dnsProviderName(provider), "成功", "")
	}
	if warning != "" {
		writeJSON(w, 200, map[string]any{"ok": true, "warning": warning})
		return
	}
	writeJSON(w, 200, okResp(nil))
}

// ---------- 手动模式两步走（v1.3.49） ----------

// manualPendingStatus 返回当前进行中的手动签发订单（设置页回填用）：
// 没有或已过期返回 nil；否则返回域名、TXT 名与值、过期时间。
func (s *Server) manualPendingStatus() map[string]any {
	p, err := s.loadPendingOrder()
	if err != nil {
		return nil
	}
	txtName, txtValue := "", ""
	if len(p.Authz) > 0 {
		txtName = p.Authz[0].TXTName
		txtValue = p.Authz[0].TXTValue
	}
	return map[string]any{
		"domain": p.Domain, "txt_name": txtName, "txt_value": txtValue,
		"expires_at": p.ExpiresAt.Format(time.RFC3339),
	}
}

// handleManualStart 手动签发第一步：下单并返回 TXT 记录名与值，
// 同时把 pending 订单落盘（1 小时有效，面板重启后还在）。
func (s *Server) handleManualStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" || !validDomain(domain) {
		writeJSON(w, 400, errResp("域名格式不对"))
		return
	}
	if s.dnsProviderID() != "manual" {
		writeJSON(w, 400, errResp("当前 DNS 服务商不是手动模式：请先在设置页把服务商切成“手动添加 TXT”"))
		return
	}
	s.certMu.Lock()
	defer s.certMu.Unlock()
	st, _, err := s.startACMEOrder(domain)
	if err != nil {
		writeJSON(w, 500, errResp("下单失败："+err.Error()))
		return
	}
	now := time.Now()
	pend := &acmePendingOrder{
		Version: 1, Domain: domain, OrderURL: st.OrderURL, FinalizeURL: st.FinalizeURL,
		CSRKeyPEM: string(st.CSRKeyPEM), CreatedAt: now, ExpiresAt: now.Add(acmePendingTTL),
	}
	for _, az := range st.Authz {
		pend.Authz = append(pend.Authz, acmePendingAuthz{
			AuthzURL: az.AuthzURL, ChallengeURL: az.ChallengeURL, Token: az.Token,
			TXTName: az.TXTName, TXTValue: az.TXTValue,
		})
	}
	if err := s.savePendingOrder(pend); err != nil {
		writeJSON(w, 500, errResp("保存订单失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "手动签发第一步", domain, "成功", "订单已下，待用户加 TXT 记录")
	txtName, txtValue := "", ""
	if len(pend.Authz) > 0 {
		txtName = pend.Authz[0].TXTName
		txtValue = pend.Authz[0].TXTValue
	}
	writeJSON(w, 200, okResp(map[string]any{
		"domain": domain, "txt_name": txtName, "txt_value": txtValue,
		"expires_at": pend.ExpiresAt.Format(time.RFC3339),
	}))
}

// handleManualConfirm 手动签发第二步：读回 pending，先等 TXT 生效
// （看不到就报错但不提交，pending 还能用），再提交挑战、finalize、
// 下证书；成功后按 handleHTTPS 成功路径开 HTTPS 并重启面板。
func (s *Server) handleManualConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" || !validDomain(domain) {
		writeJSON(w, 400, errResp("域名格式不对"))
		return
	}
	if s.dnsProviderID() != "manual" {
		writeJSON(w, 400, errResp("当前 DNS 服务商不是手动模式"))
		return
	}
	pend, err := s.loadPendingOrder()
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if pend.Domain != domain {
		writeJSON(w, 400, errResp(fmt.Sprintf("待确认的订单是 %s，不是 %s：请先取消它再重新开始", pend.Domain, domain)))
		return
	}
	s.certMu.Lock()
	defer s.certMu.Unlock()
	ac, err := s.resumeACMEClient()
	if err != nil {
		writeJSON(w, 500, errResp("恢复 ACME 账户失败："+err.Error()))
		return
	}
	st := pendingToState(pend)
	// 先等 TXT 生效（最多 2 分钟）：看不到就报错，不提交挑战，
	// pending 还在，用户等几分钟再点继续验证。
	for _, az := range st.Authz {
		if err := waitForTXTVisible(az.TXTName, az.TXTValue); err != nil {
			writeJSON(w, 500, errResp(err.Error()))
			return
		}
	}
	for _, az := range st.Authz {
		if err := submitAndPollChallenge(ac, az, true); err != nil {
			if errors.Is(err, errACMEValidationFailed) {
				// 验证一旦 invalid，订单就作废了：删 pending，让用户重新开始。
				s.clearPendingOrder()
			}
			writeJSON(w, 500, errResp(err.Error()))
			return
		}
	}
	if err := s.finalizeACMEOrder(ac, st); err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	s.clearPendingOrder()
	s.cfg.PanelCertLastErr = ""
	s.cfg.PanelCertRenewWarned = false
	s.cfg.HTTPSEnabled = true
	s.cfg.HTTPSDomain = domain
	if err := s.saveConfig(); err != nil {
		writeJSON(w, 500, errResp("保存失败"))
		return
	}
	s.audit(s.currentUser(r), "手动签发第二步", domain, "成功", "真证书已签下，面板重启生效")
	writeJSON(w, 200, okResp(map[string]any{"msg": "面板正在重启以切换 HTTPS"}))
	go func() { time.Sleep(300 * time.Millisecond); restartPanelFunc(s.env) }()
}

// handleManualCancel 取消进行中的手动签发（删 pending 文件）。
func (s *Server) handleManualCancel(w http.ResponseWriter, r *http.Request) {
	s.clearPendingOrder()
	s.audit(s.currentUser(r), "取消手动签发", "", "成功", "")
	writeJSON(w, 200, okResp(nil))
}
