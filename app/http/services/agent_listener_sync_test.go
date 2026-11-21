package services

// agent_listener_sync_test.go 监听器镜像下行同步单测(specs/005-event-listener
// §2/FR-4)。覆盖:upsert 新增/更新/乱序旧帧 LWW 拒收(拒收不触发钩子)、
// remove 存在/缺失(幂等钩子)/畸形载荷、经 handleRemoteAgentMessage 的整链
// 派发与 enabled-device 门控豁免、协议常量 wire 串。
// home 注入复用 agent_listener_store_test.go 的 withListenerMirrorHome;
// 钩子 swap 同款纪律:测试串行,禁 t.Parallel。

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// swapListenerSyncHooks swap 两个 watcher 钩子缝并记录调用;Cleanup 还原。
func swapListenerSyncHooks(t *testing.T) (upserts *[]ListenerMirror, removes *[]string) {
	t.Helper()
	var gotUpserts []ListenerMirror
	var gotRemoves []string
	oldU, oldR := listenerUpsertHook, listenerRemoveHook
	listenerUpsertHook = func(m ListenerMirror) { gotUpserts = append(gotUpserts, m) }
	listenerRemoveHook = func(id string) { gotRemoves = append(gotRemoves, id) }
	t.Cleanup(func() {
		listenerUpsertHook = oldU
		listenerRemoveHook = oldR
	})
	return &gotUpserts, &gotRemoves
}

// listenerFrameFromMirror 把镜像行经 JSON 往返转成 server publicListener 形态
// 的 map(snake_case);extraKV 逐对塞进 listener 对象模拟前向兼容未知键。
func listenerFrameFromMirror(t *testing.T, m ListenerMirror, extraKV ...[2]interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	var lm map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &lm))
	for _, kv := range extraKV {
		lm[kv[0].(string)] = kv[1]
	}
	return map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": lm}
}

// readListenerMirrorFileRows 直读镜像文件行(断言「文件里有」而非只有缓存)。
func readListenerMirrorFileRows(t *testing.T, home string) []ListenerMirror {
	t.Helper()
	raw, err := os.ReadFile(listenerMirrorFilePath(t, home))
	require.NoError(t, err)
	var rows []ListenerMirror
	require.NoError(t, json.Unmarshal(raw, &rows))
	return rows
}

func TestListenerProtocolConstants(t *testing.T) {
	assert.Equal(t, "listener.upsert", models.AgentEventListenerUpsert)
	assert.Equal(t, "listener.remove", models.AgentEventListenerRemove)
	assert.Equal(t, "listener.event", models.AgentEventListenerEvent)
	assert.Equal(t, "listener.event.reject", models.AgentEventListenerEventReject)
}

func TestListenerUpsertFrameNewMirrorAndHook(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	upserts, _ := swapListenerSyncHooks(t)

	src := sampleListenerMirror("L-new", "2026-11-21T08:00:00Z")
	// 前向兼容:server 未来加键不要求 agent 同步升级。
	frame := listenerFrameFromMirror(t, src, [2]interface{}{"future_field", "x"})

	require.NoError(t, handleListenerUpsertFrame(frame))

	// 镜像缓存与文件双落。
	got, ok := getListenerMirror("L-new")
	require.True(t, ok, "upsert 后镜像缓存必须有该行")
	assert.Equal(t, src.ID, got.ID)
	assert.Equal(t, src.Title, got.Title)
	assert.Equal(t, src.SourceType, got.SourceType)
	assert.Equal(t, src.Status, got.Status)
	assert.Equal(t, src.TargetSessionID, got.TargetSessionID)
	assert.Equal(t, src.UpdatedAt, got.UpdatedAt)
	assert.JSONEq(t, string(src.SourceParams), string(got.SourceParams), "source_params 经 map 往返键序会重排,语义比较")
	rows := readListenerMirrorFileRows(t, home)
	require.Len(t, rows, 1)
	assert.Equal(t, "L-new", rows[0].ID)

	// 钩子收到解析后的镜像(未知键被容忍、不报错)。
	require.Len(t, *upserts, 1)
	assert.Equal(t, "L-new", (*upserts)[0].ID)
	assert.Equal(t, src.UpdatedAt, (*upserts)[0].UpdatedAt)
	assert.JSONEq(t, string(src.SourceParams), string((*upserts)[0].SourceParams))
}

func TestListenerUpsertFrameLWWGate(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	upserts, _ := swapListenerSyncHooks(t)

	// 基线 t1。
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-1", "2026-11-21T08:00:00Z"))))
	require.Len(t, *upserts, 1)

	// 更新 t2(更旧标题换新值证明替换):落定 + 钩子。
	newer := sampleListenerMirror("L-1", "2026-11-21T09:00:00Z")
	newer.Title = "换新标题"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, newer)))
	got, ok := getListenerMirror("L-1")
	require.True(t, ok)
	assert.Equal(t, "换新标题", got.Title, "更新帧必须替换旧行")
	assert.Equal(t, "2026-11-21T09:00:00Z", got.UpdatedAt)
	require.Len(t, *upserts, 2, "有效更新必须触发钩子")

	// 乱序旧帧 t0:静默拒收(返回 nil)——镜像不变 + 钩子不触发。
	stale := sampleListenerMirror("L-1", "2026-11-21T07:00:00Z")
	stale.Title = "不该出现的旧标题"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, stale)), "LWW 拒收不是错误")
	got, ok = getListenerMirror("L-1")
	require.True(t, ok)
	assert.Equal(t, "换新标题", got.Title, "乱序旧帧不得改动镜像")
	assert.Equal(t, "2026-11-21T09:00:00Z", got.UpdatedAt)
	assert.Len(t, *upserts, 2, "拒收帧不得触发钩子(镜像未变,watcher 不折腾)")
	rows := readListenerMirrorFileRows(t, home)
	require.Len(t, rows, 1)
	assert.Equal(t, "换新标题", rows[0].Title)

	// 平局(相等 updated_at)新胜:store 应用替换 → 行换新 + 钩子照触发。重复
	// 推送同一版本会让 Task 9 多做一次幂等 watcher 重启——本断言把该行为登记
	// 在案(由 Task 9 的重启幂等兜住,不在本层去重)。
	tie := sampleListenerMirror("L-1", "2026-11-21T09:00:00Z")
	tie.Title = "平局新胜标题"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, tie)))
	got, ok = getListenerMirror("L-1")
	require.True(t, ok)
	assert.Equal(t, "平局新胜标题", got.Title, "平局(相等 updated_at)新帧必须胜出")
	assert.Len(t, *upserts, 3, "平局落定照触发钩子(重复推送=幂等重启,Task 9 兜底)")
}

func TestListenerRemoveFrameSemantics(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	_, removes := swapListenerSyncHooks(t)

	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-rm", "2026-11-21T08:00:00Z"))))

	// server 发最小对象 {id, updated_at}:删除落定 + 钩子带 id。
	require.NoError(t, handleListenerRemoveFrame(map[string]interface{}{
		"type":     models.AgentEventListenerRemove,
		"listener": map[string]interface{}{"id": "L-rm", "updated_at": "2026-11-21T09:00:00Z"},
	}))
	_, ok := getListenerMirror("L-rm")
	assert.False(t, ok, "remove 后镜像不得再有该行")
	assert.Empty(t, readListenerMirrorFileRows(t, home))
	require.Len(t, *removes, 1)
	assert.Equal(t, "L-rm", (*removes)[0])

	// 缺失行 remove(server 补偿重放):不报错;钩子照调——watcher stop 必须
	// 幂等,漏调会让已停 watcher 悬死(定案:无条件回调)。
	require.NoError(t, handleListenerRemoveFrame(map[string]interface{}{
		"type":     models.AgentEventListenerRemove,
		"listener": map[string]interface{}{"id": "L-rm", "updated_at": "2026-11-21T10:00:00Z"},
	}))
	require.Len(t, *removes, 2, "缺失行 remove 也必须回调钩子(幂等停止)")
	assert.Equal(t, "L-rm", (*removes)[1])

	// 全量对象形态(remove 容忍完整 listener 行,只取 id)。
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-full", "2026-11-21T08:00:00Z"))))
	require.NoError(t, handleListenerRemoveFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-full", "2026-11-21T09:00:00Z"))))
	_, ok = getListenerMirror("L-full")
	assert.False(t, ok, "全量对象形态 remove 亦须生效")
	require.Len(t, *removes, 3)
}

func TestListenerUpsertFrameMalformed(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	upserts, _ := swapListenerSyncHooks(t)

	// 基线一行,供「畸形帧不得污染镜像」对照。
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-keep", "2026-11-21T08:00:00Z"))))

	cases := []struct {
		name  string
		frame map[string]interface{}
	}{
		{"missing listener key", map[string]interface{}{"type": models.AgentEventListenerUpsert}},
		{"listener null", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": nil}},
		{"listener string", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": "not-an-object"}},
		{"listener number", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": 42}},
		{"listener array", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": []interface{}{"id"}}},
		{"listener object without id", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": map[string]interface{}{"title": "无 id 行"}}},
		// 字段类型错配(cooldown_ms 应为数字):decode 阶段报错,不 panic、不落镜像。
		{"listener field type mismatch", map[string]interface{}{"type": models.AgentEventListenerUpsert, "listener": map[string]interface{}{"id": "x", "cooldown_ms": "not-a-number"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, handleListenerUpsertFrame(tc.frame), "畸形帧必须报错而非静默/panic")
			rows := readListenerMirrorFileRows(t, home)
			require.Len(t, rows, 1, "畸形帧不得改动镜像")
			assert.Equal(t, "L-keep", rows[0].ID)
		})
	}
	// 基线 upsert 合法触发过一次钩子;畸形帧不得再追加。
	assert.Len(t, *upserts, 1, "畸形帧不得触发钩子")
}

func TestListenerRemoveFrameMalformed(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	_, removes := swapListenerSyncHooks(t)

	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-keep", "2026-11-21T08:00:00Z"))))

	cases := []struct {
		name  string
		frame map[string]interface{}
	}{
		{"missing listener key", map[string]interface{}{"type": models.AgentEventListenerRemove}},
		{"listener null", map[string]interface{}{"type": models.AgentEventListenerRemove, "listener": nil}},
		{"listener string", map[string]interface{}{"type": models.AgentEventListenerRemove, "listener": "oops"}},
		{"listener object without id", map[string]interface{}{"type": models.AgentEventListenerRemove, "listener": map[string]interface{}{"updated_at": "2026-11-21T09:00:00Z"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, handleListenerRemoveFrame(tc.frame))
			_, ok := getListenerMirror("L-keep")
			require.True(t, ok, "畸形 remove 不得误删既有行")
		})
	}
	assert.Empty(t, *removes, "畸形 remove 不得触发钩子")
}

// TestListenerSyncHookPanicGuard 钩子 panic 护栏(I1):Task 9 的钩子会跑真
// watcher 生命周期代码,panic 必须被 handler 内 recover 就地吞掉——镜像已在
// 钩子前落定(不回滚),read loop 不被撕断(panic 不外逸)。
func TestListenerSyncHookPanicGuard(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)

	oldU, oldR := listenerUpsertHook, listenerRemoveHook
	listenerUpsertHook = func(ListenerMirror) { panic("watcher start exploded") }
	listenerRemoveHook = func(string) { panic("watcher stop exploded") }
	t.Cleanup(func() {
		listenerUpsertHook = oldU
		listenerRemoveHook = oldR
	})

	// upsert:镜像先落盘、钩子后炸——recover 兜住,返回 nil,不外逸。
	require.NotPanics(t, func() {
		require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, sampleListenerMirror("L-boom", "2026-11-21T08:00:00Z"))))
	})
	got, ok := getListenerMirror("L-boom")
	require.True(t, ok, "钩子炸前镜像已落定,不得回滚")
	assert.Equal(t, "2026-11-21T08:00:00Z", got.UpdatedAt)
	require.Len(t, readListenerMirrorFileRows(t, home), 1)

	// remove:镜像先删行、钩子后炸——同款护栏。
	require.NotPanics(t, func() {
		require.NoError(t, handleListenerRemoveFrame(map[string]interface{}{
			"type":     models.AgentEventListenerRemove,
			"listener": map[string]interface{}{"id": "L-boom", "updated_at": "2026-11-21T09:00:00Z"},
		}))
	})
	_, ok = getListenerMirror("L-boom")
	assert.False(t, ok, "remove 的镜像删行同样先于钩子,不得回滚")
}

// TestListenerRemoteSyncViaHandleRemoteAgentMessage 整链:帧经
// handleRemoteAgentMessage 派发(读循环同路径)→ 镜像落定;畸形帧不撕断
// (无 error 回帧、无 panic);两帧不进 enabled-device 门控名单(状态同步
// 类流量,与 device.settings/inventory.refresh 同族)。
func TestListenerRemoteSyncViaHandleRemoteAgentMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()

	// 镜像 home 缝指向独立目录,断言读文件而非缓存副作用。
	mirrorHome := t.TempDir()
	withListenerMirrorHome(t, mirrorHome)
	upserts, removes := swapListenerSyncHooks(t)

	var written []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			written = append(written, m)
		}
		return nil
	}

	service.handleRemoteAgentMessage(context.Background(),
		listenerFrameFromMirror(t, sampleListenerMirror("L-ws", "2026-11-21T08:00:00Z")), writeJSON)

	got, ok := getListenerMirror("L-ws")
	require.True(t, ok, "经 WS 派发的 upsert 必须落镜像")
	assert.Equal(t, "2026-11-21T08:00:00Z", got.UpdatedAt)
	assert.Len(t, readListenerMirrorFileRows(t, mirrorHome), 1)
	require.Len(t, *upserts, 1)

	service.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type":     models.AgentEventListenerRemove,
		"listener": map[string]interface{}{"id": "L-ws", "updated_at": "2026-11-21T09:00:00Z"},
	}, writeJSON)

	_, ok = getListenerMirror("L-ws")
	assert.False(t, ok, "经 WS 派发的 remove 必须删行")
	require.Len(t, *removes, 1)

	// 畸形帧经派发:记日志即返,不写 error 回帧(无 request_id 语义)、不 panic。
	service.handleRemoteAgentMessage(context.Background(), map[string]interface{}{
		"type": models.AgentEventListenerUpsert,
	}, writeJSON)
	assert.Empty(t, written, "listener 帧族不产生任何回帧")

	// 门控豁免:被禁用设备也须保持镜像同步(状态同步类,非控制面)。
	assert.False(t, remoteAgentMessageRequiresEnabledDevice(models.AgentEventListenerUpsert))
	assert.False(t, remoteAgentMessageRequiresEnabledDevice(models.AgentEventListenerRemove))
}
