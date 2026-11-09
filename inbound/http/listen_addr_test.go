package http

// CurrentListenAddr 是绑定 reconcile 的运行时观测量:配置层给出「期望地址」,
// 这里给出「实际地址」,两者不等才重绑。判据必须包含运行态——StopHttpProxy
// 关闭 listener 后若仍从指针读,会拿到已关闭 listener 的旧地址,把「已停」
// 误报成「还在旧地址上跑」,reconcile 便永远判等而漏掉该起/该重绑的情形。

import (
	"net"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

// netListenForTest 薄包装 net.Listen(测试里连写两次,省一层判错样板)。
func netListenForTest(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func resetHTTPListenerState(t *testing.T) {
	t.Helper()
	httpMutex.Lock()
	httpListener = nil
	isHttpRunning = false
	httpMutex.Unlock()
	t.Cleanup(func() {
		httpMutex.Lock()
		httpListener = nil
		isHttpRunning = false
		httpMutex.Unlock()
	})
}

// TestCurrentListenAddrEmptyWhenNotRunning 未运行 → 空串(未绑定)。
func TestCurrentListenAddrEmptyWhenNotRunning(t *testing.T) {
	resetHTTPListenerState(t)
	if got := CurrentListenAddr(); got != "" {
		t.Fatalf("未运行时 CurrentListenAddr() = %q, want 空串", got)
	}
}

// TestCurrentListenAddrReportsBoundAddr 运行中 → 返回真实绑定地址。
func TestCurrentListenAddrReportsBoundAddr(t *testing.T) {
	resetHTTPListenerState(t)
	addr := config.UnifiedIngressListenAddr()
	listener, err := netListenForTest(addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	httpMutex.Lock()
	httpListener = listener
	isHttpRunning = true
	httpMutex.Unlock()

	if got := CurrentListenAddr(); got != listener.Addr().String() {
		t.Fatalf("CurrentListenAddr() = %q, want %q", got, listener.Addr().String())
	}
}

// TestCurrentListenAddrEmptyAfterStop 停之后(指针残留)→ 仍报空串,这是
// 「以运行态为判据」的钉子:仅看指针会把已关闭 listener 的旧地址当成在跑。
func TestCurrentListenAddrEmptyAfterStop(t *testing.T) {
	resetHTTPListenerState(t)
	listener, err := netListenForTest("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = listener.Close() // 模拟 StopHttpProxy 关掉后的残留指针

	httpMutex.Lock()
	httpListener = listener // 指针残留
	isHttpRunning = false   // 但运行态已 false
	httpMutex.Unlock()

	if got := CurrentListenAddr(); got != "" {
		t.Fatalf("停止后 CurrentListenAddr() = %q, want 空串", got)
	}
}
