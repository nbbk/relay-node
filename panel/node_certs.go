package main

// ---------- 节点证书（v1.4.12，切片 7） ----------
//
// 面板签发的域名证书，下发到节点给 HY2／TUIC／AnyTLS／Trojan／
// VLESS＋TLS 入站用（agent 按 interim 契约写
// <config-dir>/certs/<ref>.crt＋<ref>.key，600 权限，找不到就跳过
// 该入站并记日志，fail-closed）。
//
// 存储：证书文件 $PANEL_HOME/certs/node-<ref>.crt／.key（都是 600）；
// 元数据放 nodes.json 的 NodeCerts[]（ref、domain、provider、
// issuedAt、expiresAt、lastError；状态不落盘，列表按 ExpiresAt 现算）。
// ref 由 nodes.json 里的计数器分配，保证唯一（失败的签发也会占一个
// 号，只留空洞，不重用）。
//
// 绑定在下发时解析（configDocOf）：TLS 入站按域名找有效证书，
// 找到就写 cert_ref、把 PEM 放进 certs[]；找不到／过期／文件缺失
// 都不发，cert_ref 留 0。这样续签换证书不用改入站，同一域名多条
// 入站／多个节点共用一张证书。

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nodeCertRenewDays 节点证书剩余天数 ≤ 该值时自动续签（与面板自身
// 证书的 panelCertRenewDays 同口径，都是 30 天）。
const nodeCertRenewDays = 30

// NodeCert 一张节点证书的元数据（nodes.json 里 NodeCerts[] 的一条）。
type NodeCert struct {
	Ref         int64  `json:"ref"`
	Domain      string `json:"domain"`
	Provider    string `json:"provider"`
	IssuedAt    int64  `json:"issued_at"`
	ExpiresAt   int64  `json:"expires_at"`
	LastError   string `json:"last_error,omitempty"`
	RenewWarned bool   `json:"renew_warned,omitempty"`
}

// ---------- 决策纯函数（仿照 shouldRenewPanelCert，好单测） ----------

// shouldRenewNodeCert 节点证书自动续签的纯决策：到期前 30 天（含已
// 过期）就续；ExpiresAt 为空（没到期时间）不续。
func shouldRenewNodeCert(expiresAt time.Time, now time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return !now.Before(expiresAt.Add(-nodeCertRenewDays * 24 * time.Hour))
}

// nodeCertStatus 证书状态（现算，不落盘）：valid／expiring（30 天内
// 到期）／expired／unknown（没到期时间）。
func nodeCertStatus(expiresAt time.Time, now time.Time) string {
	if expiresAt.IsZero() {
		return "unknown"
	}
	if now.After(expiresAt) {
		return "expired"
	}
	if !now.Before(expiresAt.Add(-nodeCertRenewDays * 24 * time.Hour)) {
		return "expiring"
	}
	return "valid"
}

// nodeCertDaysLeft 剩余天数（负数 = 已过期 N 天；与 panelCertInfo
// 同口径：int(剩余小时/24)，今天到期算 0 天）。
func nodeCertDaysLeft(expiresAt time.Time, now time.Time) int {
	return int(time.Until(expiresAt).Hours() / 24)
}

// ---------- 入站要不要证书 ----------

// inboundNeedsCert 该入站是不是 TLS 入站（要证书才能跑）：HY2／TUIC／
// AnyTLS／Trojan 全要；VLESS 只有 security=tls 的要；REALITY／XHTTP／
// SS／SOCKS5 不要。
func inboundNeedsCert(in NodeInbound) bool {
	switch in.Protocol {
	case "hysteria2", "tuic", "anytls", "trojan":
		return true
	case "vless":
		return in.Security == "tls"
	default:
		return false
	}
}

// inboundCertDomain 该入站绑定的证书域名（Domain，没有就用 SNI）：
// 不要证书的入站返回 ok=false。面板校验只要求 HY2／TUIC／AnyTLS 填
// 真实域名，不校验 CertRef（保持不变）；绑定在下发时按这个域名解析。
func inboundCertDomain(in NodeInbound) (string, bool) {
	if !inboundNeedsCert(in) {
		return "", false
	}
	d := strings.TrimSpace(in.Domain)
	if d == "" {
		d = strings.TrimSpace(in.SNI)
	}
	if d == "" {
		return "", false
	}
	return d, true
}

// ---------- 存储助手（调用方须持有 n.mu） ----------

// nextCertRefLocked 取下一个证书 ref（调用方须持有 n.mu）。
func (n *nodeBackendStore) nextCertRefLocked() int64 {
	if n.data.NextCertRef < 1 {
		n.data.NextCertRef = 1
	}
	id := n.data.NextCertRef
	n.data.NextCertRef++
	return id
}

// nodeCertByRef 按 ref 取证书元数据（调用方须持有 n.mu）。
func (n *nodeBackendStore) nodeCertByRef(ref int64) *NodeCert {
	for i := range n.data.NodeCerts {
		if n.data.NodeCerts[i].Ref == ref {
			return &n.data.NodeCerts[i]
		}
	}
	return nil
}

// validNodeCertDomain 节点证书域名校验（v1.4.13，双模式）：允许泛域名
// `*.` 开头（去掉 `*.` 后其余口径与 validDomain 相同），单域名走
// validDomain。只给节点证书的三处签发入口用——落地证书／子域名流程
// 共用的 validDomain 本身不动，那几路不要突然接受泛域名。
func validNodeCertDomain(domain string) bool {
	if strings.HasPrefix(domain, "*.") {
		return validDomain(strings.TrimPrefix(domain, "*."))
	}
	return validDomain(domain)
}

// nodeCertDomainMatches 证书域名是否覆盖查询域名（v1.4.13，双模式）：
// 精确相等算覆盖；泛域名只覆盖一级子域名（`*.example.com` 覆盖
// `hy2ceshi.example.com`，不覆盖裸域 `example.com`，也不覆盖多级
// `a.b.example.com`）。大小写不敏感。前端 app.js 的同名函数同口径。
func nodeCertDomainMatches(certDomain, domain string) bool {
	certDomain = strings.ToLower(strings.TrimSpace(certDomain))
	domain = strings.ToLower(strings.TrimSpace(domain))
	if certDomain == "" || domain == "" {
		return false
	}
	if certDomain == domain {
		return true
	}
	if !strings.HasPrefix(certDomain, "*.") {
		return false
	}
	base := strings.TrimPrefix(certDomain, "*.")
	if domain == base || !strings.HasSuffix(domain, "."+base) {
		return false
	}
	rest := strings.TrimSuffix(domain, "."+base)
	return rest != "" && !strings.Contains(rest, ".")
}

// nodeCertForDomain 按域名找有效的证书（调用方须持有 n.mu）：先精确
// 匹配，再泛域名匹配（精确和泛域名两个都有时精确优先）；过期／没到期
// 时间的不算；同口径多张时取到期最晚的那张（重复签发留下的旧证不会
// 抢走新证）。域名大小写不敏感。
func (n *nodeBackendStore) nodeCertForDomain(domain string, now time.Time) *NodeCert {
	domain = strings.TrimSpace(domain)
	var exact, wild *NodeCert
	for i := range n.data.NodeCerts {
		c := &n.data.NodeCerts[i]
		if nodeCertStatus(time.Unix(c.ExpiresAt, 0), now) == "expired" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Domain), domain) {
			if exact == nil || c.ExpiresAt > exact.ExpiresAt {
				exact = c
			}
			continue
		}
		if nodeCertDomainMatches(c.Domain, domain) {
			if wild == nil || c.ExpiresAt > wild.ExpiresAt {
				wild = c
			}
		}
	}
	if exact != nil {
		return exact
	}
	return wild
}

// nodeCertUsedBy 哪些入站在引用这张证书（调用方须持有 n.mu）：
// 返回 "节点名 / 入站 tag" 列表（删证书时拦住并列出来）。引用按域名
// 的覆盖关系算（绑定在下发时按域名解析，入站里不存 ref）：泛域名证书
// 覆盖到的入站也算引用，不论启用还是停用——停用的入站随时可能再启用，
// 删了证书它就起不来了。
func (n *nodeBackendStore) nodeCertUsedBy(ref int64) []string {
	cert := n.nodeCertByRef(ref)
	if cert == nil {
		return nil
	}
	var out []string
	for _, node := range n.data.Nodes {
		for _, in := range node.Inbounds {
			d, ok := inboundCertDomain(in)
			if !ok {
				continue
			}
			if nodeCertDomainMatches(cert.Domain, d) {
				out = append(out, node.Name+" / "+in.InboundTag)
			}
		}
	}
	return out
}

// nodeCertUsedBySplit 把 nodeCertUsedBy 的覆盖引用按“实际绑定”分两档
// （v1.4.18，调用方须持有 n.mu）：实际下发绑定走 nodeCertForDomain
// （精确优先），落到这张证书的是“正在使用”；只靠泛域名覆盖匹配、
// 实际绑定落到另一张证书（或没有可用证书）的是“备用”。两档加起来
// 就是 nodeCertUsedBy 的覆盖列表。删证书的拦截仍用 nodeCertUsedBy
// 的覆盖口径，不动——泛域名是精确证书的兜底，删了它以后精确证书
// 一过期入站就起不来了。
func (n *nodeBackendStore) nodeCertUsedBySplit(ref int64, now time.Time) (used, standby []string) {
	cert := n.nodeCertByRef(ref)
	if cert == nil {
		return nil, nil
	}
	for _, node := range n.data.Nodes {
		for _, in := range node.Inbounds {
			d, ok := inboundCertDomain(in)
			if !ok || !nodeCertDomainMatches(cert.Domain, d) {
				continue
			}
			label := node.Name + " / " + in.InboundTag
			if bound := n.nodeCertForDomain(d, now); bound != nil && bound.Ref == ref {
				used = append(used, label)
			} else {
				standby = append(standby, label)
			}
		}
	}
	return used, standby
}

// bumpNodesUsingCertDomainLocked 该域名的证书续签后，把用到它的节点
// 的配置版本都＋1（调用方须持有 n.mu；改完调用方负责 saveLocked）：
// agent 60 秒内拉到新证书。按入站域名与证书域名的覆盖关系算（泛域名
// 证书续签时，被它覆盖的入站所在节点也要 bump），不论该入站之前有没
// 有拿到证书——之前过期拿不到的，续签后也要能拿到。
func (n *nodeBackendStore) bumpNodesUsingCertDomainLocked(domain string) {
	for _, node := range n.data.Nodes {
		for _, in := range node.Inbounds {
			d, ok := inboundCertDomain(in)
			if !ok || !nodeCertDomainMatches(domain, d) {
				continue
			}
			n.bumpNodeConfigVersionLocked(node)
			break
		}
	}
}

// nodeCertDupCheckLocked 待签域名的去重检查（v1.4.13，调用方须持有 n.mu）：
// 完全相同的域名已有可用证书时返回该证书（调用方 400 拦住，文案不变）；
// 只是覆盖关系时返回警告文案（签发继续走，警告随响应返回，前端 toast
// 显示）：已有泛域名覆盖待签域名，或待签泛域名覆盖已有单域名。过期
// 的证书不算（它已失效，不拦也不警告）。
func (n *nodeBackendStore) nodeCertDupCheckLocked(domain string, now time.Time) (*NodeCert, string) {
	domain = strings.TrimSpace(domain)
	for i := range n.data.NodeCerts {
		c := &n.data.NodeCerts[i]
		if nodeCertStatus(time.Unix(c.ExpiresAt, 0), now) == "expired" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(c.Domain), domain) {
			return c, ""
		}
	}
	for i := range n.data.NodeCerts {
		c := &n.data.NodeCerts[i]
		if nodeCertStatus(time.Unix(c.ExpiresAt, 0), now) == "expired" {
			continue
		}
		if nodeCertDomainMatches(c.Domain, domain) {
			return nil, fmt.Sprintf("已有泛域名证书 %s 覆盖该域名，两个证书并存，匹配时单个优先", c.Domain)
		}
	}
	var covered []string
	for i := range n.data.NodeCerts {
		c := &n.data.NodeCerts[i]
		if nodeCertStatus(time.Unix(c.ExpiresAt, 0), now) == "expired" {
			continue
		}
		if nodeCertDomainMatches(domain, c.Domain) {
			covered = append(covered, c.Domain)
		}
	}
	if len(covered) > 0 {
		if len(covered) == 1 {
			return nil, fmt.Sprintf("已有单域名证书 %s 被该泛域名覆盖，两个证书并存，匹配时单个优先", covered[0])
		}
		return nil, fmt.Sprintf("已有单域名证书 %s 被该泛域名覆盖，证书并存，匹配时单个优先", strings.Join(covered, "、"))
	}
	return nil, ""
}

// nodeCertFilePaths 证书文件路径（$PANEL_HOME/certs/node-<ref>.crt/.key）。
func (n *nodeBackendStore) nodeCertFilePaths(ref int64) (string, string) {
	dir := filepath.Join(n.panelHome, "certs")
	return filepath.Join(dir, fmt.Sprintf("node-%d.crt", ref)),
		filepath.Join(dir, fmt.Sprintf("node-%d.key", ref))
}

// readNodeCertFiles 读一张节点证书的 PEM（crt＋key）。
func (n *nodeBackendStore) readNodeCertFiles(ref int64) ([]byte, []byte, error) {
	crtPath, keyPath := n.nodeCertFilePaths(ref)
	crt, err := os.ReadFile(crtPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return crt, key, nil
}

// parseCertExpiryPEM 从证书 PEM 里解析到期时间（取第一个 CERTIFICATE 块）。
func parseCertExpiryPEM(certPEM []byte) (time.Time, error) {
	blk, _ := pem.Decode(certPEM)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return time.Time{}, fmt.Errorf("证书 PEM 解析失败")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("证书解析失败：%w", err)
	}
	return cert.NotAfter, nil
}

// ---------- ACME 签发（复用 acme.go 的三段流程） ----------

// issueNodeCertForRef 给指定 ref 的证书走一次完整 ACME DNS-01 签发
// （自动服务商专用，手动模式调这个会直接报错）：下单→按服务商写 TXT→
// 提交挑战→finalize 写 node-<ref>.crt/.key（都是 600 权限），返回
// 证书到期时间。调用方须持有 s.certMu（与面板手动签发／后台续签互斥）。
// 服务商按参数走（每张证书单独选，不跟面板 HTTPS 卡片的全局服务商走；
// 凭证共用同一份配置）。失败时文件可能没写或写了一半，调用方不要
// 因此建元数据；ref 本身已分配，不重用。
func (s *Server) issueNodeCertForRef(ref int64, domain, providerID string) (time.Time, error) {
	provider := dnsProviderFor(providerID)
	if provider.ID() == "manual" {
		return time.Time{}, fmt.Errorf("手动模式不能自动签发：请在节点证书页走两步签发（先下单拿 TXT 值，去 DNS 后台加上，再点继续验证）")
	}
	if !s.dnsProviderConfigured(provider.ID()) {
		return time.Time{}, fmt.Errorf("请先在设置页 DNS 服务商卡片填写 %s 的凭证", dnsProviderName(provider.ID()))
	}
	st, ac, err := s.startACMEOrder(domain)
	if err != nil {
		return time.Time{}, err
	}
	type cleanup struct {
		id, value string
	}
	var cleanups []cleanup
	defer func() {
		for _, c := range cleanups {
			_ = provider.Delete(s, domain, c.id, c.value)
		}
	}()
	for _, az := range st.Authz {
		recID, err := provider.Create(s, domain, az.TXTValue)
		if err != nil {
			return time.Time{}, err
		}
		cleanups = append(cleanups, cleanup{recID, az.TXTValue})
		// 等 DNS 传播（与 obtainACMECert 同口径）。
		acmeSleep(10 * time.Second)
		if err := submitAndPollChallenge(ac, az, false); err != nil {
			return time.Time{}, err
		}
	}
	crtPath, keyPath := s.nodes.nodeCertFilePaths(ref)
	if err := s.finalizeACMEOrderTo(ac, st, crtPath, keyPath, 0o600); err != nil {
		return time.Time{}, err
	}
	crt, err := os.ReadFile(crtPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("证书写盘后读不到：%v", err)
	}
	return parseCertExpiryPEM(crt)
}

// renewNodeCertNow 一张证书立即续签（自动服务商）：用存下的 provider
// 与凭证重走签发流程，成功后更新元数据、把用到该证书的节点的配置
// 版本都 bump 一下（agent 60 秒内拉到新证书）。手动模式的证书调
// 这个直接 400（要人加 TXT，自动续不了；手动续签请重新走两步签发，
// 会签一张新证书）。调用方须持有 s.certMu。
func (s *Server) renewNodeCertNow(ref int64) error {
	s.nodes.mu.Lock()
	cert := s.nodes.nodeCertByRef(ref)
	if cert == nil {
		s.nodes.mu.Unlock()
		return errNodeCertNotFound
	}
	domain, providerID := cert.Domain, cert.Provider
	s.nodes.mu.Unlock()
	if providerID == "manual" {
		return fmt.Errorf("手动模式的证书不能自动续签：请在节点证书页重新走手动签发（会签一张新证书，旧证书到期后删除）")
	}
	if !s.dnsProviderConfigured(providerID) {
		return fmt.Errorf("请先在设置页 DNS 服务商卡片填写 %s 的凭证", dnsProviderName(providerID))
	}
	now := time.Now()
	expiry, err := s.issueNodeCertForRef(ref, domain, providerID)
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	nc := s.nodes.nodeCertByRef(ref)
	if nc == nil {
		return errNodeCertNotFound
	}
	if err != nil {
		nc.LastError = err.Error()
		_ = s.nodes.saveLocked()
		return err
	}
	nc.IssuedAt = now.Unix()
	nc.ExpiresAt = expiry.Unix()
	nc.LastError = ""
	nc.RenewWarned = false
	s.nodes.bumpNodesUsingCertDomainLocked(domain)
	return s.nodes.saveLocked()
}

var errNodeCertNotFound = errors.New("节点证书不存在")

// maybeRenewNodeCerts 节点证书自动续签（v1.4.12，切片 7）：每天由
// certLoop 调一次（与 maybeRenewPanelCert 同节奏，不新开后台任务与
// 轮询）。到期前 30 天自动续签（用存下的 provider 与凭证重走签发
// 流程）；手动模式的跳过（要人加 TXT，自动续不了）；续签成功后用到
// 该证书的节点配置版本 bump（agent 60 秒内拉到新证书）；续失败记
// LastError 并发一次 Telegram（同一张证书只提醒一次，成功后清掉）。
func (s *Server) maybeRenewNodeCerts() {
	now := time.Now()
	s.nodes.mu.Lock()
	type due struct {
		ref      int64
		domain   string
		provider string
	}
	var dues []due
	for i := range s.nodes.data.NodeCerts {
		c := &s.nodes.data.NodeCerts[i]
		if c.Provider == "manual" {
			continue
		}
		if !shouldRenewNodeCert(time.Unix(c.ExpiresAt, 0), now) {
			continue
		}
		dues = append(dues, due{ref: c.Ref, domain: c.Domain, provider: c.Provider})
	}
	s.nodes.mu.Unlock()
	for _, d := range dues {
		s.certMu.Lock()
		expiry, err := s.issueNodeCertForRef(d.ref, d.domain, d.provider)
		s.certMu.Unlock()
		s.nodes.mu.Lock()
		nc := s.nodes.nodeCertByRef(d.ref)
		if nc == nil {
			// 续签期间证书被删了：不留痕迹。
			s.nodes.mu.Unlock()
			continue
		}
		if err != nil {
			nc.LastError = err.Error()
			warn := !nc.RenewWarned
			if warn {
				nc.RenewWarned = true
			}
			_ = s.nodes.saveLocked()
			s.nodes.mu.Unlock()
			if warn {
				s.sendTelegram("节点证书续签失败：域名 " + d.domain + " 的证书剩余不足 30 天，自动续签失败（" + err.Error() + "），请到节点页证书卡片点“立即续签”，或检查 " + dnsProviderName(d.provider) + " 凭证。")
			}
			continue
		}
		nc.IssuedAt = now.Unix()
		nc.ExpiresAt = expiry.Unix()
		nc.LastError = ""
		nc.RenewWarned = false
		s.nodes.bumpNodesUsingCertDomainLocked(d.domain)
		_ = s.nodes.saveLocked()
		s.nodes.mu.Unlock()
		s.sendTelegram("节点证书已自动续签：域名 " + d.domain + " 的证书已换成新的，节点 60 秒内拉到新证书。")
	}
}

// ---------- 管理接口（会话认证） ----------

// nodeCertRowForList 证书列表里的一行（状态与剩余天数现算）。
// v1.4.18：引用分两档——used_by_active 是实际绑定到这张证书的入站
// （正在使用），used_by_standby 是只靠泛域名覆盖匹配的入站（备用）；
// used_by 仍是两档加起来的覆盖列表（删证书拦截与旧口径不变）。
func (s *Server) nodeCertRowForList(c NodeCert, now time.Time) map[string]any {
	exp := time.Unix(c.ExpiresAt, 0)
	used, standby := s.nodes.nodeCertUsedBySplit(c.Ref, now)
	return map[string]any{
		"ref": c.Ref, "domain": c.Domain, "provider": c.Provider,
		"provider_name":   dnsProviderName(c.Provider),
		"issued_at":       time.Unix(c.IssuedAt, 0).UTC().Format(time.RFC3339),
		"expires_at":      exp.UTC().Format(time.RFC3339),
		"days_left":       nodeCertDaysLeft(exp, now),
		"status":          nodeCertStatus(exp, now),
		"last_error":      c.LastError,
		"used_by":         s.nodes.nodeCertUsedBy(c.Ref),
		"used_by_active":  used,
		"used_by_standby": standby,
	}
}

// nodeCertManualPendingStatus 进行中的节点证书手动签发订单（回填用）：
// 没有、不是节点单、已过期都返回 nil。
func (s *Server) nodeCertManualPendingStatus() map[string]any {
	p, err := s.loadPendingOrder()
	if err != nil || p.Kind != "node" {
		return nil
	}
	txtName, txtValue := "", ""
	if len(p.Authz) > 0 {
		txtName = p.Authz[0].TXTName
		txtValue = p.Authz[0].TXTValue
	}
	return map[string]any{
		"ref": p.Ref, "domain": p.Domain, "txt_name": txtName, "txt_value": txtValue,
		"expires_at": p.ExpiresAt.Format(time.RFC3339),
	}
}

// handleNodeCertsList GET /api/v1/node-certs：证书列表＋服务商配置
// 状态＋进行中的节点手动签发订单（有则回填 TXT 名与值）。
func (s *Server) handleNodeCertsList(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.nodes.mu.Lock()
	rows := make([]map[string]any, 0, len(s.nodes.data.NodeCerts))
	for _, c := range s.nodes.data.NodeCerts {
		rows = append(rows, s.nodeCertRowForList(c, now))
	}
	s.nodes.mu.Unlock()
	providers := make([]map[string]any, 0, len(dnsProviderIDs))
	for _, id := range dnsProviderIDs {
		providers = append(providers, map[string]any{
			"id": id, "name": dnsProviderName(id), "configured": s.dnsProviderConfigured(id),
		})
	}
	writeJSON(w, 200, okResp(map[string]any{
		"certs": rows, "providers": providers,
		"manual_pending": s.nodeCertManualPendingStatus(),
	}))
}

// handleNodeCertsIssue POST /api/v1/node-certs：按域名签发（自动服务商）。
// 手动模式不走这里（400，提示走两步签发）；同域名已有可用证书时 400
// （请用续签，不要重复签发）；只是覆盖关系时（已有泛域名再签其中一个
// 子域名，或已有单域名再签覆盖它的泛域名）只警告、不拦截，签发继续走，
// 返回里带 warning 字段，前端签发成功时 toast 显示。签发成功返回证书行；
// ACME 流程要几十秒，前端提交后等它返回（与面板 HTTPS 签发同款同步接口）。
func (s *Server) handleNodeCertsIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain   string `json:"domain"`
		Provider string `json:"provider"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	provider := strings.TrimSpace(req.Provider)
	if domain == "" || !validNodeCertDomain(domain) {
		writeJSON(w, 400, errResp("域名格式不对"))
		return
	}
	if !validDNSProvider(provider) {
		writeJSON(w, 400, errResp("服务商不对：应为 cloudflare / aliyun / dnspod / route53 / godaddy / namecheap / manual"))
		return
	}
	if provider == "manual" {
		writeJSON(w, 400, errResp("手动模式请走两步签发：先点“开始手动签发”拿 TXT 值，去 DNS 后台加上，再点“继续验证”"))
		return
	}
	if !s.dnsProviderConfigured(provider) {
		writeJSON(w, 400, errResp("请先在设置页 DNS 服务商卡片填写 "+dnsProviderName(provider)+" 的凭证"))
		return
	}
	s.nodes.mu.Lock()
	if dup, _ := s.nodes.nodeCertDupCheckLocked(domain, time.Now()); dup != nil {
		ref, exp := dup.Ref, time.Unix(dup.ExpiresAt, 0).Format("2006-01-02")
		s.nodes.mu.Unlock()
		writeJSON(w, 400, errResp(fmt.Sprintf("域名 %s 已有可用证书（ref %d，到期 %s），请用“立即续签”，不要重复签发", domain, ref, exp)))
		return
	}
	_, warning := s.nodes.nodeCertDupCheckLocked(domain, time.Now())
	ref := s.nodes.nextCertRefLocked()
	if err := s.nodes.saveLocked(); err != nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.nodes.mu.Unlock()
	s.certMu.Lock()
	expiry, err := s.issueNodeCertForRef(ref, domain, provider)
	s.certMu.Unlock()
	if err != nil {
		writeJSON(w, 500, errResp("签发失败："+err.Error()))
		return
	}
	now := time.Now()
	s.nodes.mu.Lock()
	s.nodes.data.NodeCerts = append(s.nodes.data.NodeCerts, NodeCert{
		Ref: ref, Domain: domain, Provider: provider,
		IssuedAt: now.Unix(), ExpiresAt: expiry.Unix(),
	})
	row := s.nodeCertRowForList(s.nodes.data.NodeCerts[len(s.nodes.data.NodeCerts)-1], now)
	if err := s.nodes.saveLocked(); err != nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.nodes.mu.Unlock()
	s.audit(s.currentUser(r), "签发节点证书", fmt.Sprintf("域名 %s（ref %d，%s）", domain, ref, dnsProviderName(provider)), "成功", "")
	resp := map[string]any{"cert": row}
	if warning != "" {
		resp["warning"] = warning
	}
	writeJSON(w, 200, okResp(resp))
}

// handleNodeCertsRenew POST /api/v1/node-certs/{ref}/renew：立即续签。
func (s *Server) handleNodeCertsRenew(w http.ResponseWriter, r *http.Request) {
	ref, err := strconv.ParseInt(r.PathValue("ref"), 10, 64)
	if err != nil || ref <= 0 {
		writeJSON(w, 400, errResp("证书 ref 不对"))
		return
	}
	s.nodes.mu.Lock()
	cert := s.nodes.nodeCertByRef(ref)
	if cert == nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 404, errResp("节点证书不存在"))
		return
	}
	domain, provider := cert.Domain, cert.Provider
	s.nodes.mu.Unlock()
	if provider == "manual" {
		writeJSON(w, 400, errResp("手动模式的证书不能自动续签：请在节点证书页重新走手动签发（会签一张新证书，旧证书到期后删除）"))
		return
	}
	s.certMu.Lock()
	err = s.renewNodeCertNow(ref)
	s.certMu.Unlock()
	if err != nil {
		if errors.Is(err, errNodeCertNotFound) {
			writeJSON(w, 404, errResp("节点证书不存在"))
			return
		}
		writeJSON(w, 500, errResp("续签失败："+err.Error()))
		return
	}
	s.audit(s.currentUser(r), "续签节点证书", fmt.Sprintf("域名 %s（ref %d）", domain, ref), "成功", "用到该证书的节点配置版本已 bump，60 秒内拉到新证书")
	s.nodes.mu.Lock()
	nc := s.nodes.nodeCertByRef(ref)
	row := s.nodeCertRowForList(*nc, time.Now())
	s.nodes.mu.Unlock()
	writeJSON(w, 200, okResp(map[string]any{"cert": row}))
}

// handleNodeCertsDelete DELETE /api/v1/node-certs/{ref}：删除证书。
// 正在被入站引用的拦住（400 并列出引用它的节点／入站）：删了证书那
// 些入站就起不来了（agent fail-closed 跳过），宁可拦住让用户先删
// 入站或换域名。没被引用时删元数据与证书文件。
func (s *Server) handleNodeCertsDelete(w http.ResponseWriter, r *http.Request) {
	ref, err := strconv.ParseInt(r.PathValue("ref"), 10, 64)
	if err != nil || ref <= 0 {
		writeJSON(w, 400, errResp("证书 ref 不对"))
		return
	}
	if !s.tryOpLock(w) {
		return
	}
	defer s.opMu.Unlock()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	cert := s.nodes.nodeCertByRef(ref)
	if cert == nil {
		writeJSON(w, 404, errResp("节点证书不存在"))
		return
	}
	if used := s.nodes.nodeCertUsedBy(ref); len(used) > 0 {
		writeJSON(w, 400, map[string]any{
			"ok": false, "error": "证书正被入站引用，不能删：" + strings.Join(used, "、"),
			"used_by": used,
		})
		return
	}
	domain := cert.Domain
	kept := s.nodes.data.NodeCerts[:0]
	for _, c := range s.nodes.data.NodeCerts {
		if c.Ref != ref {
			kept = append(kept, c)
		}
	}
	s.nodes.data.NodeCerts = kept
	if err := s.nodes.saveLocked(); err != nil {
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	crtPath, keyPath := s.nodes.nodeCertFilePaths(ref)
	_ = os.Remove(crtPath)
	_ = os.Remove(keyPath)
	s.audit(s.currentUser(r), "删除节点证书", fmt.Sprintf("域名 %s（ref %d）", domain, ref), "成功", "")
	writeJSON(w, 200, okResp(map[string]any{"deleted_ref": ref}))
}

// ---------- 节点证书手动模式两步走（v1.4.12） ----------
//
// 面板手动签发的 pending 是全局单文件（certs/acme-pending.json，
// 一次只能有一单）：节点手动签发与面板手动签发串行化——任一进行中
// 时另一边开始就 400（提示先完成或取消那一单）。pending 里 Kind
// 区分归属（"panel"／"node"，老文件没有 Kind 的按面板算），Ref 是
// 节点证书的 ref（面板单没有）。两边确认／取消时都先核 Kind，
// 错归属的单不许动对方的订单。

// handleNodeCertsManualStart 手动签发第一步：下单并返回 TXT 记录名
// 与值，同时把 pending 订单落盘（1 小时有效，面板重启后还在）。
func (s *Server) handleNodeCertsManualStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" || !validNodeCertDomain(domain) {
		writeJSON(w, 400, errResp("域名格式不对"))
		return
	}
	if s.dnsProviderID() != "manual" {
		writeJSON(w, 400, errResp("当前 DNS 服务商不是手动模式：请先在设置页把服务商切成“手动添加 TXT”"))
		return
	}
	if p, err := s.loadPendingOrder(); err == nil {
		writeJSON(w, 400, errResp(fmt.Sprintf("已有进行中的手动签发（域名 %s），请先完成或取消它", p.Domain)))
		return
	}
	s.nodes.mu.Lock()
	if dup, _ := s.nodes.nodeCertDupCheckLocked(domain, time.Now()); dup != nil {
		ref, exp := dup.Ref, time.Unix(dup.ExpiresAt, 0).Format("2006-01-02")
		s.nodes.mu.Unlock()
		writeJSON(w, 400, errResp(fmt.Sprintf("域名 %s 已有可用证书（ref %d，到期 %s），请用“立即续签”，不要重复签发", domain, ref, exp)))
		return
	}
	_, warning := s.nodes.nodeCertDupCheckLocked(domain, time.Now())
	ref := s.nodes.nextCertRefLocked()
	if err := s.nodes.saveLocked(); err != nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.nodes.mu.Unlock()
	s.certMu.Lock()
	defer s.certMu.Unlock()
	st, _, err := s.startACMEOrder(domain)
	if err != nil {
		writeJSON(w, 500, errResp("下单失败："+err.Error()))
		return
	}
	now := time.Now()
	pend := &acmePendingOrder{
		Version: 1, Kind: "node", Ref: ref, Domain: domain,
		OrderURL: st.OrderURL, FinalizeURL: st.FinalizeURL,
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
	s.audit(s.currentUser(r), "节点证书手动签发第一步", fmt.Sprintf("域名 %s（ref %d）", domain, ref), "成功", "订单已下，待用户加 TXT 记录")
	txtName, txtValue := "", ""
	if len(pend.Authz) > 0 {
		txtName = pend.Authz[0].TXTName
		txtValue = pend.Authz[0].TXTValue
	}
	resp := map[string]any{
		"ref": ref, "domain": domain, "txt_name": txtName, "txt_value": txtValue,
		"expires_at": pend.ExpiresAt.Format(time.RFC3339),
	}
	if warning != "" {
		resp["warning"] = warning
	}
	writeJSON(w, 200, okResp(resp))
}

// handleNodeCertsManualConfirm 手动签发第二步：读回 pending，先等
// TXT 生效（看不到就报错但不提交，pending 还能用），再提交挑战、
// finalize 写 node-<ref>.crt/.key（600），落元数据；成功后删 pending。
// 不碰面板 HTTPS 开关、不重启面板。
func (s *Server) handleNodeCertsManualConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ref    int64  `json:"ref"`
		Domain string `json:"domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, 400, errResp("请求格式不对"))
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" || !validNodeCertDomain(domain) {
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
	if pend.Kind != "node" {
		writeJSON(w, 400, errResp("当前进行中的手动签发是面板证书，不是节点证书：请先完成或取消它"))
		return
	}
	if pend.Ref != req.Ref || pend.Domain != domain {
		writeJSON(w, 400, errResp(fmt.Sprintf("待确认的订单是 ref %d、域名 %s，与请求不符：请先取消它再重新开始", pend.Ref, pend.Domain)))
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
	for _, az := range st.Authz {
		if err := waitForTXTVisible(az.TXTName, az.TXTValue); err != nil {
			writeJSON(w, 500, errResp(err.Error()))
			return
		}
	}
	for _, az := range st.Authz {
		if err := submitAndPollChallenge(ac, az, true); err != nil {
			if errors.Is(err, errACMEValidationFailed) {
				s.clearPendingOrder()
			}
			writeJSON(w, 500, errResp(err.Error()))
			return
		}
	}
	crtPath, keyPath := s.nodes.nodeCertFilePaths(pend.Ref)
	if err := s.finalizeACMEOrderTo(ac, st, crtPath, keyPath, 0o600); err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	crt, err := os.ReadFile(crtPath)
	if err != nil {
		writeJSON(w, 500, errResp("证书写盘后读不到："+err.Error()))
		return
	}
	expiry, err := parseCertExpiryPEM(crt)
	if err != nil {
		writeJSON(w, 500, errResp(err.Error()))
		return
	}
	s.clearPendingOrder()
	now := time.Now()
	s.nodes.mu.Lock()
	s.nodes.data.NodeCerts = append(s.nodes.data.NodeCerts, NodeCert{
		Ref: pend.Ref, Domain: domain, Provider: "manual",
		IssuedAt: now.Unix(), ExpiresAt: expiry.Unix(),
	})
	row := s.nodeCertRowForList(s.nodes.data.NodeCerts[len(s.nodes.data.NodeCerts)-1], now)
	if err := s.nodes.saveLocked(); err != nil {
		s.nodes.mu.Unlock()
		writeJSON(w, 500, errResp("保存失败："+err.Error()))
		return
	}
	s.nodes.mu.Unlock()
	s.audit(s.currentUser(r), "节点证书手动签发第二步", fmt.Sprintf("域名 %s（ref %d）", domain, pend.Ref), "成功", "真证书已签下，节点 60 秒内拉到")
	writeJSON(w, 200, okResp(map[string]any{"cert": row}))
}

// handleNodeCertsManualCancel 取消进行中的节点证书手动签发（删 pending 文件）。
func (s *Server) handleNodeCertsManualCancel(w http.ResponseWriter, r *http.Request) {
	pend, err := s.loadPendingOrder()
	if err != nil {
		writeJSON(w, 400, errResp(err.Error()))
		return
	}
	if pend.Kind != "node" {
		writeJSON(w, 400, errResp("当前进行中的手动签发是面板证书，不是节点证书：请到设置页 HTTPS 卡片取消它"))
		return
	}
	s.clearPendingOrder()
	s.audit(s.currentUser(r), "取消节点证书手动签发", pend.Domain, "成功", "")
	writeJSON(w, 200, okResp(nil))
}
