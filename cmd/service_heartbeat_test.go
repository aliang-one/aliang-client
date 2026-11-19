package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 心跳协议四段:建目录 → 保档旧现场 → 持续前进 → ctx 取消后停写(文件停
// 在最后一次心跳,作为死亡时刻上界)。
func TestRunHeartbeatWritesAdvancesAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.heartbeat")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, path, 50*time.Millisecond)
	}()

	// 等首次落盘
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat file never written")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 至少再跳一次:内容必须前进(UnixNano 单调)
	time.Sleep(120 * time.Millisecond)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	if string(after) == string(before) {
		t.Fatalf("heartbeat not advancing: %s", string(before))
	}

	// 取消后停写:done 关闭 = 循环已退出,此后内容必须冻结
	cancel()
	<-done
	frozen, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heartbeat after cancel: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heartbeat after cancel: %v", err)
	}
	if string(final) != string(frozen) {
		t.Fatalf("heartbeat kept writing after ctx cancel: %s -> %s", string(frozen), string(final))
	}
}

// 取证协议:启动遇旧心跳文件必须先保档为 <path>.prev 再覆写——7034 后的
// 常态是「杀了马上拉起」,不保档则上一份死亡时刻证据被新进程覆掉。
func TestRunHeartbeatPreservesPreviousEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core.heartbeat")
	deadStamp := fmt.Sprintf("%d\n", time.Now().Add(-time.Hour).UnixNano())
	if err := os.WriteFile(path, []byte(deadStamp), 0o644); err != nil {
		t.Fatalf("seed dead heartbeat: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, path, 50*time.Millisecond)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && string(b) != deadStamp {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat never overtook the dead stamp")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	prev, err := os.ReadFile(path + ".prev")
	if err != nil {
		t.Fatalf("previous dead heartbeat must be preserved at .prev: %v", err)
	}
	if string(prev) != deadStamp {
		t.Fatalf(".prev content = %q, want the dead stamp %q", string(prev), deadStamp)
	}
}

// 新装机器口径:目录不存在时心跳自建目录成功落盘,不静默缺证。
func TestRunHeartbeatCreatesMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-yet", "nested", "core.heartbeat")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, path, 50*time.Millisecond)
	}()
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat never created missing directories")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 防御口径:空路径/非法间隔直接返回,不落盘不给调用方添错。
func TestRunHeartbeatNoopGuards(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(context.Background(), "", 20*time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("empty path must return immediately")
	}

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		runHeartbeat(context.Background(), filepath.Join(t.TempDir(), "h"), 0)
	}()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatalf("non-positive interval must return immediately")
	}
}
