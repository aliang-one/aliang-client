package services

import (
	"os"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func mustAgentTestCWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// 端到端回归（2026-10-22「显示输出啥也看不到」排查）：既有 scrollback 测试
// 全部直接注入 ring，没有覆盖真实链路 create() → startAgentTerminalProcess
// → copyTerminalOutput 泵 → ring → scrollbackSnapshot。本测试用真实 shell
// 进程走完这条链：shell 回显的输出必须在一秒量级内出现在 scrollback 里
// （CWD 解析的授权目录冷扫描已被启动预热挪走，见
// WarmAgentAuthorizedExecutionDirectories）。
func TestAgentTerminalScrollbackThroughRealPump(t *testing.T) {
	started := time.Now()
	// 模拟启动预热已完成（WarmAgentAuthorizedExecutionDirectories 的产物）：
	// create 路径的 CWD 解析必须直接命中缓存，绝不能再串上全量转录扫描。
	setAgentAuthorizedExecutionDirectoriesCache([]string{mustAgentTestCWD(t)})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })
	m := newAgentTerminalManager()

	created := make(chan struct{})
	wrapper := func(payload interface{}) error {
		if frame, ok := payload.(map[string]interface{}); ok && frame["type"] == models.AgentEventTerminalCreated {
			select {
			case <-created:
			default:
				close(created)
			}
		}
		return nil
	}

	m.create(map[string]interface{}{
		"session_id": "repro-pump",
		"shell":      "bash",
		"cwd":        ".",
		"rows":       24,
		"cols":       80,
	}, wrapper)

	select {
	case <-created:
	case <-time.After(10 * time.Second):
		t.Fatalf("terminal.created not observed")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("terminal.created took %v — CWD 解析又串上了全量扫描", elapsed)
	}

	// 写命令并等回显进 ring：输入走 phone 同款 write 通道。
	m.write(map[string]interface{}{
		"session_id": "repro-pump",
		"data":       "echo hello-scrollback-repro\n",
	}, wrapper)

	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, ok := m.scrollbackSnapshot("repro-pump", 0)
		if !ok {
			t.Fatalf("scrollbackSnapshot lost a live session")
		}
		if strings.Contains(snap.Data, "hello-scrollback-repro") {
			t.Logf("ring fed after %v (len=%d)", time.Since(started), len(snap.Data))
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pump never fed the ring; scrollback=%q", snap.Data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
