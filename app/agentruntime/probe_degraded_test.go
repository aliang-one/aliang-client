package agentruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/processor/config"
)

// 2026-11-22 对抗评审 must_fix:Status() 锁忙降级(agent_service.go)使 2s 探针
// 恒拿到 code=0 的**陈旧快照**,probeOK=true 不再蕴含实时观测。manager.go:188-191
// 文档化的冻结契约(probeOK=false=观测未知,调用方冻结坏态时钟而非当健康清零)
// 必须对 Degraded=true 恢复——否则 keepalive 长持锁窗口里 companion_watchdog 的
// regRecovery.observe(false) 会按陈旧好态反复清零 15min 坏态阈值,挂起型故障的
// 第二层安全网自我失效。一并覆盖启动后无快照的零值降级分支(不得伪报
// 「显式禁用,确定性不归本巡检管」)。

func withDegradedStatusServer(t *testing.T, status models.AgentStatusResponse) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != capabilityPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": status,
		})
	}))
	t.Cleanup(server.Close)
	originalAddr := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(func() { config.DefaultUserAgentAddr = originalAddr })
}

func TestNeedsRegistrationRecoveryDegradedSnapshotIsUnknown(t *testing.T) {
	// 陈旧好态(锁忙窗口开始前的值):若被当实时观测,probeOK=true 会按好态清零坏态时钟。
	staleGood := models.AgentStatusResponse{Degraded: true, DeviceEnabled: true, Registered: true, RemoteConnected: false}
	withDegradedStatusServer(t, staleGood)

	need, probeOK := NeedsRegistrationRecovery(time.Second)
	if probeOK {
		t.Fatal("Degraded 快照被当成实时观测 probeOK=true:regRecovery 坏态时钟将被陈旧好态清零,第二层安全网失效")
	}
	if need {
		t.Fatal("观测未知(probeOK=false)不得触发 recovery 动作")
	}
}

func TestNeedsRegistrationRecoveryDegradedZeroValueIsUnknown(t *testing.T) {
	// 启动后尚无成功状态采集的零值降级分支:不得伪报「显式禁用,确定性不归本巡检管」。
	zeroDegraded := models.AgentStatusResponse{Degraded: true}
	withDegradedStatusServer(t, zeroDegraded)

	need, probeOK := NeedsRegistrationRecovery(time.Second)
	if probeOK {
		t.Fatal("零值降级被伪报为确定性答复 (need=false,probeOK=true),实为观测未知")
	}
	if need {
		t.Fatal("观测未知不得触发 recovery 动作")
	}
}

func TestNeedsAuthenticatedSyncDegradedSnapshotIsUnknown(t *testing.T) {
	// Degraded 快照即使带粘性禁用白名单状态,也不得驱动 reconcile 决策(观测未知)。
	stale := models.AgentStatusResponse{Degraded: true, SyncStatus: "logout"}
	withDegradedStatusServer(t, stale)

	if NeedsAuthenticatedSync(time.Second) {
		t.Fatal("Degraded 快照被当成实时观测驱动 reconcile:陈旧状态不可作为重启用依据")
	}
}
