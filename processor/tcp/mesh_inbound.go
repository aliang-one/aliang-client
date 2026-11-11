// mesh_inbound.go 只承载 RouteMesh 常量:mesh 注入连接的 Metadata.Route
// 溯源标记。历史帧协议入站路径(HandleMeshConn/MeshInboundDeps/DNS 代转发
// 等)已退役(SPEC §13.1)——mesh 流量现在经 SSH 入口
// (inbound/ssh HandleIngressConn → form 矩阵)注入,注入侧仍打此标记。
package tcp

// RouteMesh 是 mesh 注入连接的 Metadata.Route 初始值(design §3.3 溯源标记)。
// B 侧自己的规则引擎照常决定真实路由(处理中会覆写为 RouteToALiang/
// RouteToSocks/RouteDirect 等),此标记仅让注入流量可识别、可审计。
const RouteMesh = "RouteMesh"
