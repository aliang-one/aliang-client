package services

// agent_listener_processexit_test.go process_exit watcher 单测(specs/005-event-
// listener §3/§1.5,plan 2026-11-21 1b Task A1):代启命令退出上行帧契约 /
// expect_code 不匹配静默 / 启动失败本地置 error / 超时杀取 -1 / boot 重启不重建
// (agent_restarted)/ 钩子生命周期 / pid 模式探测消失 / 工具面 create wire 形状。
//
// 缝纪律(同 file_tail 测试):串行,禁 t.Parallel;统一经
// setupProcessExitTest(复用 setupFileTailTest 的 home/uplink/devID/清场 +
// process_exit 旋钮 shrink/还原)。

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupProcessExitTest process_exit 测试装配:复用 file_tail 装配(home/uplink
// 捕获/dev-local 缝/watcher 清场),再缩短 pid 轮询;exec 超时旋钮默认留给
// 各用例按需 shrink(超时用例单独 100ms,其余保持 5s 防误杀)。
func setupProcessExitTest(t *testing.T) (string, *fileTailUplinkCapture) {
	t.Helper()
	home, capture := setupFileTailTest(t)
	oldPoll := listenerProcExitPollInterval.Load()
	oldTimeout := listenerProcExitExecTimeout.Load()
	listenerProcExitPollInterval.Store(20 * time.Millisecond)
	listenerProcExitExecTimeout.Store(5 * time.Second)
	t.Cleanup(func() {
		listenerProcExitPollInterval.Store(oldPoll)
		listenerProcExitExecTimeout.Store(oldTimeout)
	})
	return home, capture
}

// processExitMirror 造一行 process_exit 镜像(params 为 JSON 字符串;默认本机
// 设备行,与 setupFileTailTest 的 dev-local 缝一致)。
func processExitMirror(id, updatedAt, params string) ListenerMirror {
	return ListenerMirror{
		ID:             id,
		SourceType:     "process_exit",
		SourceDeviceID: "dev-local",
		Status:         "active",
		UpdatedAt:      updatedAt,
		SourceParams:   json.RawMessage(params),
	}
}

// processExitRegistryIDs process_exit 注册表 id 快照(锁内读)。
func processExitRegistryIDs() []string {
	listenerProcExitMu.Lock()
	defer listenerProcExitMu.Unlock()
	ids := make([]string, 0, len(listenerProcExitWatchers))
	for id := range listenerProcExitWatchers {
		ids = append(ids, id)
	}
	return ids
}

// processExitRegistryEntry 取在册 process_exit watcher(锁内读;spec 字段构造
// 后不变,经注册表互斥量发布,读安全)。
func processExitRegistryEntry(id string) (*processExitWatcher, bool) {
	listenerProcExitMu.Lock()
	defer listenerProcExitMu.Unlock()
	w, ok := listenerProcExitWatchers[id]
	return w, ok
}

// frameDetailMap 取帧 detail(map[string]interface{} 断言用)。
func frameDetailMap(t *testing.T, frame map[string]interface{}) map[string]interface{} {
	t.Helper()
	detail, ok := frame["detail"].(map[string]interface{})
	require.True(t, ok, "detail 须为对象: %+v", frame)
	return detail
}

// 1. 代启:命令退出 + expect_code=0 → 上行帧(exit_code=0、duration_ms>0、
// fingerprint 非空、source_type=process_exit)。用 /bin/sleep 0.15 保证时长
// 可断言(毫秒截断下 /bin/true 的 duration 可能落 0)。
func TestProcessExitCommandExitEmitsFrame(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-cmd", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 0.15","expect_code":0}`))))

	var frame map[string]interface{}
	require.Eventually(t, func() bool {
		frames := capture.snapshot()
		if len(frames) == 0 {
			return false
		}
		frame = frames[0]
		return true
	}, 5*time.Second, 10*time.Millisecond)

	assert.Equal(t, "listener.event", frame["type"])
	assert.Equal(t, "L-cmd", frame["listener_id"])
	assert.Equal(t, "process_exit", frame["source_type"])
	assert.NotEmpty(t, frame["fingerprint"], "fingerprint 非空")
	assert.Equal(t, "process exited (code 0)", frame["summary"])
	detail := frameDetailMap(t, frame)
	// uplink 缝收的是序列化前的原生 map:数值是 int/int64(JSON 后才是 float64)。
	assert.Equal(t, 0, detail["exit_code"])
	assert.Equal(t, "/bin/sleep 0.15", detail["command"])
	dur, ok := detail["duration_ms"].(int64)
	require.True(t, ok, "duration_ms 须为数值: %+v", detail)
	assert.Greater(t, dur, int64(0), "duration_ms > 0(命令实际运行了)")
	// 单发:发完即自停。
	waitFileTailCondition(t, func() bool { _, running := processExitRegistryEntry("L-cmd"); return !running })
	assert.Equal(t, 1, capture.count(), "恰一帧")
}

// 2. 代启 expect_code 不匹配(期望 1 实际 0)→ 不发帧;watcher 自停。
func TestProcessExitExpectCodeMismatchNoFrame(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-mismatch", "2026-11-21T08:00:00Z", `{"command":"/bin/true","expect_code":1}`))))

	// 自停即退出码裁决已发生(run 内 finish 先于自停),再断零帧。
	waitFileTailCondition(t, func() bool { _, running := processExitRegistryEntry("L-mismatch"); return !running })
	assert.Equal(t, 0, capture.count(), "expect_code 不匹配不得发帧")
}

// 3. 代启命令不存在 → 本地置 error(exec_failed),不上行。
func TestProcessExitCommandNotFoundLocalError(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-notfound", "2026-11-21T08:00:00Z", `{"command":"/nonexistent/aliang-test-cmd"}`))))

	waitFileTailCondition(t, func() bool {
		_, running := processExitRegistryEntry("L-notfound")
		if running {
			return false
		}
		m, ok := getListenerMirror("L-notfound")
		return ok && m.Status == "error" && m.ErrorNote == "exec_failed"
	})
	assert.Equal(t, 0, capture.count(), "启动失败不上行")
}

// 4. 代启超时杀:exec 超时旋钮 shrink 到 100ms,/bin/sleep 30 被杀 → 帧携带
// exit_code=-1(被杀结果即事件结果,task 定案)。
func TestProcessExitTimeoutKilled(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	listenerProcExitExecTimeout.Store(100 * time.Millisecond)
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-slow", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 30"}`))))

	var frame map[string]interface{}
	require.Eventually(t, func() bool {
		frames := capture.snapshot()
		if len(frames) == 0 {
			return false
		}
		frame = frames[0]
		return true
	}, 5*time.Second, 10*time.Millisecond)
	detail := frameDetailMap(t, frame)
	assert.Equal(t, -1, detail["exit_code"], "超时杀的退出码取 -1")
	assert.Contains(t, frame["summary"], "code -1")
}

// 5. agent 重启:boot 发现的 active 本机 process_exit 行不重建 watcher,本地
// 置 error(agent_restarted);paused/他机行不注记。
func TestProcessExitBootMarksAgentRestarted(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	paused := processExitMirror("L-pe-paused", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 5"}`)
	paused.Status = "paused"
	foreign := processExitMirror("L-pe-foreign", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 5"}`)
	foreign.SourceDeviceID = "dev-other"
	require.NoError(t, saveListenerMirrors([]ListenerMirror{
		processExitMirror("L-restart", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 5"}`),
		paused,
		foreign,
	}))

	StartEventListenerWatchers()

	_, running := processExitRegistryEntry("L-restart")
	assert.False(t, running, "boot 不得重建 process_exit watcher(重启风暴防线)")
	m, ok := getListenerMirror("L-restart")
	require.True(t, ok)
	assert.Equal(t, "error", m.Status)
	assert.Equal(t, "agent_restarted", m.ErrorNote)

	pm, _ := getListenerMirror("L-pe-paused")
	assert.Equal(t, "paused", pm.Status, "paused 行不注记(非重启打断形态)")
	fm, _ := getListenerMirror("L-pe-foreign")
	assert.Equal(t, "active", fm.Status, "他机行不注记(不归本机重启管)")
	assert.Equal(t, 0, capture.count())
}

// 6. 钩子生命周期:upsert 起 watcher;参数变更(command 换)→ 重启新实例;
// status→paused → 停;remove 帧 → 停(幂等)。pid 模式用真实存活子进程——
// 高位假 pid(如 999999)在 macOS 超 pid_max,首轮探测即「消失」自停,
// spawn 断言追不上(真实竞态,勿改回假 pid)。
func TestProcessExitHookLifecycleViaFrames(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	child := exec.Command("/bin/sleep", "60")
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	livePID := child.Process.Pid

	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-pehook", "2026-11-21T08:00:00Z", `{"command":"/bin/sleep 30"}`))))
	waitFileTailCondition(t, func() bool {
		w, ok := processExitRegistryEntry("L-pehook")
		return ok && w.spec.command == "/bin/sleep 30"
	})
	first, _ := processExitRegistryEntry("L-pehook")

	// 参数变更(command 换 pid 模式)→ 停旧起新(旧代启子进程随停被杀)。
	changed := processExitMirror("L-pehook", "2026-11-21T09:00:00Z",
		jsonMustMarshalString(t, map[string]interface{}{"pid": livePID}))
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, changed)))
	waitFileTailCondition(t, func() bool {
		w, ok := processExitRegistryEntry("L-pehook")
		return ok && w != first && w.spec.pid == livePID
	})

	// status→paused → 停。
	paused := processExitMirror("L-pehook", "2026-11-21T10:00:00Z",
		jsonMustMarshalString(t, map[string]interface{}{"pid": livePID}))
	paused.Status = "paused"
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, paused)))
	waitFileTailCondition(t, func() bool { _, ok := processExitRegistryEntry("L-pehook"); return !ok })

	// 重激活 → 再起;remove 帧 → 停(幂等)。
	active := processExitMirror("L-pehook", "2026-11-21T11:00:00Z",
		jsonMustMarshalString(t, map[string]interface{}{"pid": livePID}))
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t, active)))
	waitFileTailCondition(t, func() bool { _, ok := processExitRegistryEntry("L-pehook"); return ok })
	require.NoError(t, handleListenerRemoveFrame(map[string]interface{}{
		"type":     "listener.remove",
		"listener": map[string]interface{}{"id": "L-pehook", "updated_at": "2026-11-21T12:00:00Z"},
	}))
	waitFileTailCondition(t, func() bool { _, ok := processExitRegistryEntry("L-pehook"); return !ok })
	// 子进程全程未被监听杀掉(lifecycle 停的是 watcher,不是 pid 模式的目标)。
	assert.Equal(t, 0, capture.count(), "生命周期测试不发帧(目标进程存活到底)")
}

// 6b. pid 模式:自起子进程注册 pid → 杀+收尸 → 探测消失即上行(summary
// process gone、exit_code=null——非子进程拿不到退出码是固有限界;expect_code
// 在 pid 模式无退出码可比,忽略不拦)。
func TestProcessExitPidModeGoneEmitsFrame(t *testing.T) {
	_, capture := setupProcessExitTest(t)
	child := exec.Command("/bin/sleep", "30")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Wait() }) // 兜底收尸(正常路径下方已 Wait)

	pid := child.Process.Pid
	require.NoError(t, handleListenerUpsertFrame(listenerFrameFromMirror(t,
		processExitMirror("L-pid", "2026-11-21T08:00:00Z",
			jsonMustMarshalString(t, map[string]interface{}{"pid": pid, "expect_code": 0})))))
	waitFileTailCondition(t, func() bool {
		w, ok := processExitRegistryEntry("L-pid")
		return ok && w.spec.pid == pid
	})

	require.NoError(t, child.Process.Kill())
	_ = child.Wait() // 收尸:僵尸态下 signal 0 仍报存活,必须 reap 才探测得到消失

	var frame map[string]interface{}
	require.Eventually(t, func() bool {
		frames := capture.snapshot()
		if len(frames) == 0 {
			return false
		}
		frame = frames[0]
		return true
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "process_exit", frame["source_type"])
	assert.Contains(t, frame["summary"], "process gone")
	detail := frameDetailMap(t, frame)
	assert.Equal(t, pid, detail["pid"])
	assert.Nil(t, detail["exit_code"], "pid 模式无退出码,wire 为 null")
	assert.Contains(t, detail, "duration_ms")
	assert.NotEmpty(t, frame["fingerprint"])
}

// jsonMustMarshalString 测试速写:map → JSON 字符串。
func jsonMustMarshalString(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

// 7. 工具面放开(A2):process_exit + 合法 params 过本地预检,create wire 上
// source.type=process_exit、params 原样透传(server schema 是权威闸)。
func TestListenerToolCreateProcessExitAccepted(t *testing.T) {
	var gotBody []byte
	cap := newListenerToolTestEnv(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path == "/api/agent/listeners" {
			gotBody = body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"listener":{"id":"L-pe","status":"active"}}`))
			return
		}
		http.NotFound(w, r)
	})
	rc := listenerToolContext{sessionID: "sess-current"}
	res, err := listenerToolCreate(context.Background(), rc, "等构建",
		"process_exit", map[string]interface{}{"command": "make build", "expect_code": 0}, "",
		"构建结束 {{event}}", "once", "")
	if err != nil {
		t.Fatalf("process_exit 合法参数须过本地预检: %v", err)
	}
	if res["id"] != "L-pe" {
		t.Fatalf("result.id = %v", res["id"])
	}
	var sent map[string]json.RawMessage
	_ = json.Unmarshal(gotBody, &sent)
	var source struct {
		Type     string          `json:"type"`
		DeviceID string          `json:"device_id"`
		Params   json.RawMessage `json:"params"`
	}
	_ = json.Unmarshal(sent["source"], &source)
	if source.Type != "process_exit" {
		t.Fatalf("wire source.type 须为 process_exit: %s", gotBody)
	}
	if source.DeviceID != "dev-proxy-1" {
		t.Fatalf("wire source.device_id 须钉本机: %s", gotBody)
	}
	var params map[string]interface{}
	_ = json.Unmarshal(source.Params, &params)
	if params["command"] != "make build" || params["expect_code"] != float64(0) {
		t.Fatalf("params 须原样透传: %s", source.Params)
	}
	if n := len(cap.snapshot()); n != 1 {
		t.Fatalf("应恰一笔云端调用: %d", n)
	}
}
