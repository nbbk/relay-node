package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// runScript 在 KIT_DIR 下跑 bash <script> <args...>，stdin=/dev/null，超时 300 秒。
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
	addr := fmt.Sprintf("127.0.0.1:%d", listenPort)
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

func renewModeFor(l Landing, share443 bool) (string, bool) {
	for _, p := range l.Ports {
		if p.Proto == "tcp" && p.Listen == 80 && p.Remote == 80 {
			return "自动（走 80）", false
		}
	}
	if share443 {
		return "自动（走 443 SNI 分流）", false
	}
	// 没配 80/443 时：明文落地无证书，返回自动（无证书）
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

func (s *Server) sendTelegram(msg string) {
	if !s.cfg.Telegram.Enabled || s.cfg.Telegram.BotToken == "" || s.cfg.Telegram.ChatID == "" {
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

func qrDataURI(link string) (string, error) {
	if !commandExists("qrencode") {
		return "", fmt.Errorf("本机无 qrencode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "qrencode", "-o", "-", "-t", "PNG", "--", link)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	// base64 编码
	return "data:image/png;base64," + base64Encode(out.Bytes()), nil
}

func base64Encode(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		sb.WriteByte(enc[(n>>18)&63])
		sb.WriteByte(enc[(n>>12)&63])
		if rem > 1 {
			sb.WriteByte(enc[(n>>6)&63])
		} else {
			sb.WriteByte('=')
		}
		if rem > 2 {
			sb.WriteByte(enc[n&63])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}

// ---------- nodes.txt 解析 ----------

type linkGroup struct {
	LandingIndex int               `json:"landing_index"`
	Landing      string            `json:"landing"`
	Links        []labelledLink    `json:"links"`
	QR           map[string]string `json:"qr"`
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
		if strings.HasPrefix(line, "直连：") && cur != nil {
			cur.Links = append(cur.Links, labelledLink{Label: "直连", Link: strings.TrimSpace(strings.TrimPrefix(line, "直连："))})
			continue
		}
		_ = pendingLabel
	}
	return groups
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
