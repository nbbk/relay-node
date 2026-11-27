package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- 1.4.30：顶栏本机 IP——本地枚举为空时问公网 IP 回显兜底 ----------
//
// 背景：管理面板那台机上 machineIP() 一个 IPv4 都读不到（公网 IP 是
// NAT 映射、本来就不在网卡上，连内网 IP 也没读到），status 回的
// machine_ip 是 ""，顶栏只剩 "—"。修法：本地枚举为空时问一次公网
// IP 回显（api.ipify.org，3 秒超时），成功后进程内缓存；回显失败
// 只记 5 分钟，5 分钟后下一次 status 调用重新问；回显失败仍回 ""，
// 不阻塞、不报错；本地能拿到 IP 的机器行为不变。

func TestVersion1430(t *testing.T) {
	if Version != "1.4.33" {
		t.Fatalf("Version 应为 1.4.33，实际 %q", Version)
	}
}

// echoServer1430 起一个计数回显假服务端，返回请求次数与 URL。
func echoServer1430(t *testing.T, handler http.HandlerFunc) (*atomic.Int32, string) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &calls, srv.URL
}

// setMachineIPStubs1430 替换本地枚举与回显地址（并先清回显缓存与
// 失败时间戳），测试结束恢复原值并清缓存，不影响同进程里的其他测试。
func setMachineIPStubs1430(t *testing.T, ips []net.IP, err error, echoURL string) {
	t.Helper()
	oldLocal, oldURL := localIPsFunc, publicIPEchoURL
	machineIPEchoMu.Lock()
	machineIPEchoDone, machineIPEchoIP, machineIPEchoFailAt = false, "", time.Time{}
	machineIPEchoMu.Unlock()
	localIPsFunc = func() ([]net.IP, error) { return ips, err }
	publicIPEchoURL = echoURL
	t.Cleanup(func() {
		localIPsFunc, publicIPEchoURL = oldLocal, oldURL
		machineIPEchoMu.Lock()
		machineIPEchoDone, machineIPEchoIP, machineIPEchoFailAt = false, "", time.Time{}
		machineIPEchoMu.Unlock()
	})
}

// TestMachineIPLocalHasIPNoEcho1430 本地有 IP 时不问外网（且优先公网
// IP，与改前行为一致）。
func TestMachineIPLocalHasIPNoEcho1430(t *testing.T) {
	calls, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("203.0.113.5")}, nil, url)
	if got := machineIP(); got != "203.0.113.5" {
		t.Fatalf("本地有公网 IP 时应优先回公网 IP，实际 %q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("本地有 IP 时不应问回显，实际问了 %d 次", calls.Load())
	}
}

// TestMachineIPEchoFallback1430 本地为空＋回显成功，返回回显 IP
// （响应带换行与空白也认）。
func TestMachineIPEchoFallback1430(t *testing.T) {
	_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "  203.0.113.9\n")
	})
	setMachineIPStubs1430(t, nil, nil, url)
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("本地为空＋回显成功时应回回显 IP，实际 %q", got)
	}
}

// TestMachineIPEchoOnLocalError1430 本地枚举报错也走回显（与空列表同路）。
func TestMachineIPEchoOnLocalError1430(t *testing.T) {
	_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, nil, fmt.Errorf("mock 网卡读不到"), url)
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("本地枚举报错时应走回显，实际 %q", got)
	}
}

// TestMachineIPEchoFailure1430 回显失败回空：非 200、脏数据、超时。
func TestMachineIPEchoFailure1430(t *testing.T) {
	t.Run("非200", func(t *testing.T) {
		_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			fmt.Fprint(w, "203.0.113.9")
		})
		setMachineIPStubs1430(t, nil, nil, url)
		if got := machineIP(); got != "" {
			t.Fatalf("回显 500 时应回空，实际 %q", got)
		}
	})
	t.Run("脏数据", func(t *testing.T) {
		_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "not-an-ip\n")
		})
		setMachineIPStubs1430(t, nil, nil, url)
		if got := machineIP(); got != "" {
			t.Fatalf("回显脏数据时应回空，实际 %q", got)
		}
	})
	t.Run("超时", func(t *testing.T) {
		_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(5 * time.Second)
			fmt.Fprint(w, "203.0.113.9")
		})
		setMachineIPStubs1430(t, nil, nil, url)
		start := time.Now()
		if got := machineIP(); got != "" {
			t.Fatalf("回显超时时应回空，实际 %q", got)
		}
		if time.Since(start) > 4*time.Second {
			t.Fatalf("回显超时应在 3 秒左右放手，实际用了 %v", time.Since(start))
		}
	})
}

// TestMachineIPEchoFailureRetryAfter5Min1430 回显失败只记 5 分钟：
// 5 分钟内不再问，5 分钟后下一次 status 调用重新问，问通了就恢复
// 并按成功缓存（再问一次也不重问）。时钟用包级 machineIPNow 注入，
// 不真等 5 分钟。
func TestMachineIPEchoFailureRetryAfter5Min1430(t *testing.T) {
	var n atomic.Int32
	_, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(500)
			fmt.Fprint(w, "boom")
			return
		}
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, nil, nil, url)
	now := time.Now()
	oldNow := machineIPNow
	machineIPNow = func() time.Time { return now }
	t.Cleanup(func() { machineIPNow = oldNow })
	if got := machineIP(); got != "" {
		t.Fatalf("第一次回显失败时应回空，实际 %q", got)
	}
	if n.Load() != 1 {
		t.Fatalf("第一次应问回显 1 次，实际 %d 次", n.Load())
	}
	if got := machineIP(); got != "" {
		t.Fatalf("5 分钟内重调仍应回空，实际 %q", got)
	}
	if n.Load() != 1 {
		t.Fatalf("5 分钟内不应重问回显，实际问了 %d 次", n.Load())
	}
	now = now.Add(6 * time.Minute)
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("5 分钟后重问应回恢复后的回显 IP，实际 %q", got)
	}
	if n.Load() != 2 {
		t.Fatalf("5 分钟后应重问回显 1 次，实际问了 %d 次", n.Load())
	}
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("恢复成功后应按成功缓存，实际 %q", got)
	}
	if n.Load() != 2 {
		t.Fatalf("恢复成功后不应再问回显，实际问了 %d 次", n.Load())
	}
}
func TestMachineIPEchoCachedOnce1430(t *testing.T) {
	calls, url := echoServer1430(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "203.0.113.9")
	})
	setMachineIPStubs1430(t, nil, nil, url)
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("第一次应回回显 IP，实际 %q", got)
	}
	if got := machineIP(); got != "203.0.113.9" {
		t.Fatalf("第二次应回缓存的回显 IP，实际 %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("回显只应问一次，实际问了 %d 次", calls.Load())
	}
}
