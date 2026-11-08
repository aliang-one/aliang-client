// Package relay 实现 B 侧常驻 mesh 接收(design §3.1/§3.3):LAN 监听口与
// piko "<deviceID>.mesh" 中继端点的连接统一交给 processor/tcp 的
// HandleMeshConn——去帧头后注入 B 的入站管线,继承 B 全部规则(多跳由此
// 免费获得)。拒绝路径的错误行回写与连接关闭都在 mesh_inbound 内统一处理
// (错误回源,design §3.3)。
//
// LAN 鉴权分期(design §3.1 实施期修正):本阶段信任边界=帧头门禁——
// EffectiveForwardEnabled(默认关,让渡型能力须显式开启)+ from ∈
// customer.mesh.peers(对称组网 ACL),连接首行即帧头、无额外握手。
// peer_lan 式自签证书+指纹钉扎(agent_peer_lan.go:130-155/349-361)随
// LAN beacon 在 Task 9/10 接入;共享 token 预行方案未采纳——私有的预鉴权
// 行会造出 mTLS 落地时要拆的协议变体,门禁(默认关+对称 peers)已构成当前
// 阶段的最小信任边界。
package relay

import (
	"log"
	"net"
	"strings"

	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/tcp"
)

// DepsFunc 逐连接取 MeshInboundDeps。customer.mesh 可热更(保存即生效),
// 门禁/ACL/跳数必须读连接时刻的配置——deps 不缓存在 Relay 内。
type DepsFunc func() tcp.MeshInboundDeps

// Relay 常驻 mesh 接收器。除注入的 deps 提供器外零内部状态,可同时 Serve
// 多个 listener(LAN 监听口与中继端点各一)。
type Relay struct {
	deps DepsFunc
}

// New 构造接收器。deps 为 nil 时所有连接立即关闭(mesh 未组装,不该发生)。
func New(deps DepsFunc) *Relay {
	return &Relay{deps: deps}
}

// Serve 常驻 accept 循环:不设 accept deadline、受理连接后不关摊——刻意区别
// 于 agent_peer_lan.go:311-320 的一次性口(首连/TTL 到点即收摊),mesh 是
// 长期能力。listener 关闭时 Accept 报错返回即退出;已受理连接的 goroutine
// 由各自生命周期收敛(同 app/tunnel tcpBridge.Serve 先例)。
func (r *Relay) Serve(listener net.Listener, carrier string) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go r.handleConn(conn, carrier)
	}
}

func (r *Relay) handleConn(conn net.Conn, carrier string) {
	if r.deps == nil {
		_ = conn.Close()
		return
	}
	if err := tcp.HandleMeshConn(conn, carrier, r.deps()); err != nil {
		log.Printf("[MESH-RELAY] carrier=%s conn finished with error: %v", carrier, err)
	}
}

// ProductionDeps 组装生产 deps 提供器(组装层注入 selfID——relay 不自摸
// device_id 全局):门禁/ACL/跳数逐连接读全局配置;handler 取 processor/tcp
// 单例,与 TUN/HTTP 入站同一处理链(SNI 嗅探/MITM/B 自己的规则全继承)。
func ProductionDeps(selfDeviceID string) DepsFunc {
	return func() tcp.MeshInboundDeps {
		deps := tcp.MeshInboundDeps{
			SelfID:  selfDeviceID,
			Handler: tcp.GetHandler(),
		}
		var mesh *config.MeshConfig
		if cfg := config.GetGlobalConfig(); cfg != nil && cfg.Customer != nil {
			mesh = cfg.Customer.Mesh
		}
		// EffectiveMaxHops nil 接收者安全:Mesh==nil → 默认跳数(此时门禁
		// 已关,跳数只是保持 deps 形态完整)。
		deps.MaxHops = mesh.EffectiveMaxHops()
		if mesh == nil {
			// mesh 层未配置 = 不参与:ForwardEnabled=false + PeerAllowed=nil
			// (mesh_inbound 对 nil ACL 全拒),无需额外标志位。
			return deps
		}
		deps.ForwardEnabled = mesh.EffectiveForwardEnabled()
		peerIDs := make([]string, 0, len(mesh.Peers))
		for _, peer := range mesh.Peers {
			if id := strings.TrimSpace(peer.DeviceID); id != "" {
				peerIDs = append(peerIDs, id)
			}
		}
		deps.PeerAllowed = func(deviceID string) bool {
			for _, id := range peerIDs {
				if id == deviceID {
					return true
				}
			}
			return false
		}
		return deps
	}
}
