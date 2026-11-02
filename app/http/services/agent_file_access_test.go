package services

import (
	"testing"
	"time"

	"aliang.one/nursorgate/internal/tccguard"
)

// installFileAccessHooks 装测试替身。
//
// Home 与 StateDir 必须一起覆盖：前者决定类别清单（家目录解析失败会让
// Categories() 返回空，sweep 直接空转），后者避免测试往真实
// ~/.aliang/tcc-grants.json 写授权记录。
func installFileAccessHooks(t *testing.T, attended bool, probe func(string) error) {
	t.Helper()
	home := t.TempDir()
	stateDir := t.TempDir()
	restore := tccguard.SetTestHooks(tccguard.Hooks{
		Supported: boolPtr(true),
		Attended:  func() bool { return attended },
		Probe:     probe,
		Home:      func() (string, error) { return home, nil },
		StateDir:  func() (string, error) { return stateDir, nil },
		ProbeWait: 2 * time.Second,
	})
	t.Cleanup(restore)
}

// TestFileAccessStartRefusesWhenUnattended 覆盖本功能最核心的保护：
// 无人值守时**绝不启动**探测——那只会制造一个没人能点的系统弹窗，正是要消灭
// 的场景（2026-11-01 一条命令卡 1h58m）。
func TestFileAccessStartRefusesWhenUnattended(t *testing.T) {
	probeCalled := false
	installFileAccessHooks(t, false, func(string) error {
		probeCalled = true
		return nil
	})

	svc := &agentFileAccessService{}
	snap, started := svc.Start()
	if started {
		t.Fatal("无人值守时不应启动授权 sweep")
	}
	if snap.LastErrorCode != "unattended" {
		t.Fatalf("lastErrorCode 应为 unattended，实际 %q", snap.LastErrorCode)
	}
	if snap.State == "running" {
		t.Fatal("状态不应是 running")
	}
	if probeCalled {
		t.Fatal("无人值守时绝不能探测（会弹无人应答的系统框）")
	}
}

// TestFileAccessStartRefusesWhenUnsupported 非 macOS / 守卫未启用时同样不启动。
func TestFileAccessStartRefusesWhenUnsupported(t *testing.T) {
	restore := tccguard.SetTestHooks(tccguard.Hooks{
		Supported: boolPtr(false),
		Probe:     func(string) error { return nil },
		Home:      func() (string, error) { return t.TempDir(), nil },
		StateDir:  func() (string, error) { return t.TempDir(), nil },
	})
	t.Cleanup(restore)

	svc := &agentFileAccessService{}
	_, started := svc.Start()
	if started {
		t.Fatal("不支持时不应启动授权 sweep")
	}
	if snap := svc.Snapshot(); snap.Supported {
		t.Fatal("快照应报告 supported=false")
	}
}

// TestFileAccessStartIsIdempotent 已在跑时重入必须返回当前快照且不重复启动。
func TestFileAccessStartIsIdempotent(t *testing.T) {
	probeCalls := make(chan struct{}, 64)
	release := make(chan struct{})
	installFileAccessHooks(t, true, func(string) error {
		probeCalls <- struct{}{}
		<-release // 卡住第一类，制造「正在跑」窗口
		return nil
	})

	svc := &agentFileAccessService{}
	if _, started := svc.Start(); !started {
		t.Fatal("首次 Start 应当启动")
	}
	select {
	case <-probeCalls:
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内没有发起任何探测，sweep 可能空转（类别清单为空）")
	}

	if _, started := svc.Start(); started {
		t.Fatal("running 时重入不应再次启动")
	}

	close(release)
	// 等 sweep 收尾，避免 goroutine 跨测试存活。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Snapshot().State == "done" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("sweep 未在 10s 内收尾")
}

// TestFileAccessSnapshotReportsCachedGrants 未跑过时，快照应把已有 grant
// 显示为 granted（只读缓存，绝不 stat 受保护目录）。
func TestFileAccessSnapshotReportsCachedGrants(t *testing.T) {
	installFileAccessHooks(t, true, func(string) error { return nil })

	svc := &agentFileAccessService{}
	snap := svc.Snapshot()
	if !snap.Supported {
		t.Fatal("supported 应为 true")
	}
	if snap.State != "idle" {
		t.Fatalf("初始状态应为 idle，实际 %q", snap.State)
	}
	if len(snap.Categories) == 0 {
		t.Fatal("应至少列出受保护类别")
	}
}

// TestFileAccessSweepStopsAfterPending 一类卡住后必须把剩余标记为 skipped：
// 继续往下弹只会再造几个无人应答的系统框。
func TestFileAccessSweepStopsAfterPending(t *testing.T) {
	installFileAccessHooks(t, true, func(string) error {
		// 模拟用户不点：探测一直不返回，直到 probeWait 超时 → pending。
		time.Sleep(3 * time.Second)
		return nil
	})

	svc := &agentFileAccessService{}
	if _, started := svc.Start(); !started {
		t.Fatal("应当启动")
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snap := svc.Snapshot()
		if snap.State == "done" {
			skipped := 0
			for _, c := range snap.Categories {
				if c.State == "skipped" {
					skipped++
				}
			}
			if skipped == 0 {
				t.Fatalf("第一类 pending 后，剩余类别应标记 skipped；实际 %+v", snap.Categories)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("sweep 未在 15s 内收尾")
}

// TestFileAccessSnapshotReflectsLiveGrants 回归：sweep 结束后「刷新状态」必须
// 能看到实时授权（用户可能刚在系统设置里手动打开了被拒的目录）。此前 Snapshot
// 直接返回 s.cats 快照，刷新按钮是死按钮。
func TestFileAccessSnapshotReflectsLiveGrants(t *testing.T) {
	installFileAccessHooks(t, true, func(string) error { return nil })

	svc := &agentFileAccessService{}
	// 先跑一轮 sweep：全部 granted。
	if _, started := svc.Start(); !started {
		t.Fatal("应当启动")
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && svc.Snapshot().State != "done" {
		time.Sleep(20 * time.Millisecond)
	}
	if svc.Snapshot().State != "done" {
		t.Fatal("sweep 未收尾")
	}

	// 直接把缓存状态改成 denied，模拟「这一轮判了拒绝」。
	svc.mu.Lock()
	for i := range svc.cats {
		if svc.cats[i].Key == "desktop" {
			svc.cats[i].State = string(tccguard.StateDenied)
			svc.cats[i].Detail = "operation not permitted"
		}
	}
	svc.mu.Unlock()

	// grant 缓存里 desktop 仍然有授权 → 刷新必须覆盖成 granted。
	snap := svc.Snapshot()
	var desktop string
	for _, c := range snap.Categories {
		if c.Key == "desktop" {
			desktop = c.State
		}
	}
	if desktop != string(tccguard.StateGranted) {
		t.Fatalf("刷新状态应反映实时 grant（granted），实际 %q —— 刷新按钮成了死按钮", desktop)
	}
}

// TestFileAccessSnapshotHidesCategoriesWhenUnsupported 非 macOS 不应列出 8 个
// 「未知」类别（会误导用户以为系统支持但没授权）。
func TestFileAccessSnapshotHidesCategoriesWhenUnsupported(t *testing.T) {
	restore := tccguard.SetTestHooks(tccguard.Hooks{
		Supported: boolPtr(false),
		Probe:     func(string) error { return nil },
		Home:      func() (string, error) { return t.TempDir(), nil },
		StateDir:  func() (string, error) { return t.TempDir(), nil },
	})
	t.Cleanup(restore)

	svc := &agentFileAccessService{}
	snap := svc.Snapshot()
	if len(snap.Categories) != 0 {
		t.Fatalf("非 macOS 不应列出类别，实际 %d 条", len(snap.Categories))
	}
}

func boolPtr(v bool) *bool { return &v }
