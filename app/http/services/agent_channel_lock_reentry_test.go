package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// 2026-11-22 生产死锁回归(本机 MacBookPro dev-3920d1ae 实锤):user agent 的
// register 同步路径(SyncNowWithUserContext)持 s.mu 走 callAgentServer,云端
// 401(凭据被吊销未过期、本地续期未轮转)落入通道闸门的 owner 通知分支,
// NotifyOwnerAuthRejectedForChannel → currentAgentDeviceIDForChannelNotify
// 对已持有的非重入 s.mu 二次加锁 → 同 goroutine 永久 park、整把锁焊死:
// /api/agent/status|sync|disable 全部无限挂死,goroutine 以 +12/min 线性
// 泄漏(56433 侧 1148 条墓碑 socket 坐实)。本文件钉死两件事:
// ① 持锁调用 401 闸门必须返回(不得再入锁);
// ② 通知链在锁下必须退化读快照,且 device_id 语义不因 TryLock 降级而丢失
//    (owner 对空 device_id 退化 generation+时间窗校验,快照保真优先)。

// TestCallAgentServerChannel401NotifyDoesNotReenterLockWhileHeld 复刻生产
// 死锁:调用方持 s.mu 的前提下走完整 401 闸门,闸门必须在看门狗窗口内返回,
// 且 owner 恰好收到一次带 device_id 的通知。
func TestCallAgentServerChannel401NotifyDoesNotReenterLockWhileHeld(t *testing.T) {
	newChannelNotifyTestEnv(t)

	var mu sync.Mutex
	var reasons []string
	var deviceIDs []string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/agent-auth-rejected" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		reasons = append(reasons, body["reason"].(string))
		if id, ok := body["device_id"].(string); ok {
			deviceIDs = append(deviceIDs, id)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"applied": true, "ignored": ""},
		})
	}))
	defer owner.Close()
	t.Setenv(SessionOwnerAddrEnv, owner.URL)

	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	oldRefresher := agentChannelRefreshHeader
	// 本地续期返回同头 = 「吊销但未过期」场景(生产实锤分支)。
	agentChannelRefreshHeader = func() (string, error) { return "Bearer stale", nil }
	t.Cleanup(func() {
		agentChannelRefreshHeader = oldRefresher
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	svc := &AgentService{client: srv.Client()}
	svc.state.DeviceID = "dev-locktest"
	// 预热快照:任一次无锁读都会刷新 device_id 快照(生产中 Status/SyncNow
	// 的 ensureDeviceIdentityLocked 早已跑过,快照天然非空)。
	if got := svc.currentAgentDeviceIDForChannelNotify(); got != "dev-locktest" {
		t.Fatalf("out-of-lock device id read = %q, want dev-locktest", got)
	}

	// 复刻 register 持锁路径:调用方先持锁,再走 401 闸门。
	svc.mu.Lock()
	t.Cleanup(svc.mu.Unlock)

	done := make(chan struct{}, 1)
	go func() {
		_, _ = svc.callAgentServerWithAuthorization(http.MethodGet, srv.URL, nil, "Bearer stale")
		done <- struct{}{}
	}()

	select {
	case <-done:
		// 闸门正常返回。
	case <-time.After(3 * time.Second):
		t.Fatal("401 闸门在调用方持锁时 3s 未返回:非重入锁二次加锁自死锁复现" +
			"(生产事故 2026-11-22:currentAgentDeviceIDForChannelNotify 再入 s.mu)")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 {
		t.Fatalf("owner received %d notifications, want 1 (reasons=%v)", len(reasons), reasons)
	}
	if reasons[0] != agentAuthRejectedReasonChannel {
		t.Fatalf("reason = %v, want %v", reasons[0], agentAuthRejectedReasonChannel)
	}
	if len(deviceIDs) != 1 || deviceIDs[0] != "dev-locktest" {
		t.Fatalf("device_id in notify = %v, want [dev-locktest](快照保真,不得因锁下退化读而丢失)", deviceIDs)
	}
}

// TestCurrentAgentDeviceIDForChannelNotifyTryLockUnderLock 单元级:锁被占用
// 时设备号读取必须立即返回快照值,不得阻塞,更不得永久 park。
func TestCurrentAgentDeviceIDForChannelNotifyTryLockUnderLock(t *testing.T) {
	svc := &AgentService{}
	svc.state.DeviceID = "dev-helper"
	// 预热快照。
	if got := svc.currentAgentDeviceIDForChannelNotify(); got != "dev-helper" {
		t.Fatalf("out-of-lock read = %q, want dev-helper", got)
	}

	svc.mu.Lock()

	done := make(chan string, 1)
	go func() { done <- svc.currentAgentDeviceIDForChannelNotify() }()

	select {
	case got := <-done:
		if got != "dev-helper" {
			t.Fatalf("under-lock read = %q, want snapshot dev-helper", got)
		}
	case <-time.After(3 * time.Second):
		svc.mu.Unlock()
		t.Fatal("currentAgentDeviceIDForChannelNotify 在锁被占用时 3s 未返回:再入锁死锁")
	}

	// 解锁后再读,应返回活值(且快照保持一致)。
	svc.mu.Unlock()
	if got := svc.currentAgentDeviceIDForChannelNotify(); got != "dev-helper" {
		t.Fatalf("post-unlock read = %q, want dev-helper", got)
	}
}
