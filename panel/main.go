// relay-panel: relay-node 可视化面板后端（Go 单二进制，只用标准库）。
// 契约见 ~/workspace/relay-panel-build/api-contract.md。
// CLI: relay-panel [serve] | relay-panel version | relay-panel hash-password [密码] | relay-panel totp-disable
package main

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed VERSION
var versionFile string

// Version 面板版本（VERSION 文件内容，去空白）。
var Version = strings.TrimSpace(versionFile)

const (
	// LANDING_ADD_NODE_CMD_* 给落地加节点向导第 1 步的命令模板，
	// <子域名> 由调用方替换；面板够不着落地机，只生成命令。
	// 落地是 tiktok-node 还是 sing-box-node 按该落地的预设选（1=tiktok，2=sing-box，3=自定义默认 tiktok）。
	LANDING_ADD_NODE_CMD_TIKTOK  = "curl -fsSL https://raw.githubusercontent.com/nbbk/tiktok-node/main/install.sh | bash -s -- --add-node <子域名>"
	LANDING_ADD_NODE_CMD_SINGBOX = "curl -fsSL https://raw.githubusercontent.com/nbbk/sing-box-node/main/install.sh | bash -s -- --add-node <子域名>"
)

// landingAddNodeCmd 按落地预设生成加节点命令（preset 2 走 sing-box-node，其余走 tiktok-node）。
func landingAddNodeCmd(preset, subdomain string) string {
	tpl := LANDING_ADD_NODE_CMD_TIKTOK
	if preset == "2" {
		tpl = LANDING_ADD_NODE_CMD_SINGBOX
	}
	return strings.ReplaceAll(tpl, "<子域名>", subdomain)
}

// Env 运行环境（环境变量覆盖，供测试与运维）。
type Env struct {
	PanelHome string
	RealmDir  string
	KitDir    string
	Port      int
	SkipRoot  bool
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func detectKitDir() string {
	if v := os.Getenv("KIT_DIR"); v != "" {
		return v
	}
	for _, d := range []string{"/usr/local/share/relay-node", filepath.Join(os.Getenv("HOME"), "relay-node-kit"), "/root/relay-node-kit"} {
		if _, err := os.Stat(filepath.Join(d, "install.sh")); err == nil {
			return d
		}
	}
	// 回退：二进制旁边的仓库根（开发时 panel/ 的上一级就是 kit）
	if exe, err := os.Executable(); err == nil {
		if d := filepath.Dir(filepath.Dir(exe)); true {
			_ = d
		}
	}
	return filepath.Join(os.Getenv("HOME"), "relay-node-kit")
}

func loadEnv() Env {
	port := 11111
	if v := os.Getenv("PANEL_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &port)
	}
	return Env{
		PanelHome: envOr("PANEL_HOME", "/etc/relay-panel"),
		RealmDir:  envOr("REALM_DIR", "/etc/realm"),
		KitDir:    detectKitDir(),
		Port:      port,
		SkipRoot:  os.Getenv("PANEL_SKIP_ROOT_CHECK") == "1",
	}
}

// disableTOTPInConfigFile 关掉磁盘上 config.json 里的二次验证（SSH 恢复用）：
// totp_enabled 置 false，密钥与备用码哈希一并清除。返回 changed=false 表示
// 本来就没开；文件不存在或解析失败时返回错误（不静默跳过，免得用户以为
// 关掉了、其实还开着）。注意：只改磁盘文件，正在跑的面板进程仍用内存里
// 的旧配置，必须重启面板才生效（panel-totp-reset 会先停面板再调它）。
func disableTOTPInConfigFile(panelHome string) (bool, error) {
	p := filepath.Join(panelHome, "config.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return false, fmt.Errorf("读不到 %s：%w", p, err)
	}
	var cfg PanelConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("%s 解析失败：%w", p, err)
	}
	if !cfg.TOTPEnabled && cfg.TOTPSecret == "" {
		return false, nil
	}
	cfg.TOTPEnabled = false
	cfg.TOTPSecret = ""
	cfg.TOTPBackupCodes = nil
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(p, out, 0o600); err != nil {
		return false, fmt.Errorf("写回失败：%w", err)
	}
	return true, nil
}

// disableTOTPInConfig CLI 包装：调 disableTOTPInConfigFile，失败直接退出。
func disableTOTPInConfig(panelHome string) {
	changed, err := disableTOTPInConfigFile(panelHome)
	if err != nil {
		fmt.Fprintln(os.Stderr, "totp-disable:", err)
		os.Exit(1)
	}
	if !changed {
		fmt.Println("二次验证本来就没开，不用处理")
		return
	}
	fmt.Println("已关闭二次验证（密钥与备用码已清除），重启面板后生效")
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	// systemd 服务的环境里经常没有 HOME；子脚本（install.sh 等）在 set -u 下用 $HOME，
	// 没它会直接报错退出，先兜底（脚本侧也有同样的兜底，双保险）
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", "/root")
	}
	args := os.Args[1:]
	// CLI 子命令（安装器调用；不启动服务器）
	if len(args) > 0 {
		switch args[0] {
		case "version":
			fmt.Printf("relay-panel %s\n", Version)
			return
		case "hash-password":
			pw := ""
			if len(args) > 1 {
				pw = args[1]
			} else {
				r := bufio.NewReader(os.Stdin)
				line, _ := r.ReadString('\n')
				pw = strings.TrimRight(line, "\r\n")
			}
			if pw == "" {
				fmt.Fprintln(os.Stderr, "hash-password: 密码为空")
				os.Exit(1)
			}
			h, err := hashPassword(pw)
			if err != nil {
				fmt.Fprintln(os.Stderr, "hash-password:", err)
				os.Exit(1)
			}
			fmt.Println(h)
			return
		case "serve":
			// 继续走服务器启动
		case "totp-disable":
			// SSH 恢复：验证器丢了、备用码也没了时，关掉二次验证。
			// 只改磁盘上的 config.json；面板正在跑时会用内存里的旧配置覆盖，
			// 所以必须配合重启（panel-totp-reset 会先停面板再跑这条命令）。
			disableTOTPInConfig(envOr("PANEL_HOME", "/etc/relay-panel"))
			return
		default:
			fmt.Fprintf(os.Stderr, "用法：relay-panel [serve|version|hash-password|totp-disable]\n")
			os.Exit(1)
		}
	}
	env := loadEnv()
	if os.Geteuid() != 0 && !env.SkipRoot {
		fmt.Fprintln(os.Stderr, "relay-panel 必须以 root 运行（要改 /etc/realm、管 systemd）；测试时设 PANEL_SKIP_ROOT_CHECK=1")
		os.Exit(1)
	}
	s, err := newServer(env)
	if err != nil {
		log.Fatalf("启动失败：%v", err)
	}
	s.startBackground()
	addr := fmt.Sprintf("0.0.0.0:%d", s.cfg.Port)
	if v := os.Getenv("PANEL_PORT"); v != "" {
		fmt.Sscanf(v, "%d", &s.cfg.Port)
		addr = fmt.Sprintf("0.0.0.0:%d", s.cfg.Port)
	}
	mux := s.routes()
	var srv *http.Server
	if s.cfg.HTTPSEnabled {
		cert, key := filepath.Join(env.PanelHome, "certs", "cert.pem"), filepath.Join(env.PanelHome, "certs", "key.pem")
		if _, err := os.Stat(cert); err != nil {
			log.Printf("HTTPS 已开启但证书缺失，生成自签证书")
			if err := ensureSelfSigned(env.PanelHome, s.cfg.HTTPSDomain); err != nil {
				log.Printf("自签证书生成失败，改走 HTTP：%v", err)
			}
		}
		if _, err := os.Stat(cert); err == nil {
			srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			log.Printf("relay-panel %s 监听 https://%s", Version, addr)
			serveWithRetry(srv, cert, key)
			return
		}
	}
	srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("relay-panel %s 监听 http://%s", Version, addr)
	serveWithRetry(srv, "", "")
}

// serveWithRetry 启动 HTTP(S) 服务。重启（restartSelf）时新进程可能在旧进程
// 退出前抢绑端口——restartSelf 起新进程后只等 500ms 就退出，机器快时新进程
// 启动不到 500ms，第一次 bind 必撞“address already in use”，直接 log.Fatal
// 会把面板弄死（1.3.14 的 e2e 实测撞上：新进程 bind 失败退出、旧进程随即退出、
// 端口上什么都没了）。撞上端口占用时重试 10 次（每 0.5 秒），旧进程退出后就
// 能绑上；不是端口占用、或 10 次都撞上，才报错退出。
// http.ErrServerClosed（测试里 srv.Close()）直接返回，不报错。
func serveWithRetry(srv *http.Server, tlsCert, tlsKey string) {
	var err error
	for i := 0; i < 10; i++ {
		if tlsCert != "" {
			err = srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return
		}
		if !isAddrInUse(err) {
			log.Fatal(err)
		}
		log.Printf("端口被占用（旧进程还没退出），0.5 秒后重试（%d/10）", i+1)
		time.Sleep(500 * time.Millisecond)
	}
	log.Fatal(err)
}

// isAddrInUse 判断错误是不是端口被占（bind 冲突）。
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use")
}

// restartPanelFunc 重启面板自身的入口，包级变量只为测试能 stub 掉重启
// （真重启会 os.Exit，测试里不能真跑）。
var restartPanelFunc = restartSelf

// restartSelf 平滑重启面板自身（改端口/HTTPS 开关后用）：起新进程再退出旧进程。
func restartSelf(env Env) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "serve")
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("重启面板失败：%v", err)
		return
	}
	// 给新进程一点启动时间再退出
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}
