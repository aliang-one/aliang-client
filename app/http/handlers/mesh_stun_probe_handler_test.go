package handlers

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/app/mesh/punch"
)

// ---------------------------------------------------------------------------
// 组网诊断显式活探测批 Task 7:POST /api/mesh/stun_probe 端点测试。
// 全部网络缝注入(newConn/probe/servers),零真网络:真探测语义(预算截断/
// 判实即停/判型结论)由 punch 包 TestClassifyWithBudget* 自测钉,此处钉的是
// handler 骨架四态+铁律(405/busy 不排队/正常传参与透传/截断语境输出/
// socket 独立性恰一次 Close/绑定失败 500 不静默回落)。
// ---------------------------------------------------------------------------

// fakeProbeConn 可关断的 net.PacketConn,记录 Close 次数:独立性断言
// 「handler 恰一次 Close,不双关也不漏关」全靠它。探测阶段 handler 不经它
// 收发任何包(probe 缝为注入假实现)。
type fakeProbeConn struct {
	closeCount int
}

func (c *fakeProbeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	return 0, nil, errors.New("fake conn 不收包")
}
func (c *fakeProbeConn) WriteTo(p []byte, addr net.Addr) (int, error) { return len(p), nil }
func (c *fakeProbeConn) Close() error                                 { c.closeCount++; return nil }
func (c *fakeProbeConn) LocalAddr() net.Addr                          { return nil }
func (c *fakeProbeConn) SetDeadline(t time.Time) error                { return nil }
func (c *fakeProbeConn) SetReadDeadline(t time.Time) error            { return nil }
func (c *fakeProbeConn) SetWriteDeadline(t time.Time) error           { return nil }

// meshStunProbeRequest 请求 helper:返回 (code, 整包 envelope);非 200 时
// envelope 为 nil(405/500 用例直断 code)。
func meshStunProbeRequest(t *testing.T, h *MeshStunProbeHandler, method string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/mesh/stun_probe", nil)
	rec := httptest.NewRecorder()
	h.HandleStunProbe(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	return rec.Code, envelope
}

// TestMeshStunProbeBusy busy 态(agentPeerExecTest 同款抢锁手法):直接占住
// 包级锁模拟另一探测在跑,第二条必须立即回 200 信封 busy=true,不排队、
// 不新建 socket、不探测。
func TestMeshStunProbeBusy(t *testing.T) {
	meshStunProbeMu.Lock()
	defer meshStunProbeMu.Unlock()

	h := &MeshStunProbeHandler{
		newConn: func() (net.PacketConn, error) {
			t.Error("busy 态不得新建 socket")
			return nil, nil
		},
		probe: func(net.PacketConn, []string, time.Duration) punch.ClassifyResult {
			t.Error("busy 态不得探测")
			return punch.ClassifyResult{}
		},
	}
	started := time.Now()
	code, envelope := meshStunProbeRequest(t, h, http.MethodPost)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy reply took %v, must be immediate", elapsed)
	}
	if code != 200 {
		t.Fatalf("busy 应 200 信封(结构化返回),得到 %d", code)
	}
	data := envelope["data"].(map[string]any)
	if data["busy"] != true {
		t.Fatalf("busy = %v, want true", data["busy"])
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatalf("busy 态应带人话 note: %v", data)
	}
}

// TestMeshStunProbeNormal 正常态:三缝注入假实现,断言传参(conn 同一实例/
// servers 原样/budget=8s 总预算)与 results/truncated/note 透传。
func TestMeshStunProbeNormal(t *testing.T) {
	fake := &fakeProbeConn{}
	var (
		gotConn    net.PacketConn
		gotServers []string
		gotBudget  time.Duration
	)
	h := &MeshStunProbeHandler{
		servers: func() []string { return []string{"stun.a.test:3478", "stun.b.test:3478"} },
		newConn: func() (net.PacketConn, error) { return fake, nil },
		probe: func(conn net.PacketConn, servers []string, budget time.Duration) punch.ClassifyResult {
			gotConn, gotServers, gotBudget = conn, servers, budget
			return punch.ClassifyResult{
				Results: []punch.StunProbeEntry{
					{Server: "stun.a.test:3478", OK: true, MappedPort: 41000, NatType: punch.NatTypeNonSymmetric},
					{Server: "stun.b.test:3478", OK: true, MappedPort: 41000, NatType: punch.NatTypeNonSymmetric},
				},
			}
		},
	}
	code, envelope := meshStunProbeRequest(t, h, http.MethodPost)
	if code != 200 {
		t.Fatalf("POST code = %d, want 200", code)
	}

	// 传参断言:conn 是 newConn 产物同一实例、servers 原样、budget=总预算。
	if gotConn == nil {
		t.Fatal("probeFunc 收到的 conn 为 nil")
	}
	if gotConn != net.PacketConn(fake) {
		t.Fatal("probeFunc 应收到 newConn 产物同一实例")
	}
	if gotBudget != meshStunProbeBudget {
		t.Fatalf("budget=%v, want %v", gotBudget, meshStunProbeBudget)
	}
	if len(gotServers) != 2 || gotServers[0] != "stun.a.test:3478" || gotServers[1] != "stun.b.test:3478" {
		t.Fatalf("servers 传参不符: %v", gotServers)
	}

	// 透传断言:results 逐台原样(五键齐全)、truncated=false、elapsed_ms 在
	// (键名 elapsed_ms 非 budget_ms:装的是实耗时非预算上限,I-1 评审拍定)。
	data := envelope["data"].(map[string]any)
	results, ok := data["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %#v, want 2 条数组", data["results"])
	}
	first := results[0].(map[string]any)
	if first["server"] != "stun.a.test:3478" || first["ok"] != true ||
		first["mapped_port"].(float64) != 41000 || first["nat_type"] != punch.NatTypeNonSymmetric ||
		first["err"] != "" {
		t.Fatalf("results[0] 透传不符: %v", first)
	}
	if data["truncated"] != false {
		t.Fatalf("truncated = %v, want false", data["truncated"])
	}
	if ms, ok := data["elapsed_ms"].(float64); !ok || ms < 0 {
		t.Fatalf("elapsed_ms = %v, want 非负数", data["elapsed_ms"])
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatalf("正常态也应带默认档语境 note: %v", data)
	}
	// socket 独立性:handler 侧恰一次 Close(不双关)。
	if fake.closeCount != 1 {
		t.Fatalf("socket Close 次数=%d, want 恰 1", fake.closeCount)
	}
}

// TestMeshStunProbeTruncated 截断态语境输出:budget 1ns+4 台的真实截断机制
// 在 punch 包钉(TestClassifyWithBudgetImmediateExpiry/MidwayExpiry),此处钉
// handler 的截断语境透传——probeFunc 报 truncated+已测台数,响应必须如实
// (truncated=true,results 只含已测台,note 带默认档语境)。
func TestMeshStunProbeTruncated(t *testing.T) {
	fake := &fakeProbeConn{}
	h := &MeshStunProbeHandler{
		servers: func() []string { return []string{"s1", "s2", "s3", "s4"} },
		newConn: func() (net.PacketConn, error) { return fake, nil },
		probe: func(net.PacketConn, []string, time.Duration) punch.ClassifyResult {
			return punch.ClassifyResult{
				Results:   []punch.StunProbeEntry{{Server: "s1", Err: "stun discover: s1 在 100ms 内无有效应答"}},
				Truncated: true,
			}
		},
	}
	code, envelope := meshStunProbeRequest(t, h, http.MethodPost)
	if code != 200 {
		t.Fatalf("POST code = %d, want 200", code)
	}
	data := envelope["data"].(map[string]any)
	results, ok := data["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("已测台数应如实(1 台),results = %#v", data["results"])
	}
	if got := results[0].(map[string]any)["server"]; got != "s1" {
		t.Fatalf("已测台 server = %v, want s1", got)
	}
	if data["truncated"] != true {
		t.Fatalf("truncated = %v, want true(不假装完整)", data["truncated"])
	}
	if note, _ := data["note"].(string); note == "" {
		t.Fatalf("截断态应带默认档语境 note: %v", data)
	}
	if fake.closeCount != 1 {
		t.Fatalf("截断路径 socket Close 次数=%d, want 恰 1", fake.closeCount)
	}
}

// TestMeshStunProbeConnIndependence 独立性:probeFunc 收到的 conn 非 nil 且
// 是 newConn 产物同一实例;handler 侧用后恰一次 Close——用可关断的 fake conn
// 记录 Close 次数=1(双关/漏关都算错)。
func TestMeshStunProbeConnIndependence(t *testing.T) {
	fake := &fakeProbeConn{}
	var gotConn net.PacketConn
	h := &MeshStunProbeHandler{
		servers: func() []string { return []string{"stun.x.test:3478"} },
		newConn: func() (net.PacketConn, error) { return fake, nil },
		probe: func(conn net.PacketConn, _ []string, _ time.Duration) punch.ClassifyResult {
			gotConn = conn
			return punch.ClassifyResult{Results: []punch.StunProbeEntry{}}
		},
	}
	code, envelope := meshStunProbeRequest(t, h, http.MethodPost)
	if code != 200 {
		t.Fatalf("POST code = %d, want 200", code)
	}
	if gotConn == nil {
		t.Fatal("probeFunc 收到的 conn 为 nil(独立性断言:必须传入新建 socket)")
	}
	if gotConn != net.PacketConn(fake) {
		t.Fatal("应传 newConn 产物同一实例,不得另建")
	}
	if fake.closeCount != 1 {
		t.Fatalf("handler 侧应恰一次 Close,得到 %d 次(双关/漏关都算错)", fake.closeCount)
	}
	// 空结果也是恒数组(一台未测=results [],不 null)。
	results, ok := envelope["data"].(map[string]any)["results"].([]any)
	if !ok || len(results) != 0 {
		t.Fatalf("results = %#v, want 空数组非 null", envelope["data"].(map[string]any)["results"])
	}
}

// TestMeshStunProbeMethodNotAllowed 非 POST 405(HandleDiag 同款 http.Error
// 惯例——common.Error 收业务码会把 405 映射成 500)。
func TestMeshStunProbeMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/mesh/stun_probe", nil)
	rec := httptest.NewRecorder()
	(&MeshStunProbeHandler{}).HandleStunProbe(rec, req)
	if rec.Code != 405 {
		t.Fatalf("GET code = %d, want 405", rec.Code)
	}
}

// TestMeshStunProbeBindFailure500 绑定铁律:NewBoundUDPConn 失败=500 上抛,
// 绝不静默回落未绑定 socket 探测(回落学错映射)。
func TestMeshStunProbeBindFailure500(t *testing.T) {
	h := &MeshStunProbeHandler{
		servers: func() []string { return []string{"stun.x.test:3478"} },
		newConn: func() (net.PacketConn, error) {
			return nil, errors.New("punch socket: bind eth0: boom")
		},
		probe: func(net.PacketConn, []string, time.Duration) punch.ClassifyResult {
			t.Error("绑定失败绝不探测(不得静默回落未绑定 socket)")
			return punch.ClassifyResult{}
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/stun_probe", nil)
	rec := httptest.NewRecorder()
	h.HandleStunProbe(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("绑定失败 code = %d, want 500", rec.Code)
	}
}

// TestMeshStunProbeUnassembled500 装配守卫:零值 handler(缺缝接线)=fail
// loud 500,不假绿空答。
func TestMeshStunProbeUnassembled500(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/mesh/stun_probe", nil)
	rec := httptest.NewRecorder()
	(&MeshStunProbeHandler{}).HandleStunProbe(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("未装配 code = %d, want 500", rec.Code)
	}
}
