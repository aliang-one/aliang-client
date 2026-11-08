// mesh_offline AI 流量离线改道的离线信号注入缝(计划 Task 12,design
// §2 行11/§9.4):平台隧道不可达(离线)且 AI 类离线兜底勾选时,
// shouldForceAliangRoute 的命中从 RouteToALiang 改判 mesh 兜底节点
// (RouteToNode 家族,B 出其隧道)——判定本体见 route_override.go
// meshOfflineRedirectForAI,执行见 handler.go handleNonTLS 强制分支。
package tcp

import "sync/atomic"

// meshOfflineSignal 离线信号注入缝(egress offlineSignal 同款模式,同
// meshSelfIDSource/meshPikoDialSource 先例):atomic.Value 保护。**共用契约
// (与 processor/egress SetOfflineSignal 注释互指):两缝必须同源同真假
// ——Task 14 组装层用同一 manager 状态 getter 同时接线两缝,否则会出现
// 「自身流量经 B 兜底而 AI 流量仍撞死隧道」的分裂形态。**未接线(nil)
// 视为在线(保守:不激活改道,保持 RouteToALiang 现状)。
var meshOfflineSignal atomic.Value // func() bool

// SetMeshOfflineSignal 注入离线信号提供器(true=平台隧道不可达,激活 AI
// 离线改道);nil = 清除(未接线 = 视为在线)。并发安全(生产写方为
// Task 14 组装层接线路径,读方为每次 AI 连接的强制路由分支)。
func SetMeshOfflineSignal(fn func() bool) {
	if fn == nil {
		fn = func() bool { return false }
	}
	meshOfflineSignal.Store(fn)
}

// meshOfflineActive 读离线信号;未接线(false 保守,在线语义)。
func meshOfflineActive() bool {
	fn, _ := meshOfflineSignal.Load().(func() bool)
	if fn == nil {
		return false
	}
	return fn()
}

// MeshOfflineSignalForTest 读回离线信号缝的当前值(生产读方走
// meshOfflineActive):导出仅供 app/tunnel 装配断言测试钉「egress/tcp 两缝
// 同源同真假」契约(Task 14)。
func MeshOfflineSignalForTest() bool { return meshOfflineActive() }

// MeshOfflineAIRedirectActive AI 离线改道是否激活的跨包查询(探针降级等
// 消费方入口):与数据面改道判定同源同真假——本函数就是
// meshOfflineRedirectForAI 的布尔投影视图,不存在第二份判定逻辑。
// app/http/services agentCoreEnsureUpstream 用它在离线激活时跳过上游探针。
func MeshOfflineAIRedirectActive() bool {
	_, ok := meshOfflineRedirectForAI()
	return ok
}
