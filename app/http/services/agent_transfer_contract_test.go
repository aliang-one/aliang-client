package services

// 互传协议「四点齐」契约测试（防漏登记先例复发）：常量 + 契约条目 +
// switch 臂 + 门禁，缺一点本文件就会红。file.upload.progress/cancel 的
// 补登记也钉在这里。

import (
	"testing"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

func TestAgentTransferProtocolConstants(t *testing.T) {
	cases := map[string]string{
		models.AgentEventTransferUpload:   "transfer.upload",
		models.AgentEventTransferProgress: "transfer.progress",
		models.AgentEventTransferResult:   "transfer.result",
		models.AgentEventTransferReceive:  "transfer.receive",
		models.AgentEventTransferCancel:   "transfer.cancel",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("constant drift: got %q want %q", got, want)
		}
	}
}

func TestAgentTransferContractEntriesRegistered(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()

	find := func(events []models.AgentProtocolEvent, typ string) *models.AgentProtocolEvent {
		for i := range events {
			if events[i].Type == typ {
				return &events[i]
			}
		}
		return nil
	}
	requireField := func(t *testing.T, ev *models.AgentProtocolEvent, field string) {
		t.Helper()
		for _, f := range ev.Required {
			if f == field {
				return
			}
		}
		t.Fatalf("%s contract missing required field %q (Required=%v)", ev.Type, field, ev.Required)
	}

	// ServerSends：三条下发指令
	up := find(contract.WebSocket.ServerSends, models.AgentEventTransferUpload)
	if up == nil {
		t.Fatal("transfer.upload missing from ServerSends")
	}
	requireField(t, up, "transfer_id")
	requireField(t, up, "source_path") // D2：没有 source_path 发送链是断的
	requireField(t, up, "upload")
	if len(up.Emits) == 0 {
		t.Fatal("transfer.upload must declare Emits")
	}
	rcv := find(contract.WebSocket.ServerSends, models.AgentEventTransferReceive)
	if rcv == nil {
		t.Fatal("transfer.receive missing from ServerSends")
	}
	requireField(t, rcv, "transfer_id")
	requireField(t, rcv, "get_url")
	requireField(t, rcv, "sha256")
	cxl := find(contract.WebSocket.ServerSends, models.AgentEventTransferCancel)
	if cxl == nil {
		t.Fatal("transfer.cancel missing from ServerSends")
	}
	requireField(t, cxl, "transfer_id")

	// ClientSends：进度 + 结果（业务键 transfer_id 是 Required，request_id 是 Optional——D1）
	prg := find(contract.WebSocket.ClientSends, models.AgentEventTransferProgress)
	if prg == nil {
		t.Fatal("transfer.progress missing from ClientSends")
	}
	requireField(t, prg, "transfer_id")
	requireField(t, prg, "direction")
	res := find(contract.WebSocket.ClientSends, models.AgentEventTransferResult)
	if res == nil {
		t.Fatal("transfer.result missing from ClientSends")
	}
	requireField(t, res, "transfer_id")
	requireField(t, res, "ok")

	// 漏登记先例补钉：file.upload.progress / file.upload.cancel 必须在册，
	// 且 progress 必须出现在 file.upload 的 Emits 里（从 Emits 反查的审计
	// 路径也要能找到它，防先例第三次复发）。
	if find(contract.WebSocket.ClientSends, models.AgentEventFileUploadProgress) == nil {
		t.Fatal("file.upload.progress missing from ClientSends (漏登记先例)")
	}
	if find(contract.WebSocket.ServerSends, models.AgentEventFileUploadCancel) == nil {
		t.Fatal("file.upload.cancel missing from ServerSends (漏登记先例)")
	}
	fu := find(contract.WebSocket.ServerSends, models.AgentEventFileUpload)
	if fu == nil {
		t.Fatal("file.upload missing from ServerSends")
	}
	hasProgress := false
	for _, e := range fu.Emits {
		if e == models.AgentEventFileUploadProgress {
			hasProgress = true
		}
	}
	if !hasProgress {
		t.Fatalf("file.upload Emits must include progress, got %v", fu.Emits)
	}
	// receive 的 Optional 必须登记 max_bytes（接收端 too_large 兜底闸的字段）
	if rcv2 := find(contract.WebSocket.ServerSends, models.AgentEventTransferReceive); rcv2 != nil {
		hasMax := false
		for _, f := range rcv2.Optional {
			if f == "max_bytes" {
				hasMax = true
			}
		}
		if !hasMax {
			t.Fatal("transfer.receive Optional must include max_bytes")
		}
	}
}

func TestAgentTransferGate(t *testing.T) {
	cases := []struct {
		msgType string
		want    bool
	}{
		{models.AgentEventTransferUpload, true},
		{models.AgentEventTransferReceive, true},
		{models.AgentEventTransferCancel, false}, // D14：豁免（照 file.upload.cancel 先例）
	}
	for _, tc := range cases {
		if got := remoteAgentMessageRequiresEnabledDevice(tc.msgType); got != tc.want {
			t.Fatalf("remoteAgentMessageRequiresEnabledDevice(%q) = %v, want %v", tc.msgType, got, tc.want)
		}
	}
}

func TestAgentCapabilitiesIncludeFileTransfer(t *testing.T) {
	found := false
	for _, cap := range agentCapabilities() {
		if cap == "file_transfer_v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("agentCapabilities() must advertise file_transfer_v1")
	}
}

// TestAgentTransferDispatchArmsWireUp 验证显式臂确实消费了三类消息：
//   - disabled 设备下 transfer.upload 必须被门禁拦下（恰好一条 error），
//     transfer.cancel 必须穿过门禁且不回 error（默认臂会回 unsupported）。
func TestAgentTransferDispatchArmsWireUp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)

	service := NewAgentService()
	service.mu.Lock()
	service.state.Enabled = false
	service.state.Registered = false
	service.mu.Unlock()

	var written []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			written = append(written, m)
		}
		return nil
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":        models.AgentEventTransferUpload,
		"request_id":  "req-t1",
		"transfer_id": "tr_disabled1",
		"filename":    "a.bin",
		"source_path": "/tmp/a.bin",
		"upload":      map[string]interface{}{"key": "k", "upload_id": "u", "part_size": 1, "parts": []interface{}{}},
	}, writeJSON)
	if len(written) != 1 || written[0]["type"] != models.AgentEventError {
		t.Fatalf("disabled-device transfer.upload must be rejected with exactly one error, got %v", written)
	}

	before := len(written)
	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":        models.AgentEventTransferCancel,
		"transfer_id": "tr_disabled1",
	}, writeJSON)
	for _, m := range written[before:] {
		if m["type"] == models.AgentEventError {
			t.Fatalf("transfer.cancel on a disabled device must not be rejected, got %v", m)
		}
	}
}
