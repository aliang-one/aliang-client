package services

// agent_listener_boot_test.go boot 接线单测(specs/005-event-listener §2/FR-3,
// plan Task 10):全量拉取优先/失败回落本地、云端成功全量覆写(旧行清掉)、
// 「先镜像后 watcher」顺序契约(1b8c36b 教训的服务层钉死)、空镜像 no-op、
// 真 HTTP 拉取实现(header/信封/未知键容忍/未注册拒发)与 base 解析派生。
//
// 缝纪律(同 Task 7/8/9):测试串行,禁 t.Parallel;swap 期间不得有在跑
// watcher/watcher 依赖的 seam;Cleanup 还原顺序经 setupFileTailTest 统一保证。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

// swapListenerCloudFetchAll 换全量拉取缝并登记还原。
func swapListenerCloudFetchAll(t *testing.T, fn func() ([]ListenerMirror, error)) {
	t.Helper()
	old := listenerCloudFetchAll
	listenerCloudFetchAll = fn
	t.Cleanup(func() { listenerCloudFetchAll = old })
}

// 1. 云端不可达(离线/未注册同路径):回落本地文件,boot 不阻塞、镜像不被
// 空表覆写(FR-3 离线只读——回落是读,不是写)。
func TestListenerBootCloudUnreachableFallsBackToLocal(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	swapListenerCloudFetchAll(t, func() ([]ListenerMirror, error) {
		return nil, fmt.Errorf("cloud unreachable (test)")
	})
	seeded := []ListenerMirror{sampleListenerMirror("L-local-1", "2026-11-20T10:00:00Z")}
	if err := saveListenerMirrors(seeded); err != nil {
		t.Fatalf("seed local mirror: %v", err)
	}

	LoadListenerMirror() // 不得 panic、不得阻塞

	got := listListenerMirrors()
	if len(got) != 1 || got[0].ID != "L-local-1" {
		t.Fatalf("回落后须保留本地镜像 1 条 L-local-1: %+v", got)
	}
}

// 2. 云端成功:全量覆写(save 即缓存刷新),本地陈旧行必须消失(server
// 权威——被删的监听器不得靠本地镜像复活)。
func TestListenerBootCloudFetchReplacesMirror(t *testing.T) {
	home := t.TempDir()
	withListenerMirrorHome(t, home)
	// 盘上种一行陈旧行(L-stale),fetch 只回 L-fresh。
	if err := saveListenerMirrors([]ListenerMirror{sampleListenerMirror("L-stale", "2026-11-19T00:00:00Z")}); err != nil {
		t.Fatalf("seed stale mirror: %v", err)
	}
	fresh := []ListenerMirror{sampleListenerMirror("L-fresh", "2026-11-21T00:00:00Z")}
	swapListenerCloudFetchAll(t, func() ([]ListenerMirror, error) { return fresh, nil })

	LoadListenerMirror()

	got := listListenerMirrors()
	if len(got) != 1 || got[0].ID != "L-fresh" {
		t.Fatalf("全量拉取后须只剩 L-fresh(陈旧行清掉): %+v", got)
	}
	// 盘上字节也要是覆写后的形态(下次离线 boot 的回落读到权威快照)——
	// 经 store 路径 helper 直读文件,不绕缓存。
	raw, err := os.ReadFile(listenerMirrorFilePath(t, home))
	if err != nil {
		t.Fatalf("读镜像文件: %v", err)
	}
	if strings.Contains(string(raw), "L-stale") || !strings.Contains(string(raw), "L-fresh") {
		t.Fatalf("盘上镜像须已覆写: %s", raw)
	}
}

// 3. 顺序契约钉死(1b8c36b 教训):Load 在前、Start 在后——按 boot 真实顺序
// 调用,own-device file_tail 行须长出 watcher;Stop 清场收敛。接线侧顺序
// (app/agentruntime/server.go 相邻两行,Load 前 Start 后)由 cmd 侧
// TestListenerWiringAudit 的 exactly-once 共同保证。
func TestListenerBootOrderLoadThenStart(t *testing.T) {
	home, _ := setupFileTailTest(t) // home/devID(up缝)+watcher 清场,Cleanup 统一还原
	targetPath := home + "/build.log"
	fetched := []ListenerMirror{fileTailMirror("L-boot-1", targetPath, "ERROR", "2026-11-21T00:00:00Z")}
	swapListenerCloudFetchAll(t, func() ([]ListenerMirror, error) { return fetched, nil })

	LoadListenerMirror()         // 先:镜像落定
	StartEventListenerWatchers() // 后:按镜像起 watcher

	if ids := fileTailRegistryIDs(); len(ids) != 1 || ids[0] != "L-boot-1" {
		t.Fatalf("boot 顺序调用后须恰有 1 个 L-boot-1 watcher: %v", ids)
	}
	StopEventListenerWatchers()
	if ids := fileTailRegistryIDs(); len(ids) != 0 {
		t.Fatalf("StopEventListenerWatchers 后注册表须清空: %v", ids)
	}
}

// 4. 空镜像:Start no-op 不 panic(新装机/账号无监听器是常态路径)。
func TestListenerBootStartWatchersEmptyMirrorNoop(t *testing.T) {
	setupFileTailTest(t) // 空 home = 空镜像
	StartEventListenerWatchers()
	if ids := fileTailRegistryIDs(); len(ids) != 0 {
		t.Fatalf("空镜像不得起任何 watcher: %v", ids)
	}
}

// 5. 真 HTTP 拉取实现(fetchAllListenerMirrorsFromCloud):header 契约
// (Authorization/X-Aliang-Device-ID 与远端 WS 握手同源)、{code,msg,data}
// 信封(unwrapAgentServerData 口径)、行内未知键容忍、信封 code!=0 报错、
// 裸数组兼容、未注册拒发(不触网)。
func TestListenerCloudFetchAllHTTP(t *testing.T) {
	// user-agent runtime 形态:effectiveUserAuthorizationLocked 走
	// forwardedUserAuthorization 字段而非持久凭据回退,身份材料确定性可断言。
	t.Setenv(AgentRuntimeEnv, "1")

	// 可注册的裸服务:client 必备(fecth 走 svc.client.Do),身份字段经
	// remoteConnectionSnapshot 读(与远端 WS 握手同闸)。
	newSeededService := func(registered bool) *AgentService {
		svc := &AgentService{client: &http.Client{Timeout: agentHTTPTimeout}}
		if registered {
			svc.mu.Lock()
			svc.state.Enabled = true
			svc.state.Registered = true
			svc.state.DeviceID = "dev-boot-http"
			svc.forwardedUserAuthorization = "Bearer boot-http-jwt"
			svc.mu.Unlock()
		}
		return svc
	}
	swapShared := func(t *testing.T, svc *AgentService) {
		t.Helper()
		sharedAgentServiceMu.Lock()
		original := sharedAgentService
		sharedAgentService = svc
		sharedAgentServiceMu.Unlock()
		t.Cleanup(func() {
			sharedAgentServiceMu.Lock()
			sharedAgentService = original
			sharedAgentServiceMu.Unlock()
		})
	}

	var mu sync.Mutex
	var gotAuth, gotDeviceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agent/listeners" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotDeviceID = r.Header.Get("X-Aliang-Device-ID")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"msg":  "ok",
			"data": []map[string]interface{}{
				{
					"id":               "L-http-1",
					"source_type":      "file_tail",
					"source_device_id": "dev-other",
					"status":           "active",
					"updated_at":       "2026-11-21T00:00:00Z",
					"source_params":    map[string]interface{}{"path": "/tmp/x.log", "match": "ERROR"},
					"future_field":     "server 加的未知键,agent 须容忍",
				},
			},
		})
	}))
	defer server.Close()

	oldBase := listenerCloudBaseURL
	listenerCloudBaseURL = func() (string, error) { return server.URL, nil }
	t.Cleanup(func() { listenerCloudBaseURL = oldBase })

	t.Run("envelope rows parsed headers intact", func(t *testing.T) {
		swapShared(t, newSeededService(true))
		rows, err := fetchAllListenerMirrorsFromCloud()
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "L-http-1" || rows[0].SourceDeviceID != "dev-other" {
			t.Fatalf("信封 data 行解析不符: %+v", rows)
		}
		mu.Lock()
		auth, dev := gotAuth, gotDeviceID
		mu.Unlock()
		if auth != "Bearer boot-http-jwt" || dev != "dev-boot-http" {
			t.Fatalf("header 契约破损: Authorization=%q X-Aliang-Device-ID=%q", auth, dev)
		}
	})

	t.Run("envelope nonzero code errors", func(t *testing.T) {
		codeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 401, "msg": "device_unregistered"})
		}))
		defer codeServer.Close()
		saved := listenerCloudBaseURL
		listenerCloudBaseURL = func() (string, error) { return codeServer.URL, nil }
		defer func() { listenerCloudBaseURL = saved }()
		swapShared(t, newSeededService(true))
		if _, err := fetchAllListenerMirrorsFromCloud(); err == nil {
			t.Fatal("信封 code!=0 必须报错(调用方回落本地,不得把业务错误当空表)")
		}
	})

	t.Run("bare array tolerated", func(t *testing.T) {
		bareServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"id":"L-bare","updated_at":"2026-11-21T01:00:00Z"}]`))
		}))
		defer bareServer.Close()
		saved := listenerCloudBaseURL
		listenerCloudBaseURL = func() (string, error) { return bareServer.URL, nil }
		defer func() { listenerCloudBaseURL = saved }()
		swapShared(t, newSeededService(true))
		rows, err := fetchAllListenerMirrorsFromCloud()
		if err != nil {
			t.Fatalf("裸数组(无信封)须容忍: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "L-bare" {
			t.Fatalf("裸数组解析不符: %+v", rows)
		}
	})

	// 生产真实 wire 形态(PhoneServer modules/routes/listeners.ts 设备面实发
	// {listeners:[...]},无 data 键):不识别此形态 = FR-3 boot 全量拉取永远
	// 静默回落本地镜像(Task 11 跨仓漂移发现,本用例钉死)。
	t.Run("device face listeners object shape", func(t *testing.T) {
		faceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"msg":"","listeners":[{"id":"L-face","source_type":"file_tail","updated_at":"2026-11-21T02:00:00Z"}]}`))
		}))
		defer faceServer.Close()
		saved := listenerCloudBaseURL
		listenerCloudBaseURL = func() (string, error) { return faceServer.URL, nil }
		defer func() { listenerCloudBaseURL = saved }()
		swapShared(t, newSeededService(true))
		rows, err := fetchAllListenerMirrorsFromCloud()
		if err != nil {
			t.Fatalf("设备面 {listeners:[...]} 实发形态必须可解析: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "L-face" || rows[0].SourceType != "file_tail" {
			t.Fatalf("设备面形态解析不符: %+v", rows)
		}
	})

	// 未识别对象形态(listeners 键缺席)须报错回落本地——不得当空表覆写镜像
	// (未识别形状 ≠ 空账号;空表覆写会把 server 权威镜像静默清空)。
	t.Run("unrecognized object shape errors not empty", func(t *testing.T) {
		oddServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"items":[]}`))
		}))
		defer oddServer.Close()
		saved := listenerCloudBaseURL
		listenerCloudBaseURL = func() (string, error) { return oddServer.URL, nil }
		defer func() { listenerCloudBaseURL = saved }()
		swapShared(t, newSeededService(true))
		if _, err := fetchAllListenerMirrorsFromCloud(); err == nil {
			t.Fatal("listeners 键缺席的对象形态必须报错(回落本地),不得当空表")
		}
	})

	t.Run("not registered errors without network", func(t *testing.T) {
		swapShared(t, newSeededService(false))
		// base 指向一个「到达即失败」的地址:未注册分支必须在此之前短路
		// (按错误文案断言短路面,而非把它与拨号失败混为一谈)。
		unreachable := "http://127.0.0.1:1"
		saved := listenerCloudBaseURL
		listenerCloudBaseURL = func() (string, error) { return unreachable, nil }
		defer func() { listenerCloudBaseURL = saved }()
		_, err := fetchAllListenerMirrorsFromCloud()
		if err == nil {
			t.Fatal("未注册/无凭据必须报错(boot 回落本地)")
		}
		if !strings.Contains(err.Error(), "agent remote identity unavailable") {
			t.Fatalf("须是身份短路面错误而非拨号失败: %v", err)
		}
	})
}

// 6. base 解析派生:与 currentAgentWebSocketURL 同链同闸(dev 重写/scheme
// 白名单/未配置报错),差异仅 scheme 归一方向与 base path 保留。
func TestListenerCloudBaseURLDerivation(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "http://localhost:5174"}})
	got, err := resolveListenerCloudBaseURL()
	if err != nil {
		t.Fatalf("resolveListenerCloudBaseURL() error = %v", err)
	}
	if got != "http://localhost:4000" {
		t.Fatalf("dev 重写不符(须与 WS URL 派生同款 5174→4000): %q", got)
	}

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "https://api.aliang.one/sub"}})
	if got, _ = resolveListenerCloudBaseURL(); got != "https://api.aliang.one/sub" {
		t.Fatalf("https + base path 须原样保留(与 /api/agent/* HTTP 调用方拼接口径一致): %q", got)
	}

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "wss://mesh.example.com/"}})
	if got, _ = resolveListenerCloudBaseURL(); got != "https://mesh.example.com" {
		t.Fatalf("wss 须归一为 https 且剥尾斜杠: %q", got)
	}

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "ftp://bad.example.com"}})
	if _, err = resolveListenerCloudBaseURL(); err == nil {
		t.Fatal("scheme 白名单外的 base 必须报错")
	}

	config.SetGlobalConfig(nil) // 未配置(与 currentAgentWebSocketURL 同闸)
	if _, err = resolveListenerCloudBaseURL(); err == nil {
		t.Fatal("未配置 agent server 必须报错")
	}
}
