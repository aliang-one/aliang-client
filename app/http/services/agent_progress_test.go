package services

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"aliang.one/nursorgate/app/agentcore"
)

func TestAutomaticShellProgressReportsOnlyLongCommands(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := AgentService{state: agentState{RegisteredUser: "test-account"}}
		service.progress.supported.Store(true)
		var reports []map[string]interface{}
		service.setCurrentRemoteWriter(func(payload interface{}) error {
			frame := payload.(map[string]interface{})
			reports = append(reports, frame)
			service.progress.acceptAck(map[string]interface{}{
				"request_id": frame["request_id"], "status": "accepted", "operation_id": "op_shell",
			})
			return nil
		})
		progress := newAgentProgressTool(&service, "session-shell", "run-shell")
		start := progress.shellObserver()

		start("PowerShell", "", nil)(agentcore.ShellOutcome("completed"))
		time.Sleep(time.Second)
		synctest.Wait()
		if len(reports) != 0 {
			t.Fatalf("short command wrote %d progress reports", len(reports))
		}

		finish := start("Bash", "", nil)
		time.Sleep(750 * time.Millisecond)
		synctest.Wait()
		if len(reports) != 1 || reports[0]["action"] != "start" ||
			reports[0]["title"] != "Bash" || reports[0]["session_id"] != "session-shell" ||
			reports[0]["run_id"] != "run-shell" || reports[0]["command"] != nil {
			t.Fatalf("running command progress: %#v", reports)
		}

		finish(agentcore.ShellOutcome("timed_out"))
		synctest.Wait()
		if len(reports) != 2 || reports[1]["action"] != "finish" ||
			reports[1]["operation_id"] != "op_shell" || reports[1]["outcome"] != "failed" ||
			reports[1]["error_code"] != "tool_timeout" {
			t.Fatalf("terminal command progress: %#v", reports)
		}
	})
}

func TestAutomaticShellProgressRestoresFinishAfterLostStartAck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := AgentService{state: agentState{RegisteredUser: "test-account"}}
		service.progress.supported.Store(true)
		var requestIDs []string
		var terminal map[string]interface{}
		acknowledge := false
		service.setCurrentRemoteWriter(func(payload interface{}) error {
			frame := payload.(map[string]interface{})
			if frame["action"] == "start" {
				requestIDs = append(requestIDs, remoteString(frame, "request_id"))
			} else {
				terminal = frame
			}
			if acknowledge {
				service.progress.acceptAck(map[string]interface{}{
					"request_id": frame["request_id"], "status": "accepted", "operation_id": "op_shell",
				})
			}
			return nil
		})

		progress := newAgentProgressTool(&service, "session-shell", "run-shell")
		finish := progress.shellObserver()("Bash", "", nil)
		time.Sleep(time.Second)
		finish(agentcore.ShellCancelled)
		time.Sleep(8 * time.Second)
		synctest.Wait()
		if len(requestIDs) != 2 || requestIDs[0] == "" || requestIDs[0] != requestIDs[1] {
			t.Fatalf("start retries must share the original request: %v", requestIDs)
		}

		acknowledge = true
		service.flushOperationFinishes(context.Background())
		if len(requestIDs) != 3 || requestIDs[2] != requestIDs[0] ||
			terminal["operation_id"] != "op_shell" || terminal["outcome"] != "cancelled" {
			t.Fatalf("registration did not recover the original task and finish it: starts=%v finish=%v", requestIDs, terminal)
		}
	})
}

func TestProgressToolDropsEventsFromPreviousAccount(t *testing.T) {
	service := AgentService{state: agentState{RegisteredUser: "account-b"}}
	service.handleRemoteAgentMessage(context.WithValue(context.Background(), operationConnectionAccount{}, "account-a"),
		map[string]interface{}{"type": "operation.updated", "operation": map[string]interface{}{
			"operation_id": "old", "revision": 1,
		}}, nil)
	service.handleRemoteAgentMessage(context.WithValue(context.Background(), operationConnectionAccount{}, "account-b"),
		map[string]interface{}{"type": "operation.updated", "operation": map[string]interface{}{
			"operation_id": "current", "revision": 1,
		}}, nil)
	if service.operations.account != "account-b" || len(service.operations.rows) != 1 || service.operations.rows["current"].Revision != 1 {
		t.Fatalf("unexpected account mirror: %q %#v", service.operations.account, service.operations.rows)
	}
}

func TestProgressToolBindsOperationToRun(t *testing.T) {
	service := AgentService{state: agentState{RegisteredUser: "test-account"}}
	service.progress.supported.Store(true)
	sends := 0
	service.setCurrentRemoteWriter(func(payload interface{}) error {
		frame := payload.(map[string]interface{})
		sends++
		service.progress.acceptAck(map[string]interface{}{
			"request_id": frame["request_id"], "status": "accepted",
			"operation_id": "op_1",
		})
		return nil
	})
	tool := newAgentProgressTool(&service, "local-conversation", "run-1")
	ctx := context.Background()
	if result := tool.Execute(ctx, map[string]interface{}{"action": "start", "title": "Build"}, nil); result.IsError {
		t.Fatal(result)
	}
	if result := tool.Execute(ctx, map[string]interface{}{"action": "update", "operation_id": "op_other"}, nil); !result.IsError {
		t.Fatal("accepted another run's operation")
	}
	if result := tool.Execute(ctx, map[string]interface{}{"action": "finish", "operation_id": "op_1", "outcome": []string{"completed"}}, nil); !result.IsError {
		t.Fatal("accepted malformed outcome")
	}
	if result := tool.Execute(ctx, map[string]interface{}{"action": "finish", "operation_id": "op_1", "outcome": "completed"}, nil); result.IsError {
		t.Fatal(result)
	}
	if result := tool.Execute(ctx, map[string]interface{}{"action": "update", "operation_id": "op_1"}, nil); !result.IsError {
		t.Fatal("accepted an update after finish")
	}
	if sends != 2 {
		t.Fatalf("sent %d reports, want 2", sends)
	}
	if _, err := json.Marshal(tool.InputSchema()); err != nil {
		t.Fatal(err)
	}
}

func TestProgressToolViaClaudeMCP(t *testing.T) {
	service := AgentService{state: agentState{RegisteredUser: "test-account"}}
	service.progress.supported.Store(true)
	var frames []map[string]interface{}
	service.setCurrentRemoteWriter(func(payload interface{}) error {
		frame := payload.(map[string]interface{})
		frames = append(frames, frame)
		service.progress.acceptAck(map[string]interface{}{
			"request_id": frame["request_id"], "status": "accepted", "operation_id": "op_claude",
		})
		return nil
	})
	_, bridge := newToolAskTestManager(t, time.Second)
	token := bridge.registerRun("local-claude", "run-claude", nil)
	bridge.runs[token].progress = newAgentProgressTool(&service, "local-claude", "run-claude")
	defer bridge.unregisterRun(token)

	server := httptest.NewServer(bridge.tokenCheckedHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp/aliang/" + token}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, input := range []map[string]interface{}{
		{"action": "start", "title": "Build assets"},
		{"action": "update", "operation_id": "op_claude", "current": 3, "total": 8, "unit": "files"},
		{"action": "finish", "operation_id": "op_claude", "outcome": "completed"},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "report_progress", Arguments: input})
		if err != nil || result.IsError || !strings.Contains(toolAskResultText(t, result), "op_claude") {
			t.Fatalf("call %v: result=%v error=%v", input["action"], result, err)
		}
	}
	if len(frames) != 3 || frames[1]["session_id"] != "local-claude" || frames[1]["run_id"] != "run-claude" {
		t.Fatalf("unexpected cloud frames: %#v", frames)
	}
}

func TestShellProgressReusesExplicitCardAndReportsOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := AgentService{state: agentState{RegisteredUser: "test-account"}}
		service.progress.supported.Store(true)
		var reports []map[string]interface{}
		service.setCurrentRemoteWriter(func(payload interface{}) error {
			frame := payload.(map[string]interface{})
			reports = append(reports, frame)
			service.progress.acceptAck(map[string]interface{}{
				"request_id": frame["request_id"], "status": "accepted", "operation_id": "op_shell",
			})
			return nil
		})
		progress := newAgentProgressTool(&service, "session-shell", "run-shell")
		result := progress.Execute(t.Context(), map[string]interface{}{
			"action": "start", "kind": "shell", "title": "Run tests",
		}, nil)
		if result.IsError {
			t.Fatal(result)
		}
		var outputBytes atomic.Int64
		finish := progress.shellObserver()("Bash", "op_shell", outputBytes.Load)
		synctest.Wait()
		outputBytes.Store(321)
		time.Sleep(time.Second)
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		outputBytes.Store(654)
		time.Sleep(time.Second)
		synctest.Wait()
		finish(agentcore.ShellCancelled)
		progress.close()
		synctest.Wait()
		if len(reports) != 4 || reports[0]["action"] != "start" ||
			reports[1]["operation_id"] != "op_shell" || reports[1]["action"] != "update" ||
			reports[1]["current"] != int64(321) || reports[1]["unit"] != "bytes" ||
			reports[1]["total"] != nil || reports[1]["command"] != nil ||
			reports[2]["operation_id"] != "op_shell" || reports[2]["current"] != int64(654) ||
			reports[3]["operation_id"] != "op_shell" || reports[3]["outcome"] != "cancelled" {
			t.Fatalf("expected one start, two counts and one actual cancellation: %#v", reports)
		}
	})
}

func TestShellProgressCannotClaimAnotherRunOrUnrelatedTask(t *testing.T) {
	s := &AgentService{}
	progress := newAgentProgressTool(s, "session", "run")
	progress.active["op_download"] = progressTask{kind: "download"}
	for _, id := range []string{"op_from_other_run", "op_download"} {
		if _, ok := progress.claimShellOperation(id); ok {
			t.Fatalf("Shell must not take ownership of %s", id)
		}
	}
}

func TestShellProgressStageVocabularyIsBoundedAndPrivate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := AgentService{state: agentState{RegisteredUser: "test-account"}}
		service.progress.supported.Store(true)
		var updates []map[string]interface{}
		service.setCurrentRemoteWriter(func(payload interface{}) error {
			frame := payload.(map[string]interface{})
			if frame["action"] == "update" {
				updates = append(updates, frame)
			}
			service.progress.acceptAck(map[string]interface{}{
				"request_id": frame["request_id"], "status": "accepted", "operation_id": "op_shell",
			})
			return nil
		})
		progress := newAgentProgressTool(&service, "session-shell", "run-shell")
		var outputBytes atomic.Int64
		finish := progress.shellObserver()("Bash", "", outputBytes.Load)
		// Wait past the start delay so the byte observer is running.
		time.Sleep(time.Second)
		outputBytes.Store(100)
		time.Sleep(time.Second)
		synctest.Wait()
		// Three quiet ticks demote the card to waiting exactly once.
		time.Sleep(3 * time.Second)
		synctest.Wait()
		// Staying quiet must not keep ticking the cloud.
		time.Sleep(2 * time.Second)
		synctest.Wait()
		outputBytes.Store(200)
		time.Sleep(time.Second)
		synctest.Wait()
		finish(agentcore.ShellCompleted)
		progress.close()
		synctest.Wait()

		if len(updates) != 3 {
			t.Fatalf("want streaming, waiting, resumed-streaming updates: %#v", updates)
		}
		allowed := map[string]bool{
			"type": true, "session_id": true, "run_id": true, "agent_instance_id": true,
			"request_id": true, "source_sequence": true, "action": true,
			"operation_id": true, "current": true, "unit": true, "phase": true, "step": true,
		}
		want := []struct {
			phase, step string
			current     int64
		}{
			{"running", "receiving command output", 100},
			{"waiting", "waiting for command output", 100},
			{"running", "receiving command output", 200},
		}
		for i, frame := range updates {
			for key := range frame {
				if !allowed[key] {
					t.Fatalf("update %d carried unexpected field %q", i, key)
				}
			}
			if frame["phase"] != want[i].phase || frame["step"] != want[i].step ||
				frame["current"] != want[i].current || frame["unit"] != "bytes" {
				t.Fatalf("update %d = %#v, want %+v", i, frame, want[i])
			}
		}
	})
}

func TestAgentCoreSubagentBridgesCarryShellObserver(t *testing.T) {
	// SYNC-16 首片接线（2026-11-27）：子 run 复用主 run 传入的同一份 ToolEnv
	// ——子 agent 里的长命令（构建/测试/下载）此前对操作进度通道完全静默。
	setTestAgentHome(t)
	writeSubagentDef(t, os.Getenv("HOME"), "researcher.md",
		"---\nname: researcher\ndescription: 调研任务\n---\n你是调研员。")

	env := &agentcore.ToolEnv{ProjectPath: t.TempDir()}
	env.OnShellStart = func(tool string, progressOperationID string, outputBytes func() int64) func(agentcore.ShellOutcome) {
		return func(agentcore.ShellOutcome) {}
	}
	m := newAgentAIManager()
	tools := m.agentCoreSubagentTools(agentAIRun{sessionID: "s-obs", projectPath: t.TempDir()},
		agentcore.NewClient(agentCoreBaseURL()), env, nil, nil, nil, nil, nil)
	if len(tools) != 1 || tools[0].Name() != "task" {
		t.Fatalf("应为唯一 task 工具, got %+v", tools)
	}
	router, ok := tools[0].(*agentCoreSubagentTaskRouter)
	if !ok {
		t.Fatalf("tools[0] 不是 task router: %T", tools[0])
	}
	if len(router.bridges) == 0 {
		t.Fatal("无桥可验证")
	}
	for name, b := range router.bridges {
		if b.env != env || b.env.OnShellStart == nil {
			t.Fatalf("桥 %q 未携带主 run 的 shell 观察者", name)
		}
	}
}
