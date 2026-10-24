package services

import (
	"os"
	"strings"
)

// 外部 TUI 会话的插话(steer)。会话不在 agent 注册表(手机侧的
// tuiDrivenRunning:导入/桌面终端里自发生长的 Claude Code 回合),但
// ~/.claude/sessions/<pid>.json 的 pid 记录带着 CC >= 2.1.224 写的跨会话
// messaging UDS socket——与 stop 的外部 SIGINT(interruptExternalClaudeTurn)
// 用的是同一份注册表。注入后 CC 把消息按「回合中排队」处理,当前回合结束
// 自动接续,ack=queued 与 TUI 注入回合(steerTUITurn)同语义。
//
// 返回 (result, errMsg):result ∈
//   - "queued"      注入成功且未被 bypass 门扣留
//   - "held"        CC 扣留了这条插话(权限门),transcript 无回显
//   - "error"       拨号/写入失败
//   - "not_running" 无 live 记录 / 记录非 busy / 无 socket——调用方落回既有
//     not_running ack,手机会按「回合已结束」处理
//
// 附件不支持(与 steerTUITurn 同口径:TUI 注入帧只有文本,图片必须走
// 「回合开始前下载到本地缓存」的通道,见 runTUITurn 的附件落地注释)。
// ccPeerDialInject / ccPeerWatchHeld 是包级变量,测试可拦截(与 TUI 注入
// 测试同一旋钮)。
func externalSteerAttempt(home, nativeSessionID, content string) (string, string) {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	home = strings.TrimSpace(home)
	if nativeSessionID == "" || home == "" {
		return "not_running", ""
	}
	record, live := liveClaudeTUIRecord(home, nativeSessionID)
	if !live {
		return "not_running", ""
	}
	// steer 的语义是「回合中插话」:记录非 busy 说明回合已结束,调用方应当
	// 走普通新消息路径(重开一轮),而不是把消息排队进一个静止的 TUI。
	if !strings.EqualFold(strings.TrimSpace(record.Status), "busy") {
		return "not_running", ""
	}
	socketPath := strings.TrimSpace(record.MessagingSocketPath)
	if socketPath == "" || !strings.HasPrefix(socketPath, "/") {
		// 老世代 CC 不写 messaging socket——无通道,如实报 not_running。
		return "not_running", ""
	}
	jsonlPath := ccPeerSessionJSONL(home, nativeSessionID)
	if jsonlPath == "" {
		return "not_running", ""
	}
	// held 观测以「当前 transcript 末尾」为起点:只盯注入之后新写的字节
	// (与 steerTUITurn 同口径,避免把历史字节重复扫一遍)。
	offset := int64(0)
	if info, err := os.Stat(jsonlPath); err == nil {
		offset = info.Size()
	}
	if err := ccPeerDialInject(socketPath, loadClaudePeerToken(home, record.PID), content, ccPeerTUIPermissionClass(jsonlPath)); err != nil {
		return "error", "无法把插话送进终端会话"
	}
	if ccPeerWatchHeld(jsonlPath, offset, ccPeerHeldWatchWindow) {
		return "held", "终端会话拒绝了这条插话，请在终端中处理"
	}
	return "queued", ""
}
