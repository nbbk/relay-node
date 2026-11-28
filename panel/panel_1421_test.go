package main

// v1.4.21：轻量节点后端切片 11 标志版——面板行为无变化。
//
// 覆盖：
//   - 版本断言 1.4.21。
//   - 安装命令回归（1.4.12 落地的 wget 兜底，本次不动代码只做回归断言）：
//     面板生成的安装命令形状不变——有 curl 用 curl、没有用 wget、两个
//     都没有就明说请先装 curl 或 wget 并 exit 1。整条命令逐字比对；curl
//     分支在前、wget 分支在后；两个分支的脚本 URL 与参数（--panel／
//     --node-id／--token）原样相同；bash 只在两个分支里各跑一次；一行
//     无换行；HTTPS 面板时 scheme 为 https。

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersion1421(t *testing.T) {
	if Version != "1.4.40" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

func TestNodeInstallCommand1421(t *testing.T) {
	s := newTestServer1400(t)
	req := httptest.NewRequest("GET", "http://203.0.113.1:11111/", nil)
	const tok = "join_0123456789abcdef0123456789abcdef"
	cmd := s.nodeInstallCommand(req, 11, tok)

	// 整条命令逐字比对（1.4.12 定稿的形状，一字不动）。
	script := "https://raw.githubusercontent.com/nbbk/zhexuexi-node-backend/main/install-node-backend.sh"
	args := "--panel http://203.0.113.1:11111 --node-id 11 --token " + tok
	want := "if command -v curl >/dev/null 2>&1; then curl -fsSL " + script + " | bash -s -- " + args +
		"; elif command -v wget >/dev/null 2>&1; then wget -qO- " + script + " | bash -s -- " + args +
		"; else echo '请先安装 curl 或 wget' >&2; exit 1; fi"
	if cmd != want {
		t.Fatalf("安装命令形状变了（应与 1.4.12 定稿逐字一致）：\n实际 %q\n期望 %q", cmd, want)
	}
	// 一行可复制粘贴。
	if strings.Contains(cmd, "\n") {
		t.Fatalf("安装命令应是一行可复制粘贴的命令，实际含换行：%q", cmd)
	}
	// 有 curl 用 curl（优先）、没有用 wget 兜底。
	curlAt := strings.Index(cmd, "curl -fsSL "+script+" | bash -s -- "+args)
	wgetAt := strings.Index(cmd, "wget -qO- "+script+" | bash -s -- "+args)
	if curlAt == -1 {
		t.Fatalf("curl 分支不对：应含 %q，实际 %q", "curl -fsSL "+script+" | bash -s -- "+args, cmd)
	}
	if wgetAt == -1 {
		t.Fatalf("wget 分支不对：应含 %q，实际 %q", "wget -qO- "+script+" | bash -s -- "+args, cmd)
	}
	if curlAt > wgetAt {
		t.Fatalf("curl 优先：curl 分支应在 wget 分支之前，实际 %q", cmd)
	}
	if !strings.Contains(cmd, "command -v curl") || !strings.Contains(cmd, "command -v wget") {
		t.Fatalf("应先 command -v 查 curl、再查 wget，实际 %q", cmd)
	}
	if strings.Index(cmd, "command -v curl") > strings.Index(cmd, "command -v wget") {
		t.Fatalf("应先查 curl、再查 wget，实际 %q", cmd)
	}
	// bash 只在 curl／wget 两个分支里各跑一次（无两者时不跑 bash）。
	if n := strings.Count(cmd, "| bash -s -- "); n != 2 {
		t.Fatalf("bash 应只在 curl／wget 两个分支里各跑一次，实际 %d 次：%q", n, cmd)
	}
	// 无两者：明说请先装 curl 或 wget（stderr），exit 1（不要静默成功、
	// 不要空跑 bash）。
	if !strings.Contains(cmd, "echo '请先安装 curl 或 wget' >&2") {
		t.Fatalf("无 curl／wget 时应明说请先安装 curl 或 wget（stderr），实际 %q", cmd)
	}
	if !strings.Contains(cmd, "exit 1") {
		t.Fatalf("无 curl／wget 时应 exit 1，实际 %q", cmd)
	}
	// HTTPS 面板时 --panel 用 https scheme（与本次请求的 Host 一致）。
	s.cfg.HTTPSEnabled = true
	cmdTLS := s.nodeInstallCommand(req, 11, tok)
	if !strings.Contains(cmdTLS, "--panel https://203.0.113.1:11111 --node-id 11 --token "+tok) {
		t.Fatalf("HTTPS 面板时 --panel 应为 https 地址，实际 %q", cmdTLS)
	}
	if !strings.HasPrefix(cmdTLS, "if command -v curl") || !strings.HasSuffix(cmdTLS, "exit 1; fi") {
		t.Fatalf("HTTPS 时命令首尾形状也应不变，实际 %q", cmdTLS)
	}
}
