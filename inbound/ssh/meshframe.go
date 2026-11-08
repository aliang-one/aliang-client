// meshframe.go — SSH 入口形态②(mesh 帧外壳)的帧头协议。
//
// 与组网分支(feat/mesh-forwarding)的 outbound/meshproto 同源:帧格式权威
// = docs/mesh-forwarding-network-design.md §3.2(来源 device_id / 流量类别 /
// 目标 host:port|裸域名|dns: / path vector / 跳数),首行 JSON 头 + 原始
// 字节双向转发。本分支独立实现、不 import mesh 代码(该分支不在本分支);
// 两分支合并后由集成批次统一为单一实现(计划 2026-11-08-ssh-ingress Task 5
// 与 spec §8 的冲突纪律)。
package ssh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// MeshHopsHardCap 是转发跳数硬上限(防环层 2 数值保险,design §7:
// 默认 5/上限 8)。与 mesh 分支同值的 meshproto.MaxHopsCap 对齐;注意
// 键名差异(本实现 MeshHopsHardCap / mesh 分支 MaxHopsCap)——合并统一
// 时二选一改名。
const MeshHopsHardCap = 8

// meshFrameHeaderLimit 界定帧头首行最大长度(64KB):跳数帽下合法帧头远
// 小于此,超长即拒——防畸形超大首行占住解析内存。同源 meshproto 上限为
// 4096;SSH 载体按本分支任务口径取 64KB,合并统一时以集成批次定夺。
const meshFrameHeaderLimit = 64 * 1024

// MeshFrameHeader 是形态② 帧头。字段名与 mesh 分支 meshproto.Header
// 逐一对齐(from/class/target/via/hops),合并时直接互通。
type MeshFrameHeader struct {
	From   string   `json:"from"`   // 发起节点 device_id(链头)
	Class  string   `json:"class"`  // 流量类别:self|ai|custom
	Target string   `json:"target"` // host:port / 裸域名 / dns:domain
	Via    []string `json:"via"`    // path vector:已访节点 device_id(含链头)
	Hops   int      `json:"hops"`   // 已转发跳数
}

// ParseMeshFrameHeader 从 channel 数据流读首行 JSON 帧头。r 须为新造的
// bufio.Reader:帧头之后 r 里可能已预读载荷字节,由调用方回放
// (见 injectMeshFrame 的 meshChannelConn)。
//
// 错误一律按「帧头解析失败」处理:断开不兜底(spec §5——节点凭证发不出
// 合法帧=对端实现错误)。拒绝项:非 JSON / 缺必填字段(from/class/target)/
// 首行超 64KB / 负跳数(防伪造 {"hops":-N} 绕过跳数帽)/ 控制字节
// (from/class/target 与 via 条目均进防环日志,拒绝对端经 JSON 转义注入
// 控制字符;后两项与同源 meshproto 的质量加固对齐)。Via 缺省归一为
// [From]——链头即首跳,与 meshproto 同语义。
func ParseMeshFrameHeader(r *bufio.Reader) (*MeshFrameHeader, error) {
	line, err := r.ReadSlice('\n')
	if len(line) > meshFrameHeaderLimit || errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("mesh frame header exceeds %d bytes", meshFrameHeaderLimit)
	}
	if err != nil {
		return nil, fmt.Errorf("mesh frame header read: %w", err)
	}
	var h MeshFrameHeader
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &h); err != nil {
		return nil, fmt.Errorf("mesh frame header json: %w", err)
	}
	if h.From == "" || h.Class == "" || h.Target == "" {
		return nil, errors.New("mesh frame header: from/class/target required")
	}
	if h.Hops < 0 {
		return nil, errors.New("mesh frame header: negative hops")
	}
	if hasControlByte(h.From) || hasControlByte(h.Class) || hasControlByte(h.Target) {
		return nil, errors.New("mesh frame header: control byte in from/class/target")
	}
	for i, id := range h.Via {
		if hasControlByte(id) {
			return nil, fmt.Errorf("mesh frame header: control byte in via[%d]", i)
		}
	}
	if h.Via == nil {
		h.Via = []string{h.From}
	}
	return &h, nil
}

// AppendHop 把本跳追加进 path vector 并递增跳数(防环层 1 主防线的记账侧,
// design §7)。环路自查(Via 含本机)与跳数帽(hops>=MeshHopsHardCap)由
// 注入路径先行判定并回写可区分错误行({"err":"loop"}/{"err":"hops-exceeded"}),
// 本方法只做追加——与 meshproto.AppendHop 的「自查+追加」合一形态在合并
// 统一时对齐。
func (h *MeshFrameHeader) AppendHop(deviceID string) {
	h.Via = append(h.Via, deviceID)
	h.Hops++
}

// hasControlByte 报告 s 是否含控制字节(<0x20)。
func hasControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return true
		}
	}
	return false
}
