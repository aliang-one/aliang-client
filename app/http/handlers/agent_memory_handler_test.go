package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
)

// swapAgentCoreMemoryDirForHandlerTest 经导出门面把包级 store 目录换到
// t.TempDir:handlers 包拿不到 services 未导出 seam,直打真 agentHome() 会
// 污染本机 ~/.aliang/memory(CRUD 不产生在途 goroutine,直接 restore 即可)。
func swapAgentCoreMemoryDirForHandlerTest(t *testing.T) {
	t.Helper()
	t.Cleanup(services.AgentCoreMemory.SetDirForTest(t.TempDir()))
}

// newAgentMemoryTestMux 按 core routes.go 六行注册逐字复刻方法前缀形态;
// PUT/DELETE 单条靠 PathValue 取 id,必须过 mux 匹配才能拿到路径参数。
func newAgentMemoryTestMux() *http.ServeMux {
	h := NewAgentMemoryHandler()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/memory", h.HandleMemoryList)
	mux.HandleFunc("DELETE /api/agent/memory", h.HandleMemoryDelete)
	mux.HandleFunc("POST /api/agent/memory", h.HandleMemoryPost)
	mux.HandleFunc("PUT /api/agent/memory/{id}", h.HandleMemoryPut)
	mux.HandleFunc("DELETE /api/agent/memory/{id}", h.HandleMemoryDelete)
	mux.HandleFunc("GET /api/agent/memory/projects", h.HandleMemoryProjects)
	return mux
}

func doAgentMemoryReq(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// agentMemoryEnvelope common.Success/Error 信封形态;Data 一并携带 ok/memories/
// projects/entry 四态字段,单结构通解(未出现的字段保持零值)。
type agentMemoryEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		OK       bool `json:"ok"`
		Memories []struct {
			ID        int    `json:"id"`
			Content   string `json:"content"`
			Source    string `json:"source"`
			LastHitAt string `json:"last_hit_at"`
		} `json:"memories"`
		Projects []struct {
			Project string `json:"project"`
			Count   int    `json:"count"`
		} `json:"projects"`
		Entry struct {
			ID        int    `json:"id"`
			Content   string `json:"content"`
			Source    string `json:"source"`
			LastHitAt string `json:"last_hit_at"`
		} `json:"entry"`
	} `json:"data"`
}

func decodeAgentMemoryEnvelope(t *testing.T, rec *httptest.ResponseRecorder) agentMemoryEnvelope {
	t.Helper()
	var env agentMemoryEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("response is not valid JSON: %v; body=%s", err, rec.Body.String())
	}
	return env
}

const agentMemoryTestProject = "/tmp/agentcore-memory-proj"

// TestAgentMemoryHandler 六端点行为面:400 关卡/POST+GET 往返/PUT 改(保留
// source、不刷 last_hit_at)/DELETE 单条与清空/不存在 404/超上限 400/projects
// 清单。目录经导出 seam 换 TempDir。
func TestAgentMemoryHandler(t *testing.T) {
	swapAgentCoreMemoryDirForHandlerTest(t)
	mux := newAgentMemoryTestMux()

	t.Run("list_without_project_400", func(t *testing.T) {
		rec := doAgentMemoryReq(t, mux, http.MethodGet, "/api/agent/memory?project=", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if env := decodeAgentMemoryEnvelope(t, rec); env.Msg != "project 必填" {
			t.Fatalf("msg = %q, want project 必填", env.Msg)
		}
	})

	t.Run("post_then_get_roundtrip", func(t *testing.T) {
		rec := doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
			`{"project":"`+agentMemoryTestProject+`","content":"事实一"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("post code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		posted := decodeAgentMemoryEnvelope(t, rec)
		if posted.Data.Entry.ID <= 0 || posted.Data.Entry.Source != "manual" {
			t.Fatalf("entry = %+v, want id>0 且 source=manual", posted.Data.Entry)
		}
		rec = doAgentMemoryReq(t, mux, http.MethodGet,
			"/api/agent/memory?project="+agentMemoryTestProject, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("get code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		env := decodeAgentMemoryEnvelope(t, rec)
		if len(env.Data.Memories) != 1 || env.Data.Memories[0].Content != "事实一" || env.Data.Memories[0].Source != "manual" {
			t.Fatalf("memories = %+v, want 1 条 manual 事实一", env.Data.Memories)
		}
	})

	t.Run("put_updates_content", func(t *testing.T) {
		rec := doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
			`{"project":"`+agentMemoryTestProject+`","content":"旧事实"}`)
		posted := decodeAgentMemoryEnvelope(t, rec)
		if posted.Data.Entry.ID <= 0 {
			t.Fatalf("post entry id = %d, want > 0; body=%s", posted.Data.Entry.ID, rec.Body.String())
		}
		rec = doAgentMemoryReq(t, mux, http.MethodPut,
			"/api/agent/memory/"+strconv.Itoa(posted.Data.Entry.ID),
			`{"project":"`+agentMemoryTestProject+`","content":"新事实"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("put code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		rec = doAgentMemoryReq(t, mux, http.MethodGet,
			"/api/agent/memory?project="+agentMemoryTestProject, "")
		env := decodeAgentMemoryEnvelope(t, rec)
		for _, m := range env.Data.Memories {
			if m.ID != posted.Data.Entry.ID {
				continue
			}
			if m.Content != "新事实" {
				t.Fatalf("id %d content = %q, want 新事实", m.ID, m.Content)
			}
			// spec §8 口径:PUT 保留原 source、不刷 last_hit_at。
			if m.Source != "manual" {
				t.Fatalf("id %d source = %q, want manual 保留", m.ID, m.Source)
			}
			if m.LastHitAt != posted.Data.Entry.LastHitAt {
				t.Fatalf("id %d last_hit_at = %q, want 不变(=%q)", m.ID, m.LastHitAt, posted.Data.Entry.LastHitAt)
			}
		}
	})

	t.Run("delete_single", func(t *testing.T) {
		rec := doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
			`{"project":"`+agentMemoryTestProject+`","content":"待删条目"}`)
		posted := decodeAgentMemoryEnvelope(t, rec)
		if posted.Data.Entry.ID <= 0 {
			t.Fatalf("post entry id = %d, want > 0", posted.Data.Entry.ID)
		}
		rec = doAgentMemoryReq(t, mux, http.MethodDelete,
			"/api/agent/memory/"+strconv.Itoa(posted.Data.Entry.ID)+"?project="+agentMemoryTestProject, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("delete code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		rec = doAgentMemoryReq(t, mux, http.MethodGet,
			"/api/agent/memory?project="+agentMemoryTestProject, "")
		env := decodeAgentMemoryEnvelope(t, rec)
		for _, m := range env.Data.Memories {
			if m.ID == posted.Data.Entry.ID {
				t.Fatalf("id %d still present after DELETE single", m.ID)
			}
		}
	})

	t.Run("delete_missing_404", func(t *testing.T) {
		rec := doAgentMemoryReq(t, mux, http.MethodDelete,
			"/api/agent/memory/99999?project="+agentMemoryTestProject, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
		if env := decodeAgentMemoryEnvelope(t, rec); env.Msg != "记忆条目不存在: 99999" {
			t.Fatalf("msg = %q, want 记忆条目不存在: 99999", env.Msg)
		}
	})

	t.Run("delete_clear", func(t *testing.T) {
		for _, content := range []string{"清空甲", "清空乙"} {
			doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
				`{"project":"`+agentMemoryTestProject+`","content":"`+content+`"}`)
		}
		rec := doAgentMemoryReq(t, mux, http.MethodDelete,
			"/api/agent/memory?project="+agentMemoryTestProject, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("clear code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		rec = doAgentMemoryReq(t, mux, http.MethodGet,
			"/api/agent/memory?project="+agentMemoryTestProject, "")
		env := decodeAgentMemoryEnvelope(t, rec)
		if len(env.Data.Memories) != 0 {
			t.Fatalf("memories = %+v, want empty after clear", env.Data.Memories)
		}
	})

	t.Run("post_content_over_cap_400", func(t *testing.T) {
		over := strings.Repeat("汉", 501)
		rec := doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
			`{"project":"`+agentMemoryTestProject+`","content":"`+over+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if env := decodeAgentMemoryEnvelope(t, rec); env.Msg != "单条记忆超上限 500 字符" {
			t.Fatalf("msg = %q, want 单条记忆超上限 500 字符", env.Msg)
		}
	})

	t.Run("get_projects", func(t *testing.T) {
		doAgentMemoryReq(t, mux, http.MethodPost, "/api/agent/memory",
			`{"project":"`+agentMemoryTestProject+`","content":"清单条目"}`)
		rec := doAgentMemoryReq(t, mux, http.MethodGet, "/api/agent/memory/projects", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("projects code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		env := decodeAgentMemoryEnvelope(t, rec)
		found := false
		for _, p := range env.Data.Projects {
			if p.Project == agentMemoryTestProject && p.Count >= 1 {
				found = true
			}
		}
		if !found {
			t.Fatalf("projects = %+v, want %s present", env.Data.Projects, agentMemoryTestProject)
		}
	})
}

// TestAgentMemoryRoutesDualRegistration 双注册冒烟(spec §11):两处注册代码
// (core routes.go register 裸 pattern 透传 + agentruntime mux.HandleFunc)
// 逐字复刻到各自 mux,同一 handler 实例,六 pattern 全探针——两条路由形态互
// 不吞,注册本身不触发 ServeMux panic(生产侧另一半由 agentruntime 包真函数
// 测试钉死,两形态不再恒等价)。
func TestAgentMemoryRoutesDualRegistration(t *testing.T) {
	swapAgentCoreMemoryDirForHandlerTest(t)
	h := NewAgentMemoryHandler()

	buildCoreRoutesMux := func() *http.ServeMux {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/agent/memory", h.HandleMemoryList)
		mux.HandleFunc("DELETE /api/agent/memory", h.HandleMemoryDelete)
		mux.HandleFunc("POST /api/agent/memory", h.HandleMemoryPost)
		mux.HandleFunc("PUT /api/agent/memory/{id}", h.HandleMemoryPut)
		mux.HandleFunc("DELETE /api/agent/memory/{id}", h.HandleMemoryDelete)
		mux.HandleFunc("GET /api/agent/memory/projects", h.HandleMemoryProjects)
		return mux
	}
	// agentruntime server.go 同款方法前缀形态(六行与 core 侧一字不差)。
	buildAgentRuntimeMux := buildCoreRoutesMux

	for _, tc := range []struct {
		name string
		mux  *http.ServeMux
	}{
		{name: "core_routes", mux: buildCoreRoutesMux()},
		{name: "agentruntime_server", mux: buildAgentRuntimeMux()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// POST 铺一条拿 id。
			rec := doAgentMemoryReq(t, tc.mux, http.MethodPost, "/api/agent/memory",
				`{"project":"/tmp/dual-proj","content":"双注册探测"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("post code = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			id := decodeAgentMemoryEnvelope(t, rec).Data.Entry.ID
			if id <= 0 {
				t.Fatalf("post entry id = %d, want > 0; body=%s", id, rec.Body.String())
			}
			// PUT {id} 探针。
			rec = doAgentMemoryReq(t, tc.mux, http.MethodPut,
				"/api/agent/memory/"+strconv.Itoa(id),
				`{"project":"/tmp/dual-proj","content":"双注册改写"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT {id} status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			// GET list / GET projects 探针——两条精确 pattern 互不吞。
			for _, probe := range []string{
				"/api/agent/memory?project=/tmp/dual-proj",
				"/api/agent/memory/projects",
			} {
				rec := doAgentMemoryReq(t, tc.mux, http.MethodGet, probe, "")
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s status = %d, want 200 (mux 裸 404 即被吞/漏注册); body=%s",
						probe, rec.Code, rec.Body.String())
				}
			}
			// DELETE {id} 探针。
			rec = doAgentMemoryReq(t, tc.mux, http.MethodDelete,
				"/api/agent/memory/"+strconv.Itoa(id)+"?project=/tmp/dual-proj", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("DELETE {id} status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			// DELETE 清空探针。
			rec = doAgentMemoryReq(t, tc.mux, http.MethodDelete,
				"/api/agent/memory?project=/tmp/dual-proj", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("DELETE 清空 status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
