package lanbeacon

import (
	"sort"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// AutoPeer 自动组网对端视图:同账号候选里「指纹+mesh 公钥双要素齐」的
// 子集(自动组网批次)。这是对候选表的**派生只读视图**,没有独立生命
// 周期——候选被 TTL 剪枝/容量驱逐,对应 AutoPeer 即自然消失。
//
// ⚠️ 信任边界(评审 major 诚实化,勿回退措辞):beacon 是明文 UDP,
// 四要素(账号哈希可嗅探/指纹自报/公钥自报/自签自洽)整链无攻击者不可
// 得的秘密——签名只防「无签名伪造降级」,不防「自签新钥顶替」。因此
// AutoPeer/准入派生只供 **mesh.auto_peers 显式开启** 的部署使用(可信
// 局域网语义),且 ForwardGate 默认关是第二道缓冲;跨信任域硬绑定须走
// 平台账号级 roster(服务端下发,台账遗留)。消费方:ingress 认证活闭
// 包(MeshAdmissionFor)与拨号装配(PeerSpec 合并),两处各自查开关。
type AutoPeer struct {
	DeviceID   string
	Addr       string // 可拨地址(UDP 来源 IP + 报文 mesh_port)
	HostKey    string // 对端 ssh_ingress host key 指纹(SHA256:…)
	MeshPubKey string // 对端 mesh 公钥 authorized_keys 行(type+b64)
}

// isAutoPeerEligible 自动 peer 四要素(自洽性四项,非账号成员证明——见
// 类型注释信任边界):同账号(own=本机哈希,调用方先在锁外快照——本判
// 定固定在表锁内调用,provider 求值绝不可二次进 b.mu)+ host key 指纹
// 在场(SHA256: 形态)+ mesh 公钥行可解析 + 广播签名验签通过(未签名=
// 旧版形状,仅展示永不晋升)。
func (b *Beacon) isAutoPeerEligible(c lanCandidate, own string) bool {
	if !c.sigVerified {
		return false
	}
	if own == "" || c.AccountHash == "" || c.AccountHash != own {
		return false
	}
	if !strings.HasPrefix(c.CertFingerprint, "SHA256:") || c.pubKey == nil {
		return false
	}
	return strings.TrimSpace(c.MeshPubKey) != ""
}

// AutoPeers 返回当前自动组网对端视图(按 device_id 排序,确定性输出)。
// Default() 未装配(nil)= 空表。
func AutoPeers() []AutoPeer {
	b := Default()
	if b == nil {
		return []AutoPeer{}
	}
	return b.autoPeers()
}

func (b *Beacon) autoPeers() []AutoPeer {
	// own 先在锁外快照(provider 求值),再持表锁判定——判定路径不可重入
	// b.mu(嵌套锁死锁教训:isAutoPeerEligible 内不得再取本锁)。
	own := b.ownAccountHash()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AutoPeer, 0, len(b.candidates))
	for _, c := range b.candidates {
		if !b.isAutoPeerEligible(*c, own) {
			continue
		}
		out = append(out, AutoPeer{
			DeviceID:   c.DeviceID,
			Addr:       c.Addr,
			HostKey:    c.CertFingerprint,
			MeshPubKey: strings.TrimSpace(c.MeshPubKey),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

// MeshAdmissionFor 按公钥身份查自动 peer 表(ingress 认证活闭包的消费
// 形态):命中返回对端 device_id。比对口径与 inbound/ssh 同源——公钥
// Marshal() 字节(含类型与公钥体),不比文本行。
func MeshAdmissionFor(key gossh.PublicKey) (deviceID string, ok bool) {
	if key == nil {
		return "", false
	}
	b := Default()
	if b == nil {
		return "", false
	}
	want := key.Marshal()
	own := b.ownAccountHash() // 锁外快照(provider 求值),同 autoPeers 纪律
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.candidates {
		if c.pubKey == nil {
			continue
		}
		if string(c.pubKey.Marshal()) != string(want) {
			continue
		}
		// 公钥在场还不够:自动 peer 四要素(同账号+指纹+可解析公钥+验签
		// 通过)全查。不合格候选继续扫表不提前否决——攻击者可用「真实对端
		// 公钥行+异 device_id+无签名」造不合格条目,提前 return 会让合法
		// peer 的准入变成随机抛硬币(评审 major#1)。
		if !b.isAutoPeerEligible(*c, own) {
			continue
		}
		return c.DeviceID, true
	}
	return "", false
}
