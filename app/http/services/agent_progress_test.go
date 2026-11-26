package services

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
