package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------- 1.3.44：修 /tmp 临时目录泄漏 ----------

// TestUpdateCommand1344Scope 有 systemd 且 systemd-run 可用时，更新走
// systemd-run --scope（scope 在面板服务的 cgroup 之外，restart 杀不到它），
// 超时 900 秒。
func TestUpdateCommand1344Scope(t *testing.T) {
	s := newTestServer144(t)
	old := scopeRunnerAvailable1344
	scopeRunnerAvailable1344 = func() bool { return true }
	defer func() { scopeRunnerAvailable1344 = old }()

	name, args, timeout := s.updateCommand1344()
	if name != "systemd-run" {
		t.Fatalf("有 systemd 时更新应走 systemd-run，实际 %q", name)
	}
	want := []string{"--scope", "--quiet", "bash", filepath.Join(s.env.KitDir, "panel-install.sh")}
	if len(args) != len(want) {
		t.Fatalf("systemd-run 参数应为 %v，实际 %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("systemd-run 参数应为 %v，实际 %v", want, args)
		}
	}
	if timeout != 900*time.Second {
		t.Fatalf("更新超时应为 900 秒，实际 %v", timeout)
	}
}

// TestUpdateCommand1344Fallback 没有 systemd 或 systemd-run 不可用时，
// 回退原来的直接跑法（bash panel-install.sh），超时同样 900 秒。
func TestUpdateCommand1344Fallback(t *testing.T) {
	s := newTestServer144(t)
	old := scopeRunnerAvailable1344
	scopeRunnerAvailable1344 = func() bool { return false }
	defer func() { scopeRunnerAvailable1344 = old }()

	name, args, timeout := s.updateCommand1344()
	if name != "bash" {
		t.Fatalf("无 systemd 时更新应直接跑 bash，实际 %q", name)
	}
	want := []string{filepath.Join(s.env.KitDir, "panel-install.sh")}
	if len(args) != 1 || args[0] != want[0] {
		t.Fatalf("bash 参数应为 %v，实际 %v", want, args)
	}
	if timeout != 900*time.Second {
		t.Fatalf("更新超时应为 900 秒，实际 %v", timeout)
	}
	if updateTimeout1344 != 900*time.Second {
		t.Fatalf("updateTimeout1344 应为 900 秒，实际 %v", updateTimeout1344)
	}
}

// TestScopeRunnerAvailable1344Real 真实探针：结果应与直接跑同一条探针
// 命令一致（没 systemd 或没 systemd-run 时一定是 false）。
func TestScopeRunnerAvailable1344Real(t *testing.T) {
	_, probeErr := runCmdOut(nil, 10*time.Second, "systemd-run", "--scope", "--quiet", "true")
	want := hasSystemd() && commandExists("systemd-run") && probeErr == nil
	if got := scopeRunnerAvailable1344(); got != want {
		t.Fatalf("scope 探针应为 %v，实际 %v（hasSystemd=%v，systemd-run 在 PATH=%v，探针错=%v）",
			want, got, hasSystemd(), commandExists("systemd-run"), probeErr)
	}
}

// TestRunUpdate1344Fallback 回退路径真跑一次假安装脚本：命令能跑通、
// 超时 900 秒、脚本确实被执行。
func TestRunUpdate1344Fallback(t *testing.T) {
	s := newTestServer144(t)
	old := scopeRunnerAvailable1344
	scopeRunnerAvailable1344 = func() bool { return false }
	defer func() { scopeRunnerAvailable1344 = old }()

	marker := filepath.Join(t.TempDir(), "ran")
	script := "echo ran > '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(s.env.KitDir, "panel-install.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := s.runUpdate1344()
	if err != nil {
		t.Fatalf("回退路径跑安装脚本应成功：%v（stdout=%q stderr=%q）", err, res.Stdout, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("回退路径退出码应为 0，实际 %d", res.ExitCode)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("回退路径没跑到安装脚本（%s 不存在）：%v", marker, err)
	}
}

// kitRoot1344 测试文件所在仓的根目录（panel/ 的上一级）。
func kitRoot1344(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 拿不到测试文件路径")
	}
	return filepath.Dir(filepath.Dir(file))
}

// TestPanelInstallDryRun1344 真实 panel-install.sh，DRY_RUN=1 沙箱实测：
// ① PATH 里没有 go、但 $GO_INSTALL_DIR/bin/go 存在时，go_ok 认得到已装的
// Go，不再下 Go 包；② 磁盘预检后清旧临时目录：超过 1 天的 tmp.* 被删，
// 新鲜的与不叫 tmp.* 的不动；③ 正常跑完 trap 会清掉当次新建的临时目录。
func TestPanelInstallDryRun1344(t *testing.T) {
	script := filepath.Join(kitRoot1344(t), "panel-install.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("panel-install.sh 不存在：%v", err)
	}

	// 假 Go 1.23.0：只会打印版本号，不真编译。
	fakeGo := t.TempDir()
	fakeBin := filepath.Join(fakeGo, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte("#!/bin/sh\necho \"go version go1.23.0 linux/amd64\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 沙箱 /tmp：必须在有 500MB 以上空闲的盘上（脚本磁盘预检的要求），
	// 不能用 t.TempDir()——本机 /tmp 是小 tmpfs。放在 kit 外一层，跑完删掉。
	sandboxTmp, err := os.MkdirTemp(filepath.Dir(kitRoot1344(t)), "test-tmp-1344-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sandboxTmp) })
	oldDir := filepath.Join(sandboxTmp, "tmp.old111111")
	freshDir := filepath.Join(sandboxTmp, "tmp.new222222")
	otherDir := filepath.Join(sandboxTmp, "otherdir")
	oldFile := filepath.Join(sandboxTmp, "tmpfile.txt")
	for _, d := range []string{oldDir, freshDir, otherDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(oldFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().AddDate(0, 0, -2)
	for _, p := range []string{oldDir, otherDir, oldFile} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{
		"PATH=/usr/bin:/bin", // 里面没有 go：认不认全靠 (a) 的 PATH 补丁
		"TMPDIR=" + sandboxTmp,
		"DRY_RUN=1",
		"GO_INSTALL_DIR=" + fakeGo,
		"PANEL_HOME=" + t.TempDir(),
		"KIT_DIR=" + t.TempDir(),
		"PANEL_USER=admin",
		"PANEL_PASS=test-password-123",
		"PANEL_MODE=1",
		"HOME=" + t.TempDir(),
	}
	cmd := exec.Command("bash", script)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(nil)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("panel-install.sh DRY_RUN 应跑通：%v\n输出：%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Go 已有") {
		t.Fatalf("PATH 没有 go、$GO_INSTALL_DIR/bin/go 存在时应认到已装的 Go（输出含“Go 已有”），实际输出：%s", out.String())
	}
	if strings.Contains(out.String(), "跳过装 Go") || strings.Contains(out.String(), "正在下官方包") {
		t.Fatalf("认到已装的 Go 后不应再下 Go 包，实际输出：%s", out.String())
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("超过 1 天的旧 tmp.* 目录应被清掉：%s", oldDir)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("新鲜的 tmp.* 目录不应被删：%v", err)
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Fatalf("不叫 tmp.* 的旧目录不应被删：%v", err)
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatalf("不是目录的旧 tmp 开头文件不应被删：%v", err)
	}
	// 当次新建的临时目录也应被 trap 清掉：沙箱里只剩预先放好的三项。
	entries, err := os.ReadDir(sandboxTmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("跑完沙箱 /tmp 应只剩 3 项（新鲜 tmp.*、otherdir、tmpfile.txt），实际 %v", names)
	}
}

// TestTrapTerm1344 同款 trap 发 TERM 后目录被删：从真实 panel-install.sh
// 里取出 trap 那行原样 eval，起一个带它的 bash，发 TERM，验临时目录被清掉。
func TestTrapTerm1344(t *testing.T) {
	script := filepath.Join(kitRoot1344(t), "panel-install.sh")
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	var trapLine string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "trap 'rm -rf \"$SRC_DIR\"'") {
			trapLine = line
			break
		}
	}
	if trapLine == "" {
		t.Fatal("panel-install.sh 里找不到 trap 'rm -rf \"$SRC_DIR\"' 那行")
	}
	if !strings.Contains(trapLine, "EXIT TERM INT HUP") {
		t.Fatalf("trap 应同时覆盖 EXIT TERM INT HUP，实际是：%s", trapLine)
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "dir.txt")
	fixture := filepath.Join(dir, "trap-fixture.sh")
	fixtureContent := "#!/bin/bash\n" +
		"SRC_DIR=\"$(mktemp -d)\"\n" +
		"echo -n \"$SRC_DIR\" > \"$1\"\n" +
		trapLine + "\n" +
		"sleep 10 &\n" +
		"wait $!\n"
	if err := os.WriteFile(fixture, []byte(fixtureContent), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", fixture, outFile)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TMPDIR=" + dir}
	cmd.Stdin = bytes.NewReader(nil)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 trap fixture 失败：%v", err)
	}
	// 等 fixture 把建好的临时目录路径写出来。
	var tmpDir string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(outFile); err == nil && len(b) > 0 {
			tmpDir = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if tmpDir == "" {
		_ = cmd.Process.Kill()
		t.Fatal("fixture 没写出临时目录路径")
	}
	if _, err := os.Stat(tmpDir); err != nil {
		t.Fatalf("发 TERM 前临时目录应存在：%v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("发 SIGTERM 失败：%v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("发 TERM 后 fixture 10 秒还没退出")
	}
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Fatalf("发 TERM 后临时目录应被 trap 清掉：%s", tmpDir)
	}
}
