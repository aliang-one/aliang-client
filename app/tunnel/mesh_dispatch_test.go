package tunnel

// WireUnifiedIngressMeshDispatch 接线钉子(spec §6.5):接线后统一口分派的
// mesh 钩子非 nil,且喂入的 JSON 帧头确实到达 HandleMeshConn(以拒绝行回
// 源为证——默认配置转发关,LAN 载体帧必被门禁拒收并回写一行文本)。门禁
// 内部语义归 processor/tcp 的 mesh_inbound 测试,此处只钉「接对了人+帧到了」。

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	httpIngress "aliang.one/nursorgate/inbound/http"
)

// TestWireUnifiedIngressMeshDispatchReachesHandleMeshConn 接线后喂 JSON 帧:
// HandleMeshConn 的默认拒收路径(ForwardEnabled=false)回写拒绝行并断连。
func TestWireUnifiedIngressMeshDispatchReachesHandleMeshConn(t *testing.T) {
	restoreMeshDispatchHook(t)
	WireUnifiedIngressMeshDispatch(func() string { return "dev-dispatch-a" })

	if httpIngress.MeshConnHandler == nil {
		t.Fatal("wiring must install MeshConnHandler")
	}

	client, server := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		httpIngress.MeshConnHandler(server)
	}()

	frame := `{"from":"peer-x","class":"ai","target":"example.com:443"}` + "\n"
	if _, err := client.Write([]byte(frame)); err != nil {
		t.Fatalf("write frame: %v", err)
	}

	// HandleMeshConn 走到门禁即回写单行拒绝(默认转发关);读到非空行 =
	// 帧已到达分派处理器。随后服务端关连接,读以错误收尾。
	line, err := readRejectLine(t, client)
	if err != nil {
		t.Fatalf("expected reject line from HandleMeshConn gate: %v", err)
	}
	if strings.Contains(line, "\n") || line == "" {
		t.Fatalf("reject must be a single non-empty line, got %q", line)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return")
	}
}

// readRejectLine 读一行拒绝文本(带 deadline 防挂)。
func readRejectLine(t *testing.T, conn net.Conn) (string, error) {
	t.Helper()
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		if _, err := conn.Read(one); err != nil {
			return string(buf), err
		}
		buf = append(buf, one[0])
		if one[0] == '\n' {
			return strings.TrimRight(string(buf), "\n"), nil
		}
		if len(buf) > 1024 {
			return string(buf), io.ErrShortBuffer
		}
	}
}

// restoreMeshDispatchHook 保存/还原统一口 mesh 钩子(进程级包变量,测试
// 结束归位 nil 之外的原值,防泄漏到包内其他用例)。
func restoreMeshDispatchHook(t *testing.T) {
	t.Helper()
	prev := httpIngress.MeshConnHandler
	t.Cleanup(func() { httpIngress.MeshConnHandler = prev })
}
