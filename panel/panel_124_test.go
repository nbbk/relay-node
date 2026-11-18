package main

import (
	"errors"
	"strings"
	"testing"
)

// ---------- 1.2.4：实时日志看不到 ----------
// 根因：realm 日志级别 warn，正常运行时不产生日志，/var/log/realm.log 是空文件；
// 旧后端把空文件切成 [""] 返回，前端用 lines.length=1 判为有内容，渲染成空。

func TestFilterLogLines(t *testing.T) {
	in := []string{"", "2026-11-19T00:00:00Z [WARN] accept error", " ", "\t", "-- No entries --", "Nov 19 00:00:01 host systemd[1]: Started realm."}
	got := filterLogLines(in)
	if len(got) != 2 || got[0] != in[1] || got[1] != in[5] {
		t.Fatalf("过滤结果不对：%q", got)
	}
	if got := filterLogLines(nil); len(got) != 0 {
		t.Fatalf("空输入应返回空：%q", got)
	}
}

func TestCollectLogLines(t *testing.T) {
	errJournal := func(n int) (string, error) { return "", errors.New("no journalctl") }
	journalOK := func(out string) func(int) (string, error) {
		return func(n int) (string, error) { return out, nil }
	}
	// 1. 空文件 + journalctl 失败 → lines 为空 + warn 说明
	lines, note := collectLogLines([]byte(""), nil, 200, errJournal)
	if len(lines) != 0 || !strings.Contains(note, "warn") {
		t.Fatalf("空文件应返回空行+warn 说明，实际 lines=%q note=%q", lines, note)
	}
	// 2. 只有空行（换行符/空白行）+ journalctl 失败 → 同上
	lines, note = collectLogLines([]byte("\n\n   \n\t\n"), nil, 200, errJournal)
	if len(lines) != 0 || !strings.Contains(note, "warn") {
		t.Fatalf("只有空行应返回空行+warn 说明，实际 lines=%q note=%q", lines, note)
	}
	// 3. 有警告行 → 正常显示，不走 journalctl
	called := false
	journalSpy := func(n int) (string, error) { called = true; return "should not be used", nil }
	lines, note = collectLogLines([]byte("2026-11-19T00:00:00Z [WARN] accept error: too many open files\n"), nil, 200, journalSpy)
	if called {
		t.Fatalf("文件有内容时不应调 journalctl")
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "accept error") || note != "" {
		t.Fatalf("警告行应正常显示，实际 lines=%q note=%q", lines, note)
	}
	// 4. 空文件 + journalctl 有启动记录 → 显示 journalctl 内容
	lines, note = collectLogLines([]byte(""), nil, 200, journalOK("Nov 19 00:00:01 host systemd[1]: Started realm.\nNov 19 00:00:02 host systemd[1]: Stopped realm.\n"))
	if len(lines) != 2 || note != "" {
		t.Fatalf("空文件应回退显示 journalctl，实际 lines=%q note=%q", lines, note)
	}
	// 5. 空文件 + journalctl 只有 "-- No entries --" → 当作无记录，返回 warn 说明
	lines, note = collectLogLines([]byte(""), nil, 200, journalOK("-- No entries --\n"))
	if len(lines) != 0 || !strings.Contains(note, "warn") {
		t.Fatalf("journalctl 无记录应返回空行+warn 说明，实际 lines=%q note=%q", lines, note)
	}
	// 6. 文件缺失 + journalctl 有内容 → 显示 journalctl 内容
	lines, note = collectLogLines(nil, errors.New("no such file"), 200, journalOK("Nov 19 00:00:01 host systemd[1]: Started realm.\n"))
	if len(lines) != 1 || note != "" {
		t.Fatalf("文件缺失应回退显示 journalctl，实际 lines=%q note=%q", lines, note)
	}
	// 7. 文件缺失 + journalctl 失败 → 空行 + 文件不存在说明
	lines, note = collectLogLines(nil, errors.New("no such file"), 200, errJournal)
	if len(lines) != 0 || !strings.Contains(note, "日志文件不存在") {
		t.Fatalf("文件缺失+journalctl 失败应返回文件不存在说明，实际 lines=%q note=%q", lines, note)
	}
	// 8. 取尾：先过滤空行再取最后 n 行
	lines, note = collectLogLines([]byte("a\n\nb\nc\n\nd\n"), nil, 2, errJournal)
	if len(lines) != 2 || lines[0] != "c" || lines[1] != "d" {
		t.Fatalf("取尾应先过滤再取最后 2 行，实际 lines=%q note=%q", lines, note)
	}
	// 9. journalctl 内容超长也取尾
	lines, note = collectLogLines([]byte(""), nil, 2, journalOK("j1\nj2\nj3\n"))
	if len(lines) != 2 || lines[0] != "j2" || lines[1] != "j3" {
		t.Fatalf("journalctl 取尾不对，实际 lines=%q note=%q", lines, note)
	}
}
