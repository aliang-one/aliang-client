//go:build !darwin && !linux && !windows

package punch

import (
	"fmt"
	"net"
	"runtime"
)

// bindUDP4 其余 GOOS(freebsd/openbsd/…)的桩:没有物理接口绑定实现,
// 返回明确错误。目的在防未来加平台时编译不断、运行时也不静默裸奔
// (NewBoundUDPConn 的语义铁律:绑定失败即上层载体自禁落 piko)。
func bindUDP4(iface string) (*net.UDPConn, error) {
	return nil, fmt.Errorf("physical-interface bind unsupported on %s (iface %q)", runtime.GOOS, iface)
}
