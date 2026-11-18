// relay-panel: relay-node 可视化面板后端（Go 单二进制，只用标准库）。
// 契约见 ~/workspace/relay-panel-build/api-contract.md。
// CLI: relay-panel [serve] | relay-panel version | relay-panel hash-password [密码]
package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		default:
			fmt.Fprintf(os.Stderr, "用法：relay-panel [serve|version|hash-password]\n")
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
			log.Fatal(srv.ListenAndServeTLS(cert, key))
			return
		}
	}
	srv = &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("relay-panel %s 监听 http://%s", Version, addr)
	log.Fatal(srv.ListenAndServe())
}

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
