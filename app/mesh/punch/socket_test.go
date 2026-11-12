package punch

import (
	"errors"
	"net"
	"runtime"
	"strings"
	"testing"
)

// TestNewBoundUDPConnResolverError resolver 解析失败必须原样上抛错误,
// 绝不静默回落到未绑定 socket(TUN-on 下裸 socket 打洞会打错 5-tuple,评审定谳致命)。
func TestNewBoundUDPConnResolverError(t *testing.T) {
	conn, iface, err := NewBoundUDPConn(func() (string, error) {
		return "", errors.New("no default route")
	})
	if err == nil {
		t.Fatal("期望 resolver 失败时返回错误,得到 nil")
	}
	if !strings.Contains(err.Error(), "resolve interface") {
		t.Fatalf("错误信息应含 \"resolve interface\",得到: %v", err)
	}
	if conn != nil {
		t.Fatalf("失败路径不应返回 conn,得到 %v", conn)
	}
	if iface != "" {
		t.Fatalf("失败路径不应返回接口名,得到 %q", iface)
	}
}

// TestNewBoundUDPConnDefaultResolver 默认解析器真实链路:物理口解析 +
// 平台绑定 + OS 分配端口;linux 非 root 缺 CAP_NET_RAW 属环境限制,
// 显式 skip 而非失败(生产语义仍是报错自禁,见 socket_linux.go 注释)。
func TestNewBoundUDPConnDefaultResolver(t *testing.T) {
	conn, iface, err := NewBoundUDPConn(DefaultResolver)
	if err != nil {
		if runtime.GOOS == "linux" && strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("linux SO_BINDTODEVICE 需 CAP_NET_RAW,当前环境无权限: %v", err)
		}
		t.Fatalf("默认 resolver 链路应成功: %v", err)
	}
	defer conn.Close()

	if iface == "" {
		t.Fatal("成功路径应返回非空接口名")
	}
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("LocalAddr 应为 *net.UDPAddr,得到 %T", conn.LocalAddr())
	}
	if addr.Port == 0 {
		t.Fatalf("OS 应分配非零端口,得到 %v", addr)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close 应干净返回,得到 %v", err)
	}
}

// TestNewBoundUDPConnNoSuchInterface 不存在的接口名必须走 bind 报错路径,
// 不得返回未绑定 conn。
func TestNewBoundUDPConnNoSuchInterface(t *testing.T) {
	conn, iface, err := NewBoundUDPConn(func() (string, error) {
		return "punch0-no-such-iface", nil
	})
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("不存在的接口名应报错")
	}
	if !strings.Contains(err.Error(), "bind") {
		t.Fatalf("错误信息应含 \"bind\",得到: %v", err)
	}
	if conn != nil || iface != "" {
		t.Fatalf("失败路径不应返回 conn/接口名,得到 conn=%v iface=%q", conn, iface)
	}
}
