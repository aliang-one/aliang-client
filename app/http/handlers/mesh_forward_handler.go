package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

// MeshCoreForwardHandler 56433 侧 /api/mesh/* 透传(user-agent runtime
// 注册用)。mesh 数据面(beacon/装配)住 core 进程——56433 裸挂
// MeshHandler 会恒报 running=false+空候选(反向假红,违反 D2 治理原则),
// 故本端点一律 forward-to-core(56431)取真值。core 侧路由表(routes.go)
// 直挂 MeshHandler,本 handler 在 56431 不存在;即使单进程形态经 56433
// 命中,转发目标 56431 仍由 MeshHandler 直答,无转发环。
// 不可达错误走 common.Error(CodeServiceUnavailable→HTTP 500,与
// proxyIfNeeded 的下游不可达形态一致,不另造 503 形状)。
// 响应头刻意只透 Content-Type:两端点不读请求头、只产 JSON,避免向 core
// 泄漏仪表盘侧鉴权头(trace-ID 断链已知取舍)。
// D3 双注册纪律(2026-10-21/10-23/10-29 三次 404 透传事故同族)。
type MeshCoreForwardHandler struct {
	coreBase func() string
	client   *http.Client
}

func NewMeshCoreForwardHandler() *MeshCoreForwardHandler {
	return NewMeshCoreForwardHandlerWithBase(nil)
}

// NewMeshCoreForwardHandlerWithBase 注入式构造(生产接线用):coreBase 由
// 注册方供给,生产注入同包 ownerBaseURL(override > ALIANG_MANAGEMENT_ADDR
// env > DefaultSessionOwnerAddr),真修好两场景——--host 定制绑定与
// ALIANG_MANAGEMENT_ADDR 部署 env(管理监听址与 agent 所见不一致)。其余
// 两腿诚实交代:①override 腿在本进程结构性恒空——56433 恒非 session-owner
// (cmd/agent.go:40 SetSessionOwnerProcess(false)),地址回灌只发生在 core;
// ②ALIANG_SESSION_OWNER_ADDR 刻意不消费——mesh 面目标是管理面非会话属主,
// 且该 env 可能指向仅挂一条路由的 notify 微监听,打过去=必 404。core 侧
// 56431 占用回退随机端口场景本链不覆盖(解析落回默认 56431,与旧硬编码
// 同),残余账已挂计划文档。nil=回退 loopback 默认(56431,仅测试/单进程
// 形态兜底)。
func NewMeshCoreForwardHandlerWithBase(coreBase func() string) *MeshCoreForwardHandler {
	if coreBase == nil {
		coreBase = func() string {
			return "http://127.0.0.1:" + strconv.Itoa(config.DefaultManagementPort)
		}
	}
	return &MeshCoreForwardHandler{
		coreBase: coreBase,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// ServeHTTP 同路径透传(method/query/body 原样;method 闸归 core 侧
// handler——core 对非 GET 已回 405,本层不再重复判)。不可达错误体走
// common.Error 包裹形态(HTTP 500,见类型注释),与 agent_handler
// proxyIfNeeded 的不可达语义一致。
func (h *MeshCoreForwardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := h.coreBase() + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		common.Error(w, common.CodeServiceUnavailable, "core process is not reachable", map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		logger.Warn(fmt.Sprintf("[MESH-FORWARD] response copy truncated path=%s err=%v", r.URL.Path, err))
	}
}
