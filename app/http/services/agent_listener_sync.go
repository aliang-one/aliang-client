package services

// agent_listener_sync.go 监听器镜像下行同步(specs/005-event-listener §2/FR-4)。
// server 注册表 CRUD 落库后经既有 agent WS 下行推 listener.upsert /
// listener.remove,本文件消费这两帧:更新本地镜像存储(agent_listener_store.go,
// updated_at LWW 仲裁在 store 内)并触发 watcher 生命周期钩子(Task 9 接线缝)。
//
// 帧序保持:两帧在 handleRemoteAgentMessage 读循环内联处理(毫秒级本地小文件
// 写,与 device.settings/ai.session.rename 同族),不经 safeGo——upsert 的 LWW
// 仲裁以到达序为输入,并派徒增无谓拒收与钩子乱序。注意 Task 9 起钩子是真
// watcher 生命周期代码,内联路径上会引入有界停止等待(stop-grace ≤5s;实况
// 是毫秒级 poll 循环退出,只有 run 卡死才会顶到上限)。
//
// 容错口径:畸形帧报错由调用方记日志即返;listener 对象内未知键一律容忍
// (plain json.Unmarshal 前向兼容,server 加字段不要求 agent 同步升级)。
// 两个 handler 各自带 recover 护栏(旧工具臂分发处同款仓规,agent_remote_ws.go
// :395 一带:绕过 RunWithGuards 的内联分发自配护栏)——Task 9 的钩子会跑真
// watcher 生命周期代码,panic 就地吞掉记日志,绝不撕断读循环;不用 safeGo
// 换护栏是因为并派会破坏 LWW 仲裁的到达序输入。

import (
	"encoding/json"
	"fmt"

	"aliang.one/nursorgate/common/logger"
)

// listenerUpsertHook / listenerRemoveHook 是 watcher 生命周期接线缝(Task 9):
// 镜像落定后回调,默认 no-op,测试可 swap(同 listenerMirrorHome seam 纪律:
// 测试串行跑,swap 期间不得有后台调用方)。Task 9 语义:status 或
// source.params 变更 → 对应 watcher 重启(参数变更也必须重启 watcher);
// remove 钩子幂等(server 补偿重放 remove 是常态)。
var (
	listenerUpsertHook = func(ListenerMirror) {}
	listenerRemoveHook = func(string) {}
)

// handleListenerUpsertFrame 消费下行 listener.upsert:listener = server
// publicListener 序列化(snake_case,字段名与 ListenerMirror json tag 同名;
// 未知键容忍)。store 内 LWW 仲裁——乱序旧帧静默拒收(store Debug 日志),
// 此时镜像未变、不触发钩子(watcher 不折腾)。以「store 现值是否就是本帧」
// 判定落定:读循环串行派发下与 store 仲裁同输入;并发窗口内最坏效果是
// 多/少一次幂等的 watcher 重启,不损镜像。
func handleListenerUpsertFrame(msg map[string]interface{}) error {
	// recover 护栏:镜像写在前、钩子(Task 9 真 watcher 代码)在后——钩子
	// panic 不损已落盘镜像,也不撕断 read loop。
	defer func() {
		if r := recover(); r != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] upsert frame panic recovered: %v", r))
		}
	}()
	raw, ok := msg["listener"]
	if !ok || raw == nil {
		return fmt.Errorf("listener.upsert missing listener object")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("listener.upsert encode listener: %w", err)
	}
	var m ListenerMirror
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("listener.upsert decode listener: %w", err)
	}
	if m.ID == "" {
		// 无 id 行是废行:进镜像既无法定位也无法删除,按畸形拒收。
		return fmt.Errorf("listener.upsert listener missing id")
	}
	if err := upsertListenerMirror(m); err != nil {
		return fmt.Errorf("listener.upsert apply %s: %w", m.ID, err)
	}
	cur, applied := getListenerMirror(m.ID)
	if !applied || cur.UpdatedAt != m.UpdatedAt {
		logger.Debug(fmt.Sprintf("listener-sync: upsert %s updated_at=%s 未落定(乱序旧帧或已被更新帧覆盖),跳过钩子",
			m.ID, m.UpdatedAt))
		return nil
	}
	listenerUpsertHook(m)
	return nil
}

// handleListenerRemoveFrame 消费下行 listener.remove:载荷是 server 最小对象
// {id, updated_at},完整 listener 行亦容忍(只取 id)。无论镜像中是否存在该行
// 都回调钩子(定案):server 补偿重放 remove 是常态,Task 9 的 watcher stop
// 必须幂等,漏调钩子会让已停 watcher 悬死。
func handleListenerRemoveFrame(msg map[string]interface{}) error {
	// recover 护栏:同 upsert(镜像删在前、幂等停止钩子在后)。
	defer func() {
		if r := recover(); r != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] remove frame panic recovered: %v", r))
		}
	}()
	raw, ok := msg["listener"]
	if !ok || raw == nil {
		return fmt.Errorf("listener.remove missing listener object")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("listener.remove encode listener: %w", err)
	}
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return fmt.Errorf("listener.remove decode listener: %w", err)
	}
	if probe.ID == "" {
		return fmt.Errorf("listener.remove listener missing id")
	}
	if err := removeListenerMirror(probe.ID); err != nil {
		return fmt.Errorf("listener.remove apply %s: %w", probe.ID, err)
	}
	listenerRemoveHook(probe.ID)
	return nil
}
