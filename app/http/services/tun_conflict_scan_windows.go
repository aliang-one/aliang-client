//go:build windows

package services

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adaptersAddressesBufFloor 是 GetAdaptersAddresses 缓冲区的下限。
var adaptersAddressesBufFloor = uint32(15 * 1024)

func loadWindowsTunInterfaceSnapshotsNative() ([]tunInterfaceSnapshot, error) {
	// 2026-11-25 4070/0196 core 崩溃取证定案:本调用链旧形态(固定 15KB +
	// OVERFLOW continue 重试)与「指针高 32 位被小整数覆写」的堆写坏同源——
	// IpAdapterAddresses 的 IfIndex/ZoneIndices 等小整数字段落在结构
	// offset 4+,与覆写几何(byte4-7)完全吻合,值域=网卡索引{1,2,6}。写坏
	// 窗口在「buffer 已不被 liveness 追踪、内核仍在/仍在被读」的复用边界上。
	// 修法三件套(行为不变):
	//  1. 首查传 nil 拿真实所需 size,一次性分配到位+余量,消灭 OVERFLOW
	//     重试路径(该路径 buffer 生命周期最短、复用竞态窗口最大);
	//  2. 成功后立即把链表内容深拷贝成纯 Go 值,不再触碰内核写过的内存;
	//  3. runtime.KeepAlive(buf) 显式锚定:遍历与拷贝期间 buffer 必须存活
	//     (UTF16PtrToString 的内部分配可能触发 GC,保守扫描之外再上一道锁)。
	size := uint32(0)
	if err := windows.GetAdaptersAddresses(
		windows.AF_UNSPEC,
		windows.GAA_FLAG_INCLUDE_ALL_INTERFACES,
		0,
		nil,
		&size,
	); err != nil && !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
		// 非溢出错误:查询 size 失败,退回固定下限照常尝试。
		size = adaptersAddressesBufFloor
	}
	if size < adaptersAddressesBufFloor {
		size = adaptersAddressesBufFloor
	}
	// 余量:两次查询之间适配器集可能增长,避免落在刚好不够的边界上。
	buf := make([]byte, size+4096)
	first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))

	var callSize uint32 = uint32(len(buf))
	err := windows.GetAdaptersAddresses(
		windows.AF_UNSPEC,
		windows.GAA_FLAG_INCLUDE_ALL_INTERFACES,
		0,
		first,
		&callSize,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			// 两次查询之间适配器暴增(极端情形):按内核给的真实值一次性重试。
			buf = make([]byte, callSize+4096)
			first = (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
			callSize = uint32(len(buf))
			err = windows.GetAdaptersAddresses(
				windows.AF_UNSPEC,
				windows.GAA_FLAG_INCLUDE_ALL_INTERFACES,
				0,
				first,
				&callSize,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("GetAdaptersAddresses failed: %w", err)
		}
	}

	snapshots := make([]tunInterfaceSnapshot, 0, 8)
	for adapter := first; adapter != nil; adapter = adapter.Next {
		name := strings.TrimSpace(windows.UTF16PtrToString(adapter.FriendlyName))
		description := strings.TrimSpace(windows.UTF16PtrToString(adapter.Description))
		if name == "" && description == "" {
			continue
		}

		snapshots = append(snapshots, tunInterfaceSnapshot{
			Name:        name,
			Description: description,
			Status:      windowsAdapterStatusText(adapter.OperStatus),
		})
	}
	// 显式保活:上面遍历里的字符串分配可能触发 GC,必须保证内核写入的
	// buf 在全部读取完成前不被回收复用。
	runtime.KeepAlive(buf)
	return snapshots, nil
}

func windowsAdapterStatusText(status uint32) string {
	switch status {
	case windows.IfOperStatusUp:
		return "up"
	case windows.IfOperStatusDown:
		return "down"
	case windows.IfOperStatusTesting:
		return "testing"
	case windows.IfOperStatusUnknown:
		return "unknown"
	case windows.IfOperStatusDormant:
		return "dormant"
	case windows.IfOperStatusNotPresent:
		return "not_present"
	case windows.IfOperStatusLowerLayerDown:
		return "lower_layer_down"
	default:
		return fmt.Sprintf("unknown(%d)", status)
	}
}
