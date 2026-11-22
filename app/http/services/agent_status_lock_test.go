package services

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// 2026-11-22 生产死锁善后回归:AgentService.s.mu 被 register/sync 路径长时
// 持有(最坏 ~50s)期间,仪表盘/手机以 ~5s 节奏轮询 GET /api/agent/status,
// 每个请求在 s.mu.Lock 无限排队——锁被焊死时即当日「状态拉不起来」现场。
// Status() 必须支持锁忙降级:TryLock 失败立即返回最近一次已知状态并显式
// 标记,绝不无限排队。

func newStatusLockTestEnv(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
}

// TestStatusReturnsLastKnownWhileLockBusy 锁忙降级契约:调用方持锁时 Status()
// 必须在看门狗窗口内返回「最近一次已知状态 + 忙标记」,不得排队等锁。
func TestStatusReturnsLastKnownWhileLockBusy(t *testing.T) {
	newStatusLockTestEnv(t)

	svc := &AgentService{client: &http.Client{Timeout: time.Second}}

	// 先无锁跑一次真实 Status:既验证基线路径,也为锁忙降级提供「最近一次
	// 已知状态」的真实来源(生产中 boot 后首轮询即完成同样的事)。
	first := svc.Status()

	// 复刻锁忙现场:sync/register 持锁期间仪表盘轮询打进来。
	svc.mu.Lock()
	done := make(chan models.AgentStatusResponse, 1)
	go func() { done <- svc.Status() }()

	select {
	case resp := <-done:
		if !resp.Degraded {
			t.Fatal("锁忙降级返回未标记 Degraded=true,前端无法辨识非实时快照")
		}
		if !strings.Contains(resp.Message, "忙") {
			t.Fatalf("锁忙降级返回未带忙标记 message=%q", resp.Message)
		}
		if resp.Status != first.Status {
			t.Fatalf("锁忙返回 status=%q, 应为最近已知快照 %q(保真,不得编造状态)", resp.Status, first.Status)
		}
	case <-time.After(3 * time.Second):
		svc.mu.Unlock()
		t.Fatal("Status() 在锁被占用时 3s 未返回:轮询无限排队(生产事故 2026-11-22 形态)")
	}
	svc.mu.Unlock()
}

// TestStatusSnapshotIsolatesDeviceMutation(2026-11-22 对抗评审 must_fix):
// 快照必须对 *AgentDevice 做值拷贝。锁下多处原地改写同一指针
// (syncRuntimeDeviceStatusLocked/applyRemoteDeviceSettings/pinPermanentDevice
// IDLocked/ensureDeviceIdentityLocked),降级路径在锁外 marshal 快照——共享
// 指针即 Go 内存模型级数据竞争(并发 string 撕裂读)。
func TestStatusSnapshotIsolatesDeviceMutation(t *testing.T) {
	newStatusLockTestEnv(t)

	svc := &AgentService{client: &http.Client{Timeout: time.Second}}
	svc.state.Device = &models.AgentDevice{Status: "online", DeviceID: "dev-a", Name: "MacBook"}

	// 正常采集,回填快照。
	if resp := svc.Status(); resp.Device == nil {
		t.Fatal("正常 Status 未携带 Device,无法验证快照隔离")
	}

	// 复刻锁下原地改写(生产形态)。
	svc.state.Device.Status = "offline"

	// 锁忙 → 降级读快照:不得见到改写后的值。
	svc.mu.Lock()
	resp := svc.Status()
	svc.mu.Unlock()

	if !resp.Degraded {
		t.Fatal("持锁调用未走降级路径,测试前提不成立")
	}
	if resp.Device == nil || resp.Device.Status != "online" {
		got := "nil"
		if resp.Device != nil {
			got = resp.Device.Status
		}
		t.Fatalf("降级快照读到锁下改写后的 Device.Status=%q(want online):快照共享 *AgentDevice 指针=数据竞争", got)
	}
}
