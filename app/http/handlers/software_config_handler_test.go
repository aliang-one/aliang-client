package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/http/storage"
)

func TestSoftwareConfigHandler_SaveActivateAndCloudEndpoints(t *testing.T) {
	store, err := storage.NewSoftwareConfigStoreWithDBPath(filepath.Join(t.TempDir(), "handler.db"))
	if err != nil {
		t.Fatalf("create store failed: %v", err)
	}

	service := services.NewSoftwareConfigServiceWithStore(store, nil)
	handler := NewSoftwareConfigHandler(service)

	saveReqBody := map[string]interface{}{
		"uuid":      "h-1",
		"software":  "opencode",
		"name":      "handler-config",
		"file_path": filepath.Join(t.TempDir(), "h1.json"),
		"version":   "1.0.0",
		"in_use":    false,
		"format":    "json",
		"content":   `{"x":1}`,
	}
	saveRaw, _ := json.Marshal(saveReqBody)
	saveReq := httptest.NewRequest(http.MethodPost, "/api/software-config/save", bytes.NewReader(saveRaw))
	saveRec := httptest.NewRecorder()
	handler.HandleSave(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saveRec.Code, saveRec.Body.String())
	}

	activateReqBody := map[string]interface{}{
		"uuid":      "h-2",
		"software":  "claude",
		"name":      "handler-active",
		"file_path": filepath.Join(t.TempDir(), "active.yaml"),
		"version":   "2.0.0",
		"format":    "yaml",
		"content":   "a: 2\n",
	}
	activateRaw, _ := json.Marshal(activateReqBody)
	activateReq := httptest.NewRequest(http.MethodPost, "/api/software-config/activate", bytes.NewReader(activateRaw))
	activateRec := httptest.NewRecorder()
	handler.HandleActivate(activateRec, activateReq)
	if activateRec.Code != http.StatusOK {
		t.Fatalf("activate status=%d body=%s", activateRec.Code, activateRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/software-config/list?software=claude", nil)
	listRec := httptest.NewRecorder()
	handler.HandleList(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}

	pushServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("push expected POST got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer pushServer.Close()

	pushReqBody := map[string]interface{}{
		"cloud_url": pushServer.URL,
	}
	pushRaw, _ := json.Marshal(pushReqBody)
	pushReq := httptest.NewRequest(http.MethodPost, "/api/software-config/cloud/push", bytes.NewReader(pushRaw))
	pushRec := httptest.NewRecorder()
	handler.HandlePushToCloud(pushRec, pushReq)
	if pushRec.Code != http.StatusOK {
		t.Fatalf("push status=%d body=%s", pushRec.Code, pushRec.Body.String())
	}
	if !strings.Contains(pushRec.Body.String(), "synced_count") {
		t.Fatalf("push response should contain synced_count, body=%s", pushRec.Body.String())
	}

	remoteUpdated := time.Now().Add(1 * time.Minute).UTC().Format(time.RFC3339)
	pullServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("pull expected GET got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"configs":[{"uuid":"h-2","name":"handler-active","file_path":"` + activateReqBody["file_path"].(string) + `","version":"2.1.0","in_use":true,"format":"yaml","content":"a: 3\\n","updated_at":"` + remoteUpdated + `"}]}`))
	}))
	defer pullServer.Close()

	pullReqBody := map[string]interface{}{
		"cloud_url": pullServer.URL,
	}
	pullRaw, _ := json.Marshal(pullReqBody)
	pullReq := httptest.NewRequest(http.MethodPost, "/api/software-config/cloud/pull", bytes.NewReader(pullRaw))
	pullRec := httptest.NewRecorder()
	handler.HandlePullFromCloud(pullRec, pullReq)
	if pullRec.Code != http.StatusOK {
		t.Fatalf("pull status=%d body=%s", pullRec.Code, pullRec.Body.String())
	}

	selectReq := httptest.NewRequest(http.MethodPost, "/api/software-config/select", bytes.NewReader([]byte(`{"uuid":"h-2","selected":true}`)))
	selectRec := httptest.NewRecorder()
	handler.HandleSelect(selectRec, selectReq)
	if selectRec.Code != http.StatusOK {
		t.Fatalf("select status=%d body=%s", selectRec.Code, selectRec.Body.String())
	}

	compareReq := httptest.NewRequest(http.MethodPost, "/api/software-config/compare", bytes.NewReader([]byte(`{"cloud_url":"`+pullServer.URL+`"}`)))
	compareRec := httptest.NewRecorder()
	handler.HandleCompareWithCloud(compareRec, compareReq)
	if compareRec.Code != http.StatusOK {
		t.Fatalf("compare status=%d body=%s", compareRec.Code, compareRec.Body.String())
	}

	pushSelectedReq := httptest.NewRequest(http.MethodPost, "/api/software-config/cloud/push-selected", bytes.NewReader(pushRaw))
	pushSelectedRec := httptest.NewRecorder()
	handler.HandlePushSelectedToCloud(pushSelectedRec, pushSelectedReq)
	if pushSelectedRec.Code != http.StatusOK {
		t.Fatalf("push-selected status=%d body=%s", pushSelectedRec.Code, pushSelectedRec.Body.String())
	}

	logReq := httptest.NewRequest(http.MethodPost, "/api/software-config/log", bytes.NewReader([]byte(`{"action":"copy","software":"claude","config_uuid":"h-2","config_name":"handler-active","detail":"copied from ui"}`)))
	logRec := httptest.NewRecorder()
	handler.HandleLogOperation(logRec, logReq)
	if logRec.Code != http.StatusOK {
		t.Fatalf("log status=%d body=%s", logRec.Code, logRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodPost, "/api/software-config/delete", bytes.NewReader([]byte(`{"uuid":"h-1"}`)))
	deleteRec := httptest.NewRecorder()
	handler.HandleDelete(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}
