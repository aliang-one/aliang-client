// Package ssh 提供节点内置的 SSH 入口服务(x/crypto sshd):
// 双形态准入——标准 ssh 客户端裸流(direct-tcpip)与 mesh-exit 通道注入,
// 公钥即形态区分器,禁密码;生命周期/host key/认证限速/通道安全收敛矩阵
// 见 server.go 与 ratelimit.go。
package ssh

import gossh "golang.org/x/crypto/ssh"

// 入口连接形态:认证时写入 Permissions.Extensions[extIngressForm],
// 通道层读取分流(Task 4/5 按形态注入管线)。
const (
	FormBare = "bare" // 形态①:authorized_keys 人用凭证,标准客户端裸流
	FormMesh = "mesh" // 形态②:mesh_authorized_keys 节点互连 mesh-exit 通道
)

// extIngressForm 是认证 Permissions 中携带入口形态的扩展键。
const extIngressForm = "ingress_form"

// MeshKey 是形态②(mesh)准入凭证:公钥 + authorized_keys 尾注释承载的
// device_id(日志/审计的人类可读身份,空则回落指纹前 16 字符)。Key 由
// parseSSHIngressMeshKeys 保证非 nil(gossh.ParseAuthorizedKey 契约)——
// 解析器是唯一来源,运行面比较/标签均不设 nil 防御(与 keyInList 同口径)。
type MeshKey struct {
	Key      gossh.PublicKey
	DeviceID string
}

// extMeshDeviceID 是认证 Permissions 中携带 mesh 对端身份的扩展键(进程内
// 数据,不上线——Extensions 由服务端产生与消费,SPEC §10.2)。
const extMeshDeviceID = "mesh_device_id"

// extKeyFingerprint 是认证 Permissions 中携带客户端公钥指纹的扩展键
// ("SHA256:xxx")。会话台账登记/踢出/拉黑都以指纹为公共身份口径。
const extKeyFingerprint = "key_fingerprint"

// BlockQuery 是一次准入黑名单查询:命中公钥指纹(如 "SHA256:xxx",gossh
// FingerprintSHA256 口径)或 mesh device_id(尾注释标签,可空)之一即拒绝。
// 与白名单判定同序发生在 publicKeyCallback:黑名单优先于白名单(同一把钥
// 两边都挂=拉黑)。
type BlockQuery struct {
	Fingerprint string
	DeviceID    string
}

// RunConfig 是服务运行期配置,由配置层 EffectiveSSHIngress 翻转而来
// (authorized_keys 行经 gossh.ParseAuthorizedKey 解析为 gossh.PublicKey)。
// 构造后视为不可变:配置变更由装配层(Reconcile)重建 Server 实例,
// 不原地改写——认证回调与通道处理全程只读此结构。
type RunConfig struct {
	// Port 专用监听口(spec §6.4 修订):0=跟随统一口 56432(不自持监听,
	// 可用性随代理运行态);>0=另开常驻专用口 ServiceBindHost:port(随入口
	// 开关)。翻译层把旧默认 56442 哨兵归零(存量配置升级不凭空开专用口),
	// 见 services.buildSSHIngressRunConfig。
	Port uint16
	// AuthorizedKeys 是形态①凭证(人用 authorized_keys)。
	AuthorizedKeys []gossh.PublicKey
	// MeshKeys 是形态②凭证(mesh_authorized_keys):公钥 + 尾注释 device_id。
	MeshKeys []MeshKey

	// Blocked 是准入拒绝名单活视图(customer.ssh_ingress.blocked_keys /
	// blocked_device_ids):认证期读时求值,变更即时生效不重启服务——
	// ForwardGate 同款闭包纪律(若纳入 runIdentity 比对,拉黑会被
	// 「凭证未变」短路冻结在旧实例里)。nil = 无黑名单,全放行(不比对)。
	Blocked func(BlockQuery) bool

	// MeshAdmission 是 mesh 形态的运行期活准入(自动组网批次):MeshKeys
	// 未命中的公钥查 beacon 自动 peer 活表(同账号+指纹+公钥+签名四要素,
	// lanbeacon.MeshAdmissionFor 同源),命中返回对端 device_id 即按 mesh
	// 形态准入。认证期读时求值、不参与 runIdentity 比对(发现新对端零重
	// 启,Blocked 同款闭包纪律)。nil = 关闭(仅手填 mesh_authorized_keys)。
	// 注意:MeshKeys 是手填权威,自动准入恒为其补集,两者不合并落盘。
	MeshAdmission func(key gossh.PublicKey) (deviceID string, ok bool)

	// LocalNodeID 返回本机节点标识。装配层仍注入(AgentService.currentDeviceID);
	// mesh-exit 形态自 D2 起不再消费(旧帧协议的 AppendHop 防环随帧协议删除,
	// 防环改由注入 Metadata 的 MeshInjected 标记承担,SPEC §6.2)。F1 已核:
	// 本包无读取方,字段保留供装配层继续注入,清理候选。
	LocalNodeID func() string

	// ForwardGate 返回转发总开关(customer.mesh.forward_enable 活视图)。
	// nil = fail-closed——装配层恒注入活闭包,nil 语义从「全放行」翻转为
	// 「拒绝」(SPEC §10.1 决策 #14)。allowed 名单职能已删除:ACL 由
	// MeshKeys 公钥列表承担,两套 ACL 并存会制造「公钥进了但名单没加」
	// 的失配。
	ForwardGate func() bool
}
