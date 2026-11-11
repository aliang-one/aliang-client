// reconcile.go — 配置期望态到运行面的接线(Task 7):进程级 SSH 入口单例
// 的启停/重启协调。期望态来源=装配层 services.ReconcileSSHIngressFromConfig
// (customer 配置提交协调路径与 core 启动序列两处调用、一个实现),本包不读
// 配置类型——翻译(EffectiveSSHIngress→RunConfig,authorized_keys 行解析)
// 归装配层,本文件只管生命周期。
package ssh

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"sync"

	"aliang.one/nursorgate/common/logger"

	gossh "golang.org/x/crypto/ssh"
)

// reconcile 单例:mu 保护 srv/want 两个字段;IsRunning/Addr 与 Reconcile
// 同锁,读侧看不到半迁移状态。
var (
	reconcileMu   sync.Mutex
	reconcileSrv  *Server
	reconcileWant runIdentity
)

// runIdentity 是「是否需要重启」的期望态比对面:凭证集、host key 路径或
// 专用口(port,spec §6.4 修订)任一变化即重启。port=0(跟随统一口)与
// port 变到其他值都会触发重启——专用口的起/停/换口都是 listener 级变更。
//
// 凭证按公钥身份做集合语义比较——排序后逐字节,列表顺序变化不触发重启:
// 裸钥(authorizedKeys)比 Marshal 字节,mesh 凭证(meshKeys)比 Marshal
// 字节+"\x00"+DeviceID(尾注释也是身份的一部分,日志/审计标签变更同样
// 重启)。比对口径与 publicKeyCallback 的准入比较同源。
//
// LocalNodeID/ForwardGate 是闭包(活视图,连接期读时求值),不参与比对:
// 装配层每次提交传新闭包实例是常态(方法值),按闭包身份比对会退化成
// 「每次提交必重启」。闭包捕获的单例指针持续有效,不比对也不失新鲜。
type runIdentity struct {
	hostKeyPath    string
	port           uint16
	authorizedKeys [][]byte
	meshKeys       [][]byte
}

func newRunIdentity(rc *RunConfig, hostKeyPath string) runIdentity {
	return runIdentity{
		hostKeyPath:    hostKeyPath,
		port:           rc.Port,
		authorizedKeys: marshalKeyIdentity(rc.AuthorizedKeys),
		meshKeys:       marshalMeshKeyIdentity(rc.MeshKeys),
	}
}

func (a runIdentity) equal(b runIdentity) bool {
	return a.hostKeyPath == b.hostKeyPath &&
		a.port == b.port &&
		keyIdentityEqual(a.authorizedKeys, b.authorizedKeys) &&
		keyIdentityEqual(a.meshKeys, b.meshKeys)
}

// marshalKeyIdentity 把公钥列表归一为排序后的 Marshal 字节集合(只服务
// authorized 裸钥;mesh 凭证走 marshalMeshKeyIdentity)。
func marshalKeyIdentity(keys []gossh.PublicKey) [][]byte {
	out := make([][]byte, 0, len(keys))
	for _, key := range keys {
		out = append(out, key.Marshal())
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}

// marshalMeshKeyIdentity 把 mesh 凭证归一为排序后的身份集合:Marshal 字节
// + "\x00" + DeviceID(注释改了但不改钥字节也要重启,否则日志身份陈旧)。
func marshalMeshKeyIdentity(keys []MeshKey) [][]byte {
	out := make([][]byte, 0, len(keys))
	for _, mk := range keys {
		b := mk.Key.Marshal()
		line := make([]byte, 0, len(b)+1+len(mk.DeviceID))
		line = append(line, b...)
		line = append(line, 0x00)
		line = append(line, mk.DeviceID...)
		out = append(out, line)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}

func keyIdentityEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// Reconcile 把期望态作用到进程级 SSH 入口实例上(幂等):
//   - rc=nil → 期望停:停掉在跑实例(未跑为 no-op);
//   - rc!=nil → 期望跑:未跑/期望态变化(端口、凭证集、host key 路径)即
//     (重)启;期望态未变为 no-op——重复提交同配置不断连。
//
// localNodeID 是防环闭包注入缝(装配层=AgentService.currentDeviceID,
// 测试=闭包);Reconcile 统一注入到实际喂给 Server 的 RunConfig 副本,
// 调用方构造 rc 时该字段留空。rc 整体视为不可变(Server 契约),此处
// 取副本注入,不改写入参。
//
// 启停失败仅记日志不上抛:SSH 入口是服务面非关键路径,配置提交不因它
// 阻断(调用点约定);失败停留止态,下次 reconcile 重试。重启=先停旧再
// 启新:同端口无法并存双监听,变更瞬间有短暂断口(服务面可接受,不做
// 过硬切换)。
//
// Bind host 语义:专用口(Port>0)监听 ServiceBindHost(默认 127.0.0.1,
// 显式 --host 放宽)——「默认安全」姿态,LAN 开放需显式 --host;跟随统一口
// (Port=0)时可达面继承 56432 绑定策略(config.UnifiedIngressListenAddr:
// ssh_ingress 开 0.0.0.0,否则 ServiceBindHost,SPEC §5.5)。
func Reconcile(rc *RunConfig, hostKeyPath string, localNodeID func() string) {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()

	if rc == nil {
		stopReconciledLocked()
		return
	}

	want := newRunIdentity(rc, hostKeyPath)
	if reconcileSrv != nil && reconcileSrv.IsRunning() && want.equal(reconcileWant) {
		return // 已在期望态:幂等 no-op
	}
	stopReconciledLocked()

	effective := *rc
	effective.LocalNodeID = localNodeID
	srv := NewServer(effective, hostKeyPath)
	if err := srv.Start(); err != nil {
		logger.Error(fmt.Sprintf("SSH ingress reconcile start failed (stays stopped until next reconcile): %v", err))
		reconcileSrv = nil
		reconcileWant = runIdentity{}
		return
	}
	reconcileSrv = srv
	reconcileWant = want
}

// stopReconciledLocked 停掉在跑实例并清空期望态记账(未跑为 no-op)。
// 调用方须持 reconcileMu。
func stopReconciledLocked() {
	if reconcileSrv == nil {
		return
	}
	if err := reconcileSrv.Stop(); err != nil {
		logger.Warn(fmt.Sprintf("SSH ingress reconcile stop: %v", err))
	}
	reconcileSrv = nil
	reconcileWant = runIdentity{}
}

// IsRunning 报告 reconcile 管理的入口实例是否在跑(装配层与集成测试的
// 观测点;与 Server.IsRunning 同名同义,作用于进程级单例)。
func IsRunning() bool {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	return reconcileSrv != nil && reconcileSrv.IsRunning()
}

// Addr 返回 reconcile 管理实例的专用监听口地址;未跑或未起专用口(Port=0
// 跟随统一口)返回空串。仅观测专用口——统一口 56432 的可达性以
// config.UnifiedIngressListenAddr 为准。
func Addr() string {
	reconcileMu.Lock()
	defer reconcileMu.Unlock()
	if reconcileSrv == nil {
		return ""
	}
	return reconcileSrv.Addr()
}

// HandleIngressConn 是统一口分派的 SSH 入口缝(spec §6.4):把一条已被
// 56432 嗅探为 SSH 的连接交给当前 reconcile 实例处理。
//
// 返回 false = 入口未配置/未启动(期望停),调用方断连且不回显任何协议字节
// ——与合并前「入口未启动则无人监听、连接被拒」同观感。
//
// 锁纪律:只在锁内取实例快照,serve 全在锁外——握手是长事务,持 reconcileMu
// 会闸住并发的配置提交与状态查询(与 stopReconciledLocked 的短临界区同口径)。
// 实例被 Reconcile 换掉的瞬间,在途 ServeConn 由其自身 Stop 的连接快照拆除
// +根 ctx 兜底覆盖,不会静默留活(评审 B1:注入连接挂 Start 根取消链)。
func HandleIngressConn(conn net.Conn) bool {
	reconcileMu.Lock()
	srv := reconcileSrv
	reconcileMu.Unlock()
	if srv == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return false
	}
	srv.ServeConn(conn)
	return true
}
