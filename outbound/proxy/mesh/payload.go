// payload.go 承担 mesh 出口的目标构造与 ExtraData payload 编码:
// hostPortFromMetadata 把四形态的连接元数据拆成 (host, port) 二元组,
// exitPayload 再编码为 mesh-exit channel 的 JSON ExtraData(tcp 带 h/p;
// dns 特殊目标无 h/p)。导出缝 ExitPayloadForMetadata/ErrUnbuildableTarget
// 供集成测试(test/mesh_integration_test.go)做 payload 等价断言复用。
package mesh

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/meshproto"
)

// exitMeta 是 mesh-exit channel ExtraData 的编码形态(t=tcp 带 h/p;
// t=dns 无其它字段)。P 是 uint16 JSON 数字——B 侧按 uint16 反序列化,
// 字符串会直接解析失败,不要改成 string。
type exitMeta struct {
	T string `json:"t"`
	H string `json:"h"`
	P uint16 `json:"p"`
	// Via 出口链路经停链(2026-11-12):出口侧原样传递 md.MeshVia;本跳
	// 自己的 id 由委托拨号点(processor/tcp mesh_delegate)在拨出前追加。
	// dns 变体无链语义,恒不带。键用单字母 v 控制 ExtraData 体积(512 上限)。
	Via []string `json:"v,omitempty"`
}

// errUnbuildableTarget 元数据无法构造出口目标(HostName 空/纯空白且
// DstIP 无效,或端口越界/为零):出口按「不可构造」拒绝,错误回源不吞。
var errUnbuildableTarget = errors.New("mesh: target unbuildable from metadata")

// ErrUnbuildableTarget 供跨包 errors.Is 判定。
func ErrUnbuildableTarget() error { return errUnbuildableTarget }

// ExitPayloadForMetadata 是 exitPayload 的导出别名,供 processor/tcp
// 兜底拨号路径构造 payload(Task E1 消费)。
func ExitPayloadForMetadata(md *M.Metadata) []byte { return exitPayload(md) }

// hostPortFromMetadata 由旧 targetFromMetadata 原样搬迁,唯一改动是返回值
// 由「拼接后的 target 串」改为「(host, port) 二元组」。
// 三种输入形态必须保持原行为:
//
//	HostName 已含端口(host:port)  → 原样拆分
//	HostName 裸域名 / 裸 IP        → DstPort(0 → 443)
//	HostName 空 + DstIP 有效       → DstIP:DstPort(0 → 443)
func hostPortFromMetadata(md *M.Metadata) (host, port string, ok bool) {
	if md == nil {
		return "", "", false
	}
	h := strings.TrimSpace(md.HostName)
	if h == "" {
		if !md.DstIP.IsValid() {
			return "", "", false
		}
		h = md.DstIP.String()
	}
	// HostName 自身可能已带端口:先按 host:port 解析,成功后直接采用。
	if ph, pp, err := net.SplitHostPort(h); err == nil && pp != "" {
		return ph, pp, true
	}
	p := int(md.DstPort)
	if p == 0 {
		p = 443
	}
	return h, strconv.Itoa(p), true
}

func exitPayload(md *M.Metadata) []byte {
	if md == nil {
		return nil // 防御:nil md 不可构造目标
	}
	// dns: 特殊目标在 hostPortFromMetadata 之前判定,否则 "dns:" 会被
	// SplitHostPort 判失败、再被拼成 "dns::443"。
	if meshproto.IsDNSTarget(strings.TrimSpace(md.HostName)) {
		return []byte(`{"t":"dns"}`) // 无 h/p,见 SPEC §4.1 / §9
	}
	host, portStr, ok := hostPortFromMetadata(md)
	if !ok {
		return nil // 调用方按「无法构造目标」拒绝
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil || port == 0 {
		return nil // 端口非法同样按不可构造处理
	}
	meta := exitMeta{T: "tcp", H: host, P: uint16(port)}
	if len(md.MeshVia) > 0 {
		meta.Via = md.MeshVia
	}
	b, _ := json.Marshal(meta)
	return b
}
