package config

import "sync"

// MeshAutoPeerView 自动组网对端视图的 config 层投影(自动组网批次):
// beacon 同账号候选经装配层注入的探针汇总到这里,供本包与 routing/tcp
// 等 config 消费方在「手填 peers 查无此人」时回查。字段与 MeshPeer 同构
// (LANAddrs 恒单项=beacon 来源地址;HostKey=对端自报指纹)。
type MeshAutoPeerView struct {
	DeviceID string
	LANAddrs []string
	HostKey  string
}

// 探针缝:config 层不能 import app 层(lanbeacon 在 app/mesh),按
// processor/tcp meshSelfIDSource 的 atomic.Value 注入先例,装配层在
// beacon 装配启动时 Set,进程内单点。
var (
	meshAutoPeerMu    sync.RWMutex
	meshAutoPeerProbe func(deviceID string) (MeshAutoPeerView, bool)
)

// SetMeshAutoPeerProbe 注入自动 peer 探针(装配层 boot 调用;幂等覆盖)。
// nil = 关闭(仅手填 peers 语义)。
func SetMeshAutoPeerProbe(fn func(deviceID string) (MeshAutoPeerView, bool)) {
	meshAutoPeerMu.Lock()
	defer meshAutoPeerMu.Unlock()
	meshAutoPeerProbe = fn
}

// MeshAutoPeerFor 查自动 peer 视图(拨号装配/编译闸/委托门的回查缝)。
// 探针未注入/未命中 = false。provider 是内存级读取(候选表遍历 ≤64)。
func MeshAutoPeerFor(deviceID string) (MeshAutoPeerView, bool) {
	meshAutoPeerMu.RLock()
	fn := meshAutoPeerProbe
	meshAutoPeerMu.RUnlock()
	if fn == nil {
		return MeshAutoPeerView{}, false
	}
	return fn(deviceID)
}
