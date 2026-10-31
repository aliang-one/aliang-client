package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentAILineageNotice 是谱系标记条目的正文：面向人的同步说明。
const agentAILineageNotice = "同步通知/Sync notice: 手机端发来的新消息已由 AI 回复完毕,本轮对话已并入上方历史记录。/ A phone turn completed and its exchange is merged into the history above."

// 会话谱系维护：CC 的 resume 只渲染"最新 cli(entrypoint) 链"的祖先——手机回合
// 由 agent 无头 claude(sdk-cli) 写入，挂在交互链尖下做兄弟分支，TUI 关闭期间
// 的回合在退出再打开后永久不可见（2026-09-26 b1f82c61 / 2026-09-28 3d06bb04
// 两次实测，手工接链两次）。修复：检测到"最新 cli 链尖落后于文件真实尖端"
// 时，追加一条 cli entrypoint 的谱系标记、parentUuid 指向文件尖端——最新 cli
// 链立即变为包含手机回合的链，下次 resume 直接可见。纯追加零改写，幂等
// （标记本身就是新的 cli 链尖，二轮检测即线性）。

// ccLineageProbe 是谱系分析的最小条目视图：只解链路三要素，其余字段不碰。
type ccLineageProbe struct {
	Uuid       string `json:"uuid"`
	ParentUuid string `json:"parentUuid"`
	Entrypoint string `json:"entrypoint"`
}

// appendLineageMarker 在 jsonlPath 上检测分叉并按需追加谱系标记。
// nativeSessionID/cwd 写入标记条目（sessionId/cwd 字段与 CC 原生条目对齐）；
// notice 为标记正文（面向人阅读的同步说明）。best-effort：读不到/不分叉一律
// 静默返回 nil，绝不阻塞回合收尾。
func appendLineageMarker(jsonlPath, nativeSessionID, cwd, notice string) error {
	jsonlPath = strings.TrimSpace(jsonlPath)
	if jsonlPath == "" || strings.TrimSpace(nativeSessionID) == "" {
		return nil
	}
	var order []*ccLineageProbe
	byUuid := map[string]*ccLineageProbe{}
	// 复用 v1.1.41 流式读取器：病态超长行跳过不冻结（该行 uuid 缺席只可能
	// 让祖先行走得更早停——最坏是多补一条标记，无害）。
	forEachAgentSessionJSONLLine(jsonlPath, agentVibeJSONLMaxLineBytes, func(line []byte) bool {
		var e ccLineageProbe
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		if e.Uuid == "" {
			return true
		}
		p := e
		byUuid[e.Uuid] = &p
		order = append(order, &p)
		return true
	})
	if len(order) == 0 {
		return nil
	}
	fileTip := order[len(order)-1]
	newestCli := (*ccLineageProbe)(nil)
	for i := len(order) - 1; i >= 0; i-- {
		if order[i].Entrypoint == "cli" {
			newestCli = order[i]
			break
		}
	}
	if newestCli == nil {
		return nil // 无交互史：resume 本就沿 sdk 链走，无需标记
	}
	if ccLineageIsAncestor(byUuid, fileTip.Uuid, newestCli.Uuid) {
		return nil // 线性：cli 链已含文件尖端
	}
	marker := map[string]interface{}{
		"parentUuid":  fileTip.Uuid,
		"isSidechain": false,
		"type":        "system",
		"subtype":     "informational",
		"content":     notice,
		"isMeta":      false,
		"timestamp":   time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"uuid":        ccPeerMsgID(),
		"userType":    "external",
		"entrypoint":  "cli",
		"cwd":         cwd,
		"sessionId":   nativeSessionID,
	}
	b, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(jsonlPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("lineage marker: appended session=%s fileTip=%s cliTip=%s", nativeSessionID, fileTip.Uuid, newestCli.Uuid))
	return nil
}

// ccLineageIsAncestor 沿 parentUuid 自 from 向根走，报告是否途经 needle。
// 缺失父节点（跨文件引用/病态行被跳过）按根处理。
func ccLineageIsAncestor(byUuid map[string]*ccLineageProbe, needle, from string) bool {
	cur := from
	seen := map[string]bool{}
	for cur != "" && !seen[cur] {
		if cur == needle {
			return true
		}
		seen[cur] = true
		e := byUuid[cur]
		if e == nil {
			return false
		}
		cur = e.ParentUuid
	}
	return false
}

// agentAIContinuedInMaxHops 是 continued-in 标记链的最大跟随跳数（防环兜底）。
const agentAIContinuedInMaxHops = 5

// ccContinuedInMaxTailBytes 是 continued-in 尾部探测的读取上限。retired 判定
// 要求「标记之后没有对话活动」，而活动必然比标记更靠近文件尾——所以尾部窗口
// 一定覆盖「标记 + 其后全部活动」；标记落出窗口说明其后已有大量内容，视为
// 活会话（保守：不重定向）。
const ccContinuedInMaxTailBytes = 256 * 1024

// ccSessionContinuedInTarget 检测 jsonlPath 是否已被 CC 的 continued-in 机制
// 续走（/fork、continuation 都会写 {"type":"continued-in",
// "continuedInSessionId":...}）：尾部存在该标记，且标记之后没有真实对话活动
// （非 isMeta 的 user 行、assistant 行、回合结束标记）时，判定原会话已退役，
// 返回续接会话 id。
//
// 2026-10-30 1dc69343 事故：TUI /fork 续走会话后手机接管仍 resume 退役 id——
// TUI 注入落进不消化的旧身份（消息入队永不 dequeue，10 分钟零输出后被 idle
// 看门狗收尸），headless --resume 同样挂在幽灵转录上。判定刻意「尾部锚定」：
// 标记后若仍有真实对话（旧会话被继续使用，如事故后次日军线返场），视为活
// 会话不重定向。queue-operation/permission-mode 等 CC 元数据行不算活动——
// 事故形态恰恰是入队/元数据在涨而回合永不开始。
func ccSessionContinuedInTarget(jsonlPath string) (string, bool) {
	jsonlPath = strings.TrimSpace(jsonlPath)
	if jsonlPath == "" {
		return "", false
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return "", false
	}
	offset := int64(0)
	if info.Size() > ccContinuedInMaxTailBytes {
		offset = info.Size() - ccContinuedInMaxTailBytes
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return "", false
	}
	// 尾读起点落在半行中间时丢弃第一段残行再按行解析；offset==0 是全量读，
	// 首行完整，不可丢弃（单行标记文件会被整行丢掉）。
	if offset > 0 {
		if idx := bytes.IndexByte(buf, '\n'); idx >= 0 {
			buf = buf[idx+1:]
		} else {
			return "", false
		}
	}
	target := ""
	retired := false
	for _, line := range bytes.Split(buf, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		// continued-in 标记本身不是对话活动，先于通用分类处理。
		var marker struct {
			Type                 string `json:"type"`
			ContinuedInSessionID string `json:"continuedInSessionId"`
		}
		if json.Unmarshal(trimmed, &marker) == nil && marker.Type == "continued-in" {
			if id := strings.TrimSpace(marker.ContinuedInSessionID); id != "" {
				target, retired = id, true
			}
			continue
		}
		// 活动判定与 TUI 路由的 classifyTranscriptLine 同一权威定义：本地命令
		// 回显、tool_result 形态的 user 行等噪声不算真实对话——否则一条噪声行
		// 就会永久击穿退役判定（2026-10-31 评审定谳）。
		switch classifyTranscriptLine(trimmed) {
		case transcriptUserEcho, transcriptAssistant, transcriptTurnEnd:
			retired = false
		}
	}
	if !retired {
		return "", false
	}
	return target, true
}

// ccFollowContinuedInChain 沿 continued-in 标记链跟随到链尖：A 退役→B、B 又
// 退役→C 时返回 C。最多 maxHops 跳，环即停。返回的链尖保证 transcript 可恢复：
// 链尖 jsonl 缺失（CC 清理/删除）时回退到最近可恢复的祖先——把 resume 指向
// 幽灵 id 比不重定向更糟（CLI resume-missing 会清空恢复点降级成 fresh，
// 2026-10-31 评审定谳）；回退到 original 本身时调用方的 id==current 守卫自然
// 退化成不重定向。链上任一环未被判定退役即提前停——续接会话本身是活的。
func ccFollowContinuedInChain(home, currentID string, maxHops int) (string, bool) {
	home = strings.TrimSpace(home)
	currentID = strings.TrimSpace(currentID)
	if home == "" || currentID == "" {
		return "", false
	}
	id, retired := ccSessionContinuedInTarget(ccPeerSessionJSONL(home, currentID))
	if !retired {
		return "", false
	}
	resumable := currentID
	seen := map[string]bool{}
	for hop := 0; hop < maxHops; hop++ {
		if id == "" || seen[id] {
			break
		}
		seen[id] = true
		nextPath := ccPeerSessionJSONL(home, id)
		if nextPath == "" {
			return resumable, true
		}
		resumable = id
		next, nextRetired := ccSessionContinuedInTarget(nextPath)
		if !nextRetired {
			return id, true
		}
		id = next
	}
	return id, true
}

// redirectAgentAIContinuedSession 在 runUserMessage 的 TUI busy 预检之前执行
// continued-in 重定向：仅 claude 族（有 CC jsonl 概念）且 resume 目标已退役时，
// 把 session 恢复点与绑定记录一并改指续接会话并打日志。TUI 注入与 headless
// --resume 两条执行路由共用这一修正。
//
// 锁纪律：resumeSessionID 的其余读写方全部持 m.mu（2026-10-31 评审定谳的无锁
// 竞态/丢更新），这里的读、CAS 写、调用方对象写全部在临界区内；文件探测刻意
// 放在锁外。
func (m *agentAIManager) redirectAgentAIContinuedSession(session *agentAISession, provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "claude", "claudecode", "auto":
	default:
		return strings.TrimSpace(session.resumeSessionID)
	}
	m.mu.Lock()
	current := strings.TrimSpace(session.resumeSessionID)
	m.mu.Unlock()
	if current == "" {
		return ""
	}
	home := externalTUIHome()
	// busy 守卫锚定原身份：CC 的 pid 记录在 /fork 后仍按旧 id 记账（事故实录，
	// 正是它让注入路由命中了退役身份），TUI 正忙时若照常重定向，busy 预检会
	// 去查续接 id 的记录而查无此记录——双写防线被解除。此刻保持旧 id，让既有
	// tui_busy 拒绝路径照常工作。
	if rec, live := liveClaudeTUIRecord(home, current); live &&
		strings.EqualFold(strings.TrimSpace(rec.Status), "busy") {
		logger.Info(fmt.Sprintf("ai.run.resume.redirect: session=%s skip, TUI busy on original identity %s", session.id, current))
		return current
	}
	id, retired := ccFollowContinuedInChain(home, current, agentAIContinuedInMaxHops)
	if !retired || id == "" || id == current {
		return current
	}
	logger.Info(fmt.Sprintf("ai.run.resume.redirect: session=%s %s -> %s (continued-in tail)", session.id, current, id))
	rebind := false
	m.mu.Lock()
	// CAS：探测窗口内恢复点被并发写（out-of-band create / 前轮迟到钉点）时
	// 让位，不覆盖权威绑定；空值视为已清空的钉点，允许重定向重建。
	skip := false
	if cur := m.sessions[session.id]; cur != nil {
		if now := strings.TrimSpace(cur.resumeSessionID); now != current && now != "" {
			logger.Info(fmt.Sprintf("ai.run.resume.redirect: session=%s skip, resume point changed concurrently to %q", session.id, now))
			skip = true
		} else {
			cur.resumeSessionID = id
		}
	}
	if !skip {
		// 调用方对象与映射同临界区写：runUserMessage 后续在锁内从该指针取
		// 快照，漏写会让回合仍带着退役 id 走下去。
		session.resumeSessionID = id
		// 绑定同帧换绑：confirmBinding 以 bindings 里的 native id 为期望值，
		// 不同步会让重定向后的 spawn 全部死于 "native session id mismatch"
		// （2026-10-31 评审定谳）。
		if binding, ok := m.bindings[session.id]; ok && binding.NativeSessionID == current {
			binding.NativeSessionID = id
			binding.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			m.bindings[session.id] = binding
			rebind = true
		}
	}
	m.mu.Unlock()
	if rebind {
		if err := m.persistIdentityState(); err != nil {
			logger.Info(fmt.Sprintf("ai.run.resume.redirect: session=%s persistIdentityState: %v", session.id, err))
		}
	}
	if skip {
		// 并发写赢家为准，调用方视图跟上。
		m.mu.Lock()
		if cur := m.sessions[session.id]; cur != nil {
			session.resumeSessionID = cur.resumeSessionID
		}
		m.mu.Unlock()
	}
	return session.resumeSessionID
}
