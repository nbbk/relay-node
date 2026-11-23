package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

// ---------- 日志轮转（防爆盘） ----------

// 面板自己写的文件（audit.log、realm.log）没有系统轮转，
// 写之前判大小，超限就轮转，旧份只留最近 3 个。
const (
	logRotateMaxBytes = 10 << 20 // 10MB
	logRotateKeep     = 3        // 保留 .1/.2/.3
)

func rotateLogIfLarge(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() <= logRotateMaxBytes {
		return
	}
	// 先删最旧的，再逐个往后挪一位，最后把当前文件挪成 .1
	_ = os.Remove(path + fmt.Sprintf(".%d", logRotateKeep))
	for i := logRotateKeep - 1; i >= 1; i-- {
		_ = os.Rename(path+fmt.Sprintf(".%d", i), path+fmt.Sprintf(".%d", i+1))
	}
	_ = os.Rename(path, path+".1")
}

// ---------- JSON 响应 ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func okResp(extra map[string]any) map[string]any {
	m := map[string]any{"ok": true}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func errResp(msg string) map[string]any { return map[string]any{"ok": false, "error": msg} }

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ---------- 命令执行（参数数组，绝不拼 shell） ----------

type cmdResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func runCmd(env []string, timeout time.Duration, name string, args ...string) error {
	_, err := runCmdOut(env, timeout, name, args...)
	return err
}

func runCmdOut(env []string, timeout time.Duration, name string, args ...string) (cmdResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := cmdResult{Stdout: out.String(), Stderr: errb.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("命令超时（%s）", name)
	}
	return res, err
}

// runScript 在 KIT_DIR 下跑 bash <script> <args...>，stdin=/dev/null，
// 超时由调用方传。
func (s *Server) runScript(script string, timeout time.Duration, env []string, args ...string) (cmdResult, error) {
	full := []string{filepath.Join(s.env.KitDir, script)}
	full = append(full, args...)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", full...)
	cmd.Dir = s.env.KitDir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = nil
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := cmdResult{Stdout: out.String(), Stderr: errb.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("脚本超时：%s", script)
	}
	return res, err
}

func hasSystemd() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// ---------- 1.3.44：更新进程与面板服务脱钩 ----------

// updateTimeout1344 后台更新超时（v1.3.44）：小机器上下 Go 包（71M）+
// 编译常超 300 秒，之前一到 300 秒就被 SIGKILL，/tmp 临时目录来不及清理。
const updateTimeout1344 = 900 * time.Second

// scopeRunnerAvailable1344（v1.3.44）：更新能不能走 systemd-run --scope。
// 默认检查三点：有 systemd、systemd-run 在 PATH 里、能起一个空 scope。
// 包级变量，测试可替换成假实现。
var scopeRunnerAvailable1344 = func() bool {
	if !hasSystemd() || !commandExists("systemd-run") {
		return false
	}
	_, err := runCmdOut(nil, 10*time.Second, "systemd-run", "--scope", "--quiet", "true")
	return err == nil
}

// updateCommand1344（v1.3.44）：后台更新要跑的命令与超时。有 systemd 时用
// systemd-run --scope 起 transient scope 跑安装脚本：scope 在面板服务的
// cgroup 之外，脚本末尾 systemctl restart relay-panel 杀不到它；就算
// 900 秒超时杀掉的也只是 systemd-run 客户端，scope 里跑着的更新继续跑。
// 没有 systemd 或 systemd-run 不可用时回退原来的直接跑法。
func (s *Server) updateCommand1344() (name string, args []string, timeout time.Duration) {
	script := filepath.Join(s.env.KitDir, "panel-install.sh")
	timeout = updateTimeout1344
	if scopeRunnerAvailable1344() {
		return "systemd-run", []string{"--scope", "--quiet", "bash", script}, timeout
	}
	return "bash", []string{script}, timeout
}

// runUpdate1344（v1.3.44）：按 updateCommand1344 跑一次后台更新。
func (s *Server) runUpdate1344() (cmdResult, error) {
	name, args, timeout := s.updateCommand1344()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = s.env.KitDir
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := cmdResult{Stdout: out.String(), Stderr: errb.String()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("脚本超时：panel-install.sh")
	}
	return res, err
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ---------- realm 服务 ----------

func realmRunning() bool {
	if hasSystemd() {
		return runCmd(nil, 5*time.Second, "systemctl", "is-active", "--quiet", "realm") == nil
	}
	_, err := runCmdOut(nil, 5*time.Second, "pgrep", "-x", "realm")
	return err == nil
}

// realmInstalled 本机装没装 realm（二进制或 systemd 单元存在就算装了）：
// 只装了面板、还没装中转时两个都没有，realm 健康检查没东西可监控。
func realmInstalled() bool {
	if _, err := os.Stat("/usr/local/bin/realm"); err == nil {
		return true
	}
	if _, err := os.Stat("/etc/systemd/system/realm.service"); err == nil {
		return true
	}
	return false
}

// realmInstalledFunc 检测本机是否装了 realm（测试可替换为桩；
// 拨号函数 probeDial 也是同款包级变量做法）。
var realmInstalledFunc = realmInstalled

func realmVersion() string {
	if r, err := runCmdOut(nil, 5*time.Second, "/usr/local/bin/realm", "--version"); err == nil {
		return strings.TrimSpace(strings.Split(r.Stdout, "\n")[0])
	}
	return ""
}

func realmUptimeSeconds() int64 {
	if r, err := runCmdOut(nil, 5*time.Second, "pgrep", "-x", "realm"); err == nil {
		pid := strings.TrimSpace(strings.Split(r.Stdout, "\n")[0])
		if pid != "" {
			if st, err := os.Stat("/proc/" + pid); err == nil {
				return int64(time.Since(st.ModTime()).Seconds())
			}
		}
	}
	return 0
}

func bbrEnabled() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv4/tcp_congestion_control")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "bbr"
}

// restartRealm 重启 realm：优先 systemctl，无 systemd 时 pkill + nohup 兜底（与 stop.sh 同款）。
func (s *Server) restartRealm() error {
	if hasSystemd() {
		if err := runCmd(nil, 30*time.Second, "systemctl", "restart", "realm"); err != nil {
			return fmt.Errorf("systemctl restart realm 失败：%v", err)
		}
		time.Sleep(time.Second)
		if !realmRunning() {
			return fmt.Errorf("realm 重启后没跑起来")
		}
		return nil
	}
	_, _ = runCmdOut(nil, 5*time.Second, "pkill", "-x", "realm")
	time.Sleep(time.Second)
	_, _ = runCmdOut(nil, 5*time.Second, "pkill", "-9", "-x", "realm")
	cmd := exec.Command("nohup", "/usr/local/bin/realm", "-c", filepath.Join(s.env.RealmDir, "config.toml"))
	rotateLogIfLarge("/var/log/realm.log") // 超 10MB 就轮转，旧份只留 3 个
	f, err := os.OpenFile("/var/log/realm.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer f.Close()
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 realm 失败：%v", err)
	}
	time.Sleep(time.Second)
	if !realmRunning() {
		return fmt.Errorf("realm 启动后没跑起来")
	}
	return nil
}

func (s *Server) controlRealm(action string) error {
	switch action {
	case "start":
		if hasSystemd() {
			return runCmd(nil, 30*time.Second, "systemctl", "start", "realm")
		}
		return s.restartRealm()
	case "stop":
		if hasSystemd() {
			_ = runCmd(nil, 30*time.Second, "systemctl", "stop", "realm")
		}
		_, _ = runCmdOut(nil, 5*time.Second, "pkill", "-x", "realm")
		return nil
	case "restart":
		return s.restartRealm()
	}
	return fmt.Errorf("未知操作：%s", action)
}

// verifyPorts 验端口：ss 查各监听端口，3 次重试。
func verifyPorts(ports []PortSpec) error {
	for attempt := 0; attempt < 3; attempt++ {
		listening := listeningPorts()
		missing := []string{}
		for _, p := range ports {
			if p.Listen == 9 {
				continue // 占位端口不验
			}
			key := fmt.Sprintf("%s:%d", p.Proto, p.Listen)
			if !listening[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		} else {
			return fmt.Errorf("端口没监听：%s", strings.Join(missing, ", "))
		}
	}
	return nil
}

// listeningPorts 读 ss 或 /proc/net，返回 "tcp:443"→true。
func listeningPorts() map[string]bool {
	out := map[string]bool{}
	if r, err := runCmdOut(nil, 5*time.Second, "ss", "-tln"); err == nil {
		for _, line := range strings.Split(r.Stdout, "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 {
				if p := portFromAddr(f[3]); p > 0 {
					out[fmt.Sprintf("tcp:%d", p)] = true
				}
			}
		}
	}
	if r, err := runCmdOut(nil, 5*time.Second, "ss", "-uln"); err == nil {
		for _, line := range strings.Split(r.Stdout, "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 {
				if p := portFromAddr(f[3]); p > 0 {
					out[fmt.Sprintf("udp:%d", p)] = true
				}
			}
		}
	}
	if len(out) == 0 {
		// /proc/net 回退
		for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			if b, err := os.ReadFile(file); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					f := strings.Fields(line)
					if len(f) >= 4 && f[3] == "0A" {
						parts := strings.Split(f[1], ":")
						if len(parts) == 2 {
							if n, err := strconv.ParseInt(parts[1], 16, 32); err == nil {
								out[fmt.Sprintf("tcp:%d", n)] = true
							}
						}
					}
				}
			}
		}
		for _, file := range []string{"/proc/net/udp", "/proc/net/udp6"} {
			if b, err := os.ReadFile(file); err == nil {
				for _, line := range strings.Split(string(b), "\n") {
					f := strings.Fields(line)
					if len(f) >= 2 {
						parts := strings.Split(f[1], ":")
						if len(parts) == 2 {
							if n, err := strconv.ParseInt(parts[1], 16, 32); err == nil {
								out[fmt.Sprintf("udp:%d", n)] = true
							}
						}
					}
				}
			}
		}
	}
	return out
}

func portFromAddr(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	p, _ := strconv.Atoi(addr[i+1:])
	return p
}

// ---------- 证书 ----------

type certInfo struct {
	DaysLeft *int   `json:"days_left"`
	Expiry   string `json:"expiry"`
}

// fetchCert 经中转连落地监听端口拿证书算剩余天数（TLS 握手，不验证）。
func fetchCert(listenPort int, sni string) certInfo {
	return fetchCertFrom(fmt.Sprintf("127.0.0.1:%d", listenPort), sni)
}

// fetchCertFrom 对任意地址握手拿证书（v1.3.46）：fetchCert 只探本机中转端口，
// tls_unreachable 分段定位里直连落地对比要直接连落地地址。拿不到返回空 certInfo。
func fetchCertFrom(addr, sni string) certInfo {
	d := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(d, "tcp", addr, &tls.Config{InsecureSkipVerify: true, ServerName: sni})
	if err != nil {
		return certInfo{}
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return certInfo{}
	}
	exp := st.PeerCertificates[0].NotAfter
	days := int(time.Until(exp).Hours() / 24)
	return certInfo{DaysLeft: &days, Expiry: exp.Format("2006-01-02")}
}

// tcpPortOpen TCP 连通探测（v1.3.46，tls_unreachable 分段定位第 ① 步）。
func tcpPortOpen(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// pickTLSEndpoint 找落地的 TLS 端点（v1.3.46，与 check.sh pick_tls_endpoint
// 前两档同规则：优先监听 443，其次落地 443，再取第一个 TCP 监听端口；
// 一个都没监听时返回第一个 TCP 端口（下游 TCP 探测会报“没人监听”），
// 没有 TCP 端口返回 ok=false）。
func pickTLSEndpoint(l Landing) (listen, remote int, ok bool) {
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Listen == 443 {
			return p.Listen, p.Remote, true
		}
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Remote == 443 {
			return p.Listen, p.Remote, true
		}
	}
	for _, p := range l.Ports {
		if p.Proto != "tcp" {
			continue
		}
		if tcpPortOpen("127.0.0.1", p.Listen, 2*time.Second) {
			return p.Listen, p.Remote, true
		}
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" {
			return p.Listen, p.Remote, true
		}
	}
	return 0, 0, false
}

// listenerProcName 查 TCP 端口的监听进程名（v1.3.46，tls_unreachable 分段定位
// 第 ② 步）：跑 ss -tlnp 解析 users:(("进程名",...))；ss 不在或查不到返回 ""。
func listenerProcName(port int) string {
	res, err := runCmdOut(nil, 5*time.Second, "ss", "-tlnp")
	if err != nil {
		return ""
	}
	pat := ":" + strconv.Itoa(port)
	for _, line := range strings.Split(res.Stdout, "\n") {
		idx := strings.Index(line, pat)
		if idx < 0 {
			continue
		}
		// 端口后必须跟空白，避免 2444 撞上 24443 这类前缀。
		after := line[idx+len(pat):]
		if after == "" || (after[0] != ' ' && after[0] != '\t') {
			continue
		}
		u := strings.Index(line, `users:(("`)
		if u < 0 {
			continue
		}
		rest := line[u+len(`users:(("`):]
		if end := strings.Index(rest, `"`); end > 0 {
			return rest[:end]
		}
	}
	return ""
}

// probeCertForLanding 按落地实际的 TCP 监听端口逐个握手拿证书（443 优先，
// 其余按 PORTS 顺序，并行探测、先拿到先返回），任一端口拿到证书就算有证书。
// 自定义端口 TLS 落地（如 24443）以前只认 443，根本不去握手，所以卡片上
// 永远显示无证书；现在按实际端口探。SNI 用证书域名，没有就用落地域名。
func probeCertForLanding(l Landing) (certInfo, bool) {
	sni := l.CertDomain
	if sni == "" {
		sni = l.Domain
	}
	ports := make([]int, 0, len(l.Ports))
	seen := map[int]bool{}
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Listen == 443 && !seen[443] {
			ports = append(ports, 443)
			seen[443] = true
		}
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" && !seen[p.Listen] {
			ports = append(ports, p.Listen)
			seen[p.Listen] = true
		}
	}
	if len(ports) == 0 {
		return certInfo{}, false
	}
	ch := make(chan certInfo, len(ports))
	for _, port := range ports {
		go func(port int) { ch <- fetchCert(port, sni) }(port)
	}
	timeout := time.After(6 * time.Second)
	for range ports {
		select {
		case ci := <-ch:
			if ci.DaysLeft != nil {
				return ci, true
			}
		case <-timeout:
			return certInfo{}, false
		}
	}
	return certInfo{}, false
}

// certModeText 证书签发方式的中文显示（审计清单用）。
func certModeText(m string) string {
	switch m {
	case "http01":
		return "默认走 80"
	case "dns01":
		return "DNS 验证"
	default:
		return "未设置"
	}
}

// formatMultiplier 倍率的审计显示：归一化后去尾零，如 1、2、2.5。
func formatMultiplier(m float64) string {
	return "×" + strconv.FormatFloat(normalizeTrafficMultiplier(m), 'f', -1, 64)
}

func renewModeFor(l Landing, share443 bool, hasCert bool) (string, bool) {
	// DNS-01：证书走 Cloudflare DNS 验证签发与续签，根本不走 80 端口，
	// 落地机 Caddy 会自动续；不能因为没 80 就判手动（智利落地 24443
	// 就是这么被误判的）。一律自动。
	if l.CertMode == "dns01" {
		return "自动（DNS 验证）", false
	}
	// 任意门模式：节点域名永远指着落地不动，续签直接走落地，不用改 A 记录。
	if l.Mode == "door" {
		return "自动（域名指落地，直接续签）", false
	}
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Listen == 80 && p.Remote == 80 {
			return "自动（走 80）", false
		}
	}
	if share443 {
		return "自动（走 443 SNI 分流）", false
	}
	// 握手拿到证书的就不是明文落地：续签走不通中转才要手动。
	if hasCert {
		return "手动（80 改了监听端口，见 README“手动续签证书”）", true
	}
	// 没拿到证书：有 443 端口的可能是 TLS 但暂时不通，也按手动算；
	// 只有 443 影子都没有的才判明文。
	hasTLS := false
	for _, p := range l.Ports {
		if p.Remote == 443 {
			hasTLS = true
		}
	}
	if !hasTLS {
		return "无证书（明文模式）", false
	}
	return "手动（80 改了监听端口，见 README“手动续签证书”）", true
}

// ---------- Telegram ----------

// telegramConfigured（v1.3.6）：Telegram 提醒通道是否配好（证书页没配时
// 提示去设置里配；没配时 sendTelegram 直接空操作，不报错）。
func (s *Server) telegramConfigured() bool {
	return s.cfg.Telegram.Enabled && s.cfg.Telegram.BotToken != "" && s.cfg.Telegram.ChatID != ""
}

func (s *Server) sendTelegram(msg string) {
	if !s.telegramConfigured() {
		return
	}
	body, _ := json.Marshal(map[string]any{"chat_id": s.cfg.Telegram.ChatID, "text": msg})
	url := "https://api.telegram.org/bot" + s.cfg.Telegram.BotToken + "/sendMessage"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

// ---------- QR ----------

// qrDataURI 用纯 Go 生成二维码 PNG（data URI），不依赖主机装 qrencode。
// 以前是 shell 出去调 qrencode，中转机上没装就出不了图；现在面板自己就能出。
func qrDataURI(link string) (string, error) {
	png, err := qrcode.Encode(link, qrcode.Medium, 256)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// ---------- nodes.txt 解析 ----------

type linkGroup struct {
	LandingIndex int    `json:"landing_index"`
	Landing      string `json:"landing"`
	// Name 分组显示名：登记过的落地用 relay.conf 的显示名（NAME[_N]，
	// 没设就是落地地址），手动 twin 分组用落地地址。
	Name  string            `json:"name"`
	Mode  string            `json:"mode"`
	Links []labelledLink    `json:"links"`
	QR    map[string]string `json:"qr"`
}

type labelledLink struct {
	Label string `json:"label"`
	Link  string `json:"link"`
}

func parseNodesTxt(data []byte) []linkGroup {
	var groups []linkGroup
	var cur *linkGroup
	var pendingLabel string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## 落地 ") {
			// ## 落地 1：203.0.113.1（...）
			rest := strings.TrimPrefix(line, "## 落地 ")
			idxStr := rest
			if i := strings.Index(rest, "："); i >= 0 {
				idxStr = rest[:i]
			}
			idx, _ := strconv.Atoi(idxStr)
			landing := ""
			if i := strings.Index(rest, "："); i >= 0 {
				landing = strings.TrimSpace(rest[i+len("："):])
				if j := strings.Index(landing, "（"); j >= 0 {
					landing = landing[:j]
				}
			}
			groups = append(groups, linkGroup{LandingIndex: idx, Landing: landing, QR: map[string]string{}})
			cur = &groups[len(groups)-1]
			continue
		}
		if strings.HasPrefix(line, "[第 ") {
			pendingLabel = ""
			continue
		}
		if strings.HasPrefix(line, "中转：") && cur != nil {
			cur.Links = append(cur.Links, labelledLink{Label: "中转", Link: strings.TrimSpace(strings.TrimPrefix(line, "中转："))})
			continue
		}
		if strings.HasPrefix(line, "任意门：") && cur != nil {
			cur.Links = append(cur.Links, labelledLink{Label: "任意门", Link: strings.TrimSpace(strings.TrimPrefix(line, "任意门："))})
			continue
		}
		if strings.HasPrefix(line, "直连：") && cur != nil {
			cur.Links = append(cur.Links, labelledLink{Label: "直连", Link: strings.TrimSpace(strings.TrimPrefix(line, "直连："))})
			continue
		}
		_ = pendingLabel
	}
	// 模式按链接行推断：有“任意门”行就是任意门模式，否则标准模式。
	// handleLinks 还会用 relay.conf 的登记再校正一次（登记过的落地以登记为准）。
	for i := range groups {
		for _, l := range groups[i].Links {
			if l.Label == "任意门" {
				groups[i].Mode = "door"
				break
			}
		}
		if groups[i].Mode == "" {
			groups[i].Mode = "standard"
		}
	}
	return groups
}

// ---------- nodes.txt：twin 持久化 ----------

// twinGroupMarker 是面板 twin 生成的分组在 nodes.txt 分组头上的标记。
// install.sh --nodes 再生成时会原样保留带这个标记的分组，不洗掉。
const twinGroupMarker = "手动 twin"

type nodesGroupBlock struct {
	header  string // 完整分组头行，如 "## 落地 3：203.0.113.1（手动 twin）"
	index   int
	landing string
	twin    bool
	body    []string // 分组头之后、下一分组头之前的行（不含头行）
}

// splitNodesTxt 把 nodes.txt 按分组切开：preamble 是第一个分组头之前的行，
// 每个分组一块（头行 + 体行）。
func splitNodesTxt(data []byte) (preamble []string, groups []nodesGroupBlock) {
	if len(data) == 0 {
		return nil, nil
	}
	var cur *nodesGroupBlock
	flush := func() {
		if cur != nil {
			groups = append(groups, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## 落地 ") {
			flush()
			rest := strings.TrimPrefix(line, "## 落地 ")
			idxStr := rest
			if i := strings.Index(rest, "："); i >= 0 {
				idxStr = rest[:i]
			}
			idx, _ := strconv.Atoi(idxStr)
			landing := ""
			if i := strings.Index(rest, "："); i >= 0 {
				landing = strings.TrimSpace(rest[i+len("："):])
				if j := strings.Index(landing, "（"); j >= 0 {
					landing = landing[:j]
				}
			}
			cur = &nodesGroupBlock{
				header:  line,
				index:   idx,
				landing: landing,
				twin:    strings.Contains(line, twinGroupMarker),
			}
			continue
		}
		if cur != nil {
			cur.body = append(cur.body, line)
		} else {
			preamble = append(preamble, line)
		}
	}
	flush()
	return preamble, groups
}

// twinEntryLines 一条 twin 记录的行：[第 N 条] + 中转/任意门行 + 直连行。
func twinEntryLines(num int, relayLabel, relayLink, directLink string) []string {
	lines := []string{fmt.Sprintf("[第 %d 条]", num)}
	// v1.3.16：只生成一条时，空的那条不写行（直连行本来就只在非空时写，中转行同理）。
	if relayLink != "" {
		lines = append(lines, relayLabel+"："+relayLink)
	}
	if directLink != "" {
		lines = append(lines, "直连："+directLink)
	}
	return lines
}

// entryHasLink 一条记录里是否已含相同的中转/任意门链接或直连链接。
func entryHasLink(lines []string, relayLink, directLink string) bool {
	for _, line := range lines {
		for _, p := range []string{"中转：", "任意门：", "直连："} {
			if strings.HasPrefix(line, p) {
				v := strings.TrimSpace(strings.TrimPrefix(line, p))
				if (relayLink != "" && v == relayLink) || (directLink != "" && v == directLink) {
					return true
				}
			}
		}
	}
	return false
}

// upsertTwinEntry 在一个普通分组的体行里更新或追加一条 twin 记录：
// 已有相同链接的那一条就原地替换，否则在末尾追加一条（编号顺延）。
func upsertTwinEntry(body []string, relayLabel, relayLink, directLink string) []string {
	entryStart := -1
	replace := func(start, end int) []string {
		num := 1
		if start < len(body) {
			_, _ = fmt.Sscanf(body[start], "[第 %d 条]", &num)
		}
		out := append([]string{}, body[:start]...)
		out = append(out, twinEntryLines(num, relayLabel, relayLink, directLink)...)
		out = append(out, body[end:]...)
		return out
	}
	for i, line := range body {
		if strings.HasPrefix(line, "[第 ") {
			if entryStart >= 0 && entryHasLink(body[entryStart:i], relayLink, directLink) {
				return replace(entryStart, i)
			}
			entryStart = i
		}
	}
	if entryStart >= 0 && entryHasLink(body[entryStart:], relayLink, directLink) {
		return replace(entryStart, len(body))
	}
	n := 0
	for _, line := range body {
		if strings.HasPrefix(line, "[第 ") {
			n++
		}
	}
	out := append([]string{}, body...)
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, "")
	out = append(out, twinEntryLines(n+1, relayLabel, relayLink, directLink)...)
	out = append(out, "")
	return out
}

// upsertTwinLinks 把一组 twin 链接写进 nodes.txt（文件不存在则创建）：
// 按落地 IP 找分组；找到 twin 标记分组就原地替换它的链接行；
// 找到普通分组就追加一条（已有相同链接时不重复）；
// 找不到就追加新分组 ## 落地 N：<IP>（手动 twin），N=现有最大编号+1。
// 原子写：写临时文件 + 重命名，文件权限 0644。
func upsertTwinLinks(path, landingIP, landingRaw, relayLabel, relayLink, directLink string) error {
	return upsertLandingLinks(path, landingIP, landingRaw, 0, relayLabel, relayLink, directLink)
}

// upsertLandingLinks 与 upsertTwinLinks 同一套写法，区别是新分组的编号：
// index>0 时用该编号（已登记落地的真实编号，如加落地/补粘贴时传 relay.conf 里的编号），
// index<=0 时自动取最大编号+1（与 upsertTwinLinks 相同）。
// 指定编号已被别的落地占用时退回自动编号，不把 1.2.1 已知的撞号问题放大。
func upsertLandingLinks(path, landingIP, landingRaw string, index int, relayLabel, relayLink, directLink string) error {
	var data []byte
	if b, err := os.ReadFile(path); err == nil {
		data = b
	}
	preamble, groups := splitNodesTxt(data)
	matched := -1
	for i, g := range groups {
		if g.landing == landingIP || (landingRaw != "" && g.landing == landingRaw) {
			matched = i
			break
		}
	}
	twinNote := "# 手动 twin：面板节点链接页生成，install.sh --nodes 不会洗掉"
	if matched < 0 {
		idx := index
		if idx <= 0 {
			idx = maxGroupIndex(groups) + 1
		} else {
			for _, g := range groups {
				if g.index == idx {
					idx = maxGroupIndex(groups) + 1
					break
				}
			}
		}
		groups = append(groups, nodesGroupBlock{
			header:  fmt.Sprintf("## 落地 %d：%s（%s）", idx, landingIP, twinGroupMarker),
			index:   idx,
			landing: landingIP,
			twin:    true,
			body:    append([]string{twinNote, ""}, twinEntryLines(1, relayLabel, relayLink, directLink)...),
		})
	} else if groups[matched].twin {
		// twin 分组：原地替换链接行（重复 twin 不重复）
		groups[matched].body = append([]string{twinNote, ""}, twinEntryLines(1, relayLabel, relayLink, directLink)...)
	} else {
		groups[matched].body = upsertTwinEntry(groups[matched].body, relayLabel, relayLink, directLink)
	}
	return writeNodesTxt(path, data, preamble, groups)
}

func maxGroupIndex(groups []nodesGroupBlock) int {
	maxIdx := 0
	for _, g := range groups {
		if g.index > maxIdx {
			maxIdx = g.index
		}
	}
	return maxIdx
}

// writeNodesTxt 把分组写回 nodes.txt（原子写：临时文件 + 重命名，0644）。
func writeNodesTxt(path string, data []byte, preamble []string, groups []nodesGroupBlock) error {
	var sb strings.Builder
	for _, line := range preamble {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	for _, g := range groups {
		sb.WriteString(g.header)
		sb.WriteString("\n")
		for _, line := range g.body {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	out := sb.String()
	if len(data) == 0 {
		// 新文件：先写 install.sh --nodes 同款文件头
		out = "# relay-node 双节点（" + time.Now().UTC().Format(time.RFC3339) + "）\n" +
			"# 中转：原链接按实际监听端口走（监听端口改过时中转链接端口跟着改）；直连：地址换成落地 IP + sni=域名\n" +
			"# 任意门：地址换成中转地址、端口换成转发端口；直连：地址换成落地 IP + sni=域名\n" + out
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	// WriteFile 受 umask 影响，显式 Chmod 保证 0644
	_ = os.Chmod(tmp, 0o644)
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// findLinksGroup 按落地找 nodes.txt 分组：先精确比落地字符串，
// 落地是域名时再解析成 IP 比一次（twin 分组头写的是 IP）。
func findLinksGroup(groups []linkGroup, l Landing) *linkGroup {
	for i := range groups {
		if groups[i].Landing == l.Landing {
			return &groups[i]
		}
	}
	if !isIP(l.Landing) {
		if ips, err := net.LookupIP(l.Landing); err == nil {
			for _, ip := range ips {
				for i := range groups {
					if groups[i].Landing == ip.String() {
						return &groups[i]
					}
				}
			}
		}
	}
	return nil
}

// linksQR 给一组链接逐条生成二维码（纯 Go，不依赖主机 qrencode），
// 生成失败的那条跳过，不阻塞其他链接。
func linksQR(links []labelledLink) map[string]string {
	qr := map[string]string{}
	for _, l := range links {
		if q, err := qrDataURI(l.Link); err == nil {
			qr[l.Label] = q
		}
	}
	return qr
}

// modeNoteFor 按预设算 MODE_NOTE（与 install.sh collect_landing 里同一套文案），
// 编辑落地时面板自己写 relay.conf，不再跑安装脚本，所以这里要复刻一份。
func modeNoteFor(preset, sbMode string, door bool) string {
	var note string
	switch preset {
	case "1":
		note = "landing-node（Xray 核心）"
	case "2":
		if sbMode == "2" {
			note = "landing-node（sing-box 核心）明文模式（HY2/TUIC 不启用）"
		} else {
			note = "landing-node（sing-box 核心）TLS 模式"
		}
	default:
		note = "自定义端口"
	}
	if door {
		note += "（任意门）"
	}
	return note
}

// deriveSBMode 从端口反推预设 2 的 sing-box 模式：带 8443/8444 就是 TLS，
// 否则是明文；非预设 2 返回空串。编辑表单预填用。
func deriveSBMode(l Landing) string {
	if l.Preset != "2" {
		return ""
	}
	for _, p := range l.Ports {
		if p.Remote == 8443 || p.Remote == 8444 {
			return "1"
		}
	}
	return "2"
}

// portsToCustomString 把端口表转回编辑表单的自定义端口写法
// （"443,8388,8443:9443"，只取 TCP；监听与落地相同只写一个数）。
// 预设 3 的落地编辑时预填用。
func portsToCustomString(ports []PortSpec) string {
	var parts []string
	seen := map[string]bool{}
	for _, p := range ports {
		if p.Proto != "tcp" {
			continue
		}
		var s string
		if p.Listen == p.Remote {
			s = strconv.Itoa(p.Listen)
		} else {
			s = fmt.Sprintf("%d:%d", p.Listen, p.Remote)
		}
		if !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ",")
}

// hasUDPPorts 端口表里有没有 UDP 规格（编辑表单的 UDP 复选框预填用）。
func hasUDPPorts(ports []PortSpec) bool {
	for _, p := range ports {
		if p.Proto == "udp" {
			return true
		}
	}
	return false
}

// removeLinksGroup 从 nodes.txt 里删掉指定落地的分组块（编辑改地址后旧分组
// 的落地还是旧地址，删掉它再由 upsertLandingLinks 按新地址建新分组，
// 避免历史里同时出现新旧两个分组）。
func removeLinksGroup(path, landingIP string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // 文件不存在就没什么可删的
	}
	preamble, groups := splitNodesTxt(data)
	kept := groups[:0]
	removed := false
	for _, g := range groups {
		if g.landing == landingIP {
			removed = true
			continue
		}
		kept = append(kept, g)
	}
	if !removed {
		return nil
	}
	return writeNodesTxt(path, data, preamble, kept)
}

// removeLinksGroups 按文件序号（0 起，按文件顺序）从 nodes.txt 里删掉指定分组，
// 只删历史，不动转发与节点。序号越界返回错误；重复序号只算一次。
// 返回实际删掉的组数与这些组的落地地址（供审计日志写，不含链接本身）。
func removeLinksGroups(path string, ordinals []int) (int, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, fmt.Errorf("历史记录文件不存在或读不到")
	}
	preamble, groups := splitNodesTxt(data)
	if len(groups) == 0 {
		return 0, nil, fmt.Errorf("历史记录是空的，没什么可删")
	}
	drop := map[int]bool{}
	for _, o := range ordinals {
		if o < 0 || o >= len(groups) {
			return 0, nil, fmt.Errorf("序号 %d 越界（共 %d 组），请刷新页面再试", o, len(groups))
		}
		drop[o] = true
	}
	kept := make([]nodesGroupBlock, 0, len(groups))
	var landings []string
	for i, g := range groups {
		if drop[i] {
			landings = append(landings, g.landing)
			continue
		}
		kept = append(kept, g)
	}
	if len(landings) == 0 {
		return 0, nil, nil
	}
	if err := writeNodesTxt(path, data, preamble, kept); err != nil {
		return 0, nil, err
	}
	return len(landings), landings, nil
}

// doorMapPorts 任意门转发端口映射：转发端口（listen）= 落地端口（remote）+ 20000，
// +20000 超 65535 时依次试 +10000、+1000；三个偏移都超了就保留原 listen
// （install.sh 随后会明确报错“转发端口算不出来”，面板这里不静默丢端口）。
// remote 与 proto 不变。
func doorMapPorts(ports []PortSpec) []PortSpec {
	out := make([]PortSpec, len(ports))
	for i, p := range ports {
		out[i] = p
		for _, off := range []int{20000, 10000, 1000} {
			if nl := p.Remote + off; nl >= 1 && nl <= 65535 {
				out[i].Listen = nl
				break
			}
		}
	}
	return out
}

// setLinkHost 只换节点链接里的地址（host），端口、SNI、名字等都不动；换不了原样返回。
func setLinkHost(link, newHost string) string {
	newHost = strings.TrimSpace(newHost)
	if newHost == "" {
		return link
	}
	pl, err := parseNodeLink(link)
	if err != nil {
		return link
	}
	switch pl.Scheme {
	case "vless", "trojan", "hysteria2", "hy2", "tuic":
		u, err := url.Parse(strings.TrimSpace(link))
		if err != nil || u.Host == "" {
			return link
		}
		u.Host = hostPort(newHost, pl.Port)
		return u.String()
	case "ss":
		u, err := url.Parse(strings.TrimSpace(link))
		if err != nil {
			return link
		}
		if u.User != nil {
			// SIP002：ss://user@host:port
			u.Host = hostPort(newHost, pl.Port)
			return u.String()
		}
		// 没有 userinfo：可能是 ss://host:port（SIP002）或 ss://base64 传统形式。
		// 先试传统 base64：整段 host 解出来是 method:password@host:port。
		if u.Host != "" {
			if raw, err := decodeSSOpaque(u.Host); err == nil {
				if at := strings.LastIndex(string(raw), "@"); at >= 0 {
					if _, pt, err := net.SplitHostPort(string(raw[at+1:])); err == nil {
						nb := base64.RawURLEncoding.EncodeToString([]byte(string(raw[:at+1]) + net.JoinHostPort(newHost, pt)))
						out := "ss://" + nb
						if u.RawQuery != "" {
							out += "?" + u.RawQuery
						}
						if u.Fragment != "" {
							out += "#" + u.Fragment
						}
						return out
					}
				}
			}
			// 不是 base64 传统形式：按 SIP002 直接换 host
			u.Host = hostPort(newHost, pl.Port)
			return u.String()
		}
		// 传统 base64 形式：ss:base64(method:password@host:port)（无 //，Opaque 形式）
		raw, err := decodeSSOpaque(u.Opaque)
		if err != nil {
			return link
		}
		at := strings.LastIndex(string(raw), "@")
		if at < 0 {
			return link
		}
		_, pt, err := net.SplitHostPort(string(raw[at+1:]))
		if err != nil {
			return link
		}
		nb := base64.RawURLEncoding.EncodeToString([]byte(string(raw[:at+1]) + net.JoinHostPort(newHost, pt)))
		out := "ss://" + nb
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			out += "#" + u.Fragment
		}
		return out
	case "vmess":
		trimmed := strings.TrimSpace(link)
		idx := strings.Index(trimmed, "://")
		if idx < 0 {
			return link
		}
		rest := trimmed[idx+3:]
		frag := ""
		if i := strings.Index(rest, "#"); i >= 0 {
			frag = rest[i:]
			rest = rest[:i]
		}
		if i := strings.Index(rest, "?"); i >= 0 {
			rest = rest[:i]
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(rest, "="))
		if err != nil {
			raw, err = base64.StdEncoding.DecodeString(rest)
			if err != nil {
				return link
			}
		}
		var j map[string]any
		if err := json.Unmarshal(raw, &j); err != nil {
			return link
		}
		j["add"] = newHost
		nb, err := json.Marshal(j)
		if err != nil {
			return link
		}
		return "vmess://" + base64.RawURLEncoding.EncodeToString(nb) + frag
	}
	return link
}

// setLinkName 只换节点链接的备注名（vless/trojan/ss/hysteria2/tuic 是 # 后面的片段，
// vmess 是 JSON 里的 ps 字段），其余部分原样不动；换不了原样返回。
// 中文备注经 url.URL.String() 百分号编码（与 setLinkHost 同一套规则），
// 链接等价，客户端显示时会解码。
func setLinkName(link, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return link
	}
	pl, err := parseNodeLink(link)
	if err != nil {
		return link
	}
	switch pl.Scheme {
	case "vless", "trojan", "ss", "hysteria2", "hy2", "tuic":
		u, err := url.Parse(strings.TrimSpace(link))
		if err != nil {
			return link
		}
		u.Fragment = name
		return u.String()
	case "vmess":
		trimmed := strings.TrimSpace(link)
		b64raw := strings.TrimPrefix(trimmed, "vmess://")
		frag := ""
		if i := strings.Index(b64raw, "#"); i >= 0 {
			frag = b64raw[i:]
			b64raw = b64raw[:i]
		}
		b64raw = strings.SplitN(b64raw, "?", 2)[0]
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(b64raw, "="))
		if err != nil {
			raw, err = base64.StdEncoding.DecodeString(b64raw)
			if err != nil {
				return link
			}
		}
		var j map[string]any
		if err := json.Unmarshal(raw, &j); err != nil {
			return link
		}
		j["ps"] = name
		nb, err := json.Marshal(j)
		if err != nil {
			return link
		}
		return "vmess://" + base64.RawURLEncoding.EncodeToString(nb) + frag
	}
	return link
}

func hostPort(host string, port int) string {
	if port > 0 {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	return host
}

// decodeSSOpaque 解 ss 传统形式的 base64（先 URL-safe 无填充，再标准编码）。
func decodeSSOpaque(s string) ([]byte, error) {
	if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "=")); err == nil {
		return raw, nil
	}
	return base64.StdEncoding.DecodeString(s)
}

func (s *Server) nodesFilePath() string {
	// 优先 KIT_DIR/nodes.txt（install.sh --nodes 写到 $HOME/relay-node-kit/nodes.txt）
	for _, p := range []string{filepath.Join(s.env.KitDir, "nodes.txt"), filepath.Join(os.Getenv("HOME"), "relay-node-kit", "nodes.txt")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(s.env.KitDir, "nodes.txt")
}
