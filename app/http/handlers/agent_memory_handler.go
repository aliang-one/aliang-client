package handlers

// agent_memory_handler.go — agentcore 记忆 CRUD API。
// 双注册纪律:core routes.go + agentruntime server.go 缺任一侧=404(老坑)。
// 无状态:store 是 services 包级单例,两进程各持 handler 等价。
//
// 双写者取舍(spec 拍板双注册):双注册=双写者——core 进程手动编辑 vs runtime
// 进程异步提取/touch 并发直写同一 JSON 文件;store 单锁只在进程内,无跨进程
// 锁,极端并发下存在丢更新窗口,按 LWW(整文件覆盖序)有意取舍。若后续要收敛,
// 可给 core 侧补 proxy 面(沿 mcp registry 的 proxyIfNeeded 先例)转发到
// runtime 单点写。
import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
)

// agentCoreMemoryRequestMaxBytes 请求体上限,防呆(同族 mcp registry 同值)。
const agentCoreMemoryRequestMaxBytes = 64 << 10 // 64KB

type AgentMemoryHandler struct{}

func NewAgentMemoryHandler() *AgentMemoryHandler { return &AgentMemoryHandler{} }

func memoryProjectFromQuery(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("project"))
}

// HandleMemoryList GET /api/agent/memory?project= 列表;无 project=400。
func (h *AgentMemoryHandler) HandleMemoryList(w http.ResponseWriter, r *http.Request) {
	project := memoryProjectFromQuery(r)
	if project == "" {
		common.ErrorBadRequest(w, "project 必填", nil)
		return
	}
	common.Success(w, map[string]interface{}{"memories": services.AgentCoreMemory.List(project)})
}

// HandleMemoryProjects GET /api/agent/memory/projects 项目清单(卡片下拉)。
func (h *AgentMemoryHandler) HandleMemoryProjects(w http.ResponseWriter, r *http.Request) {
	common.Success(w, map[string]interface{}{"projects": services.AgentCoreMemory.Projects()})
}

// HandleMemoryPost POST /api/agent/memory {project, content} 手动增。
func (h *AgentMemoryHandler) HandleMemoryPost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, agentCoreMemoryRequestMaxBytes)
	var body struct{ Project, Content string }
	if err := common.DecodeRequest(r, &body); err != nil {
		common.ErrorBadRequest(w, "请求体非法", nil)
		return
	}
	if strings.TrimSpace(body.Project) == "" {
		common.ErrorBadRequest(w, "project 必填", nil)
		return
	}
	e, err := services.AgentCoreMemory.Add(body.Project, body.Content)
	if err != nil {
		writeAgentMemoryError(w, err)
		return
	}
	common.Success(w, map[string]interface{}{"entry": e})
}

// HandleMemoryPut PUT /api/agent/memory/{id} {project, content}。
func (h *AgentMemoryHandler) HandleMemoryPut(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		common.ErrorBadRequest(w, "id 非法", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, agentCoreMemoryRequestMaxBytes)
	var body struct{ Project, Content string }
	if err := common.DecodeRequest(r, &body); err != nil {
		common.ErrorBadRequest(w, "请求体非法", nil)
		return
	}
	if strings.TrimSpace(body.Project) == "" {
		common.ErrorBadRequest(w, "project 必填", nil)
		return
	}
	if err := services.AgentCoreMemory.Update(body.Project, id, body.Content); err != nil {
		writeAgentMemoryError(w, err)
		return
	}
	common.Success(w, map[string]interface{}{"ok": true})
}

// HandleMemoryDelete DELETE:带 id 路径=删单条,无=清空项目。
func (h *AgentMemoryHandler) HandleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	project := memoryProjectFromQuery(r)
	if project == "" {
		common.ErrorBadRequest(w, "project 必填", nil)
		return
	}
	if raw := r.PathValue("id"); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			common.ErrorBadRequest(w, "id 非法", nil)
			return
		}
		if err := services.AgentCoreMemory.Remove(project, id); err != nil {
			writeAgentMemoryError(w, err)
			return
		}
	} else if err := services.AgentCoreMemory.Clear(project); err != nil {
		writeAgentMemoryError(w, err)
		return
	}
	common.Success(w, map[string]interface{}{"ok": true})
}

// writeAgentMemoryError 业务错误分诊:白名单内业务文案按语义分码——「记忆条
// 目不存在」=404;「content 必填」/「单条记忆超上限」=400 原文透传。白名单
// 外(「记忆存储不可用」与落盘 I/O 类 os 裸错)一律 500 固定文案「记忆存储
// 不可用」,原始 err 只进服务端日志——err.Error() 直透会把本机路径泄漏给客户端。
func writeAgentMemoryError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "记忆条目不存在"):
		common.ErrorNotFound(w, msg)
	case strings.Contains(msg, "content 必填"), strings.Contains(msg, "单条记忆超上限"):
		common.ErrorBadRequest(w, msg, nil)
	default:
		logger.Warn(fmt.Sprintf("agentcore-memory: API 落盘失败,响应固定文案 err=%v", err))
		common.ErrorInternalServer(w, "记忆存储不可用", nil)
	}
}
