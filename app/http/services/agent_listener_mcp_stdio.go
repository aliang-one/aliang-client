package services

// agent_listener_mcp_stdio.go 监听器 stdio MCP 面 invoke 层(specs/005-event-
// listener §5,plan Task 13)。`aliang mcp` 是 CLI 每次拉起的冷子进程,云端
// 凭据(用户 JWT + 设备身份)只住常驻 user agent 进程内存——冷子进程直调
// 共享处理器会恒报 listener cloud offline。因此 stdio 面照 ai_run_* 范式走
// 一跳:cmd/mcp.go 四枚 listener_* 工具 POST 本地 56433 路由(handlers 层
// shell,agentruntime 双注册),落到本文件的导出 invoke 函数,在常驻进程里
// 复用 Task 12 共享处理器(agent_listener_tools.go,零修改)。
//
// 零上下文契约(任务拍板):stdio 面无环境会话,listenerToolContext 置零——
// sessionID=""(create 的 target_session_id 无从缺省回填,1a 必填 fail-fast
// 如实上抛:调用方必须显式传,工具描述已明示 REQUIRED on this face);
// approvalModeOf=nil(不产出 approval_warning——审批模式是会话内语义,本面
// 无会话可查,不猜;审批策略由消费方 CLI/服务端既有闸执行)。
//
// 参数抽取与 agentcore 桥(agent_listener_tools.go)同款(inputStringField /
// remoteString / params 对象整体透传),两面参数面语义等价由
// PeerMCPContractSpecs 契约行钉住。

import (
	"context"
	"errors"
	"net/http"
)

// ListenerCoreToolSchemas 返回 agentcore 监听器工具面(注入描述符)的
// name→inputSchema 快照。唯一用途:cmd 侧 stdio 描述符副本的跨包深度对账
// 锚点——PeerMCPContractSpecs 契约行只钉参数名集+描述关键词,本锚点补钉
// 嵌套 source 形状与属性描述文本(M1)。非运行时面,勿在业务路径消费;构造
// 走零依赖 manager,门控读当前全局配置(customer.event_listeners 默认开,
// 消费方测试先 ResetGlobalConfigForTest)。
func ListenerCoreToolSchemas() map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	for _, tool := range newAgentAIManager().agentCoreListenerTools(agentAIRun{sessionID: "schema-anchor"}) {
		out[tool.Name()] = tool.InputSchema()
	}
	return out
}

// listenerMCPErrorStatus 把共享处理器错误映射到 HTTP 语义档(handlers 层据
// 此选信封码;信封 msg 恒为 err.Error() 原文,stdio 面 isError 文本与之
// 同源):离线哨兵/云端 5xx=503(不是调用方的错);云端 409(LWW 冲突)=409,
// 提示调用方取最新 updated_at 重试;其余(本地预检/1a 必填/云端 4xx 业务
// 错)=400。
func listenerMCPErrorStatus(err error) int {
	if errors.Is(err, errListenerCloudOffline) {
		return http.StatusServiceUnavailable
	}
	var statusErr *listenerCloudStatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.status == http.StatusConflict:
			return http.StatusConflict
		case statusErr.status >= http.StatusInternalServerError:
			return http.StatusServiceUnavailable
		}
	}
	return http.StatusBadRequest
}

// ListenerMCPCreate 注册监听器(stdio 面)。source{type,device_id?,params}
// 嵌套对象在本层抽取:type/device_id 取字符串、params 对象整体透传(与
// agentcore 桥同款);file_tail/process_exit 的设备归属闸与 1a target 必填
// fail-fast 都在共享处理器内,零云端调用即拒绝。
func ListenerMCPCreate(ctx context.Context, args map[string]interface{}) (interface{}, int, error) {
	var sourceType, sourceDeviceID string
	var sourceParams map[string]interface{}
	if source, ok := args["source"].(map[string]interface{}); ok {
		sourceType = remoteString(source, "type")
		sourceDeviceID = remoteString(source, "device_id")
		sourceParams, _ = source["params"].(map[string]interface{})
	}
	result, err := listenerToolCreate(ctx, listenerToolContext{sessionID: "", approvalModeOf: nil},
		inputStringField(args, "title"),
		sourceType,
		sourceParams,
		sourceDeviceID,
		inputStringField(args, "prompt_template"),
		inputStringField(args, "mode"),
		inputStringField(args, "target_session_id"),
	)
	if err != nil {
		return nil, listenerMCPErrorStatus(err), err
	}
	return result, http.StatusOK, nil
}

// ListenerMCPList 全量列表(stdio 面;零参,入参签名与其余三枚统一供
// handlers shell 复用)。行经 ListenerMirror 类型钉契约后序列化,与侧栏
// 面板/agentcore 面同源,无第二套口径。
func ListenerMCPList(ctx context.Context, _ map[string]interface{}) (interface{}, int, error) {
	rows, err := listenerToolList(ctx)
	if err != nil {
		return nil, listenerMCPErrorStatus(err), err
	}
	return rows, http.StatusOK, nil
}

// ListenerMCPUpdate 部分更新(stdio 面):args 整包交给共享处理器,patch 键
// 白名单在其内部过滤(与 agentcore 桥同款——id 不入 patch,junk 键丢弃),
// LWW updated_at 缺省由本地镜像行回填。
func ListenerMCPUpdate(ctx context.Context, args map[string]interface{}) (interface{}, int, error) {
	result, err := listenerToolUpdate(ctx, inputStringField(args, "id"), args)
	if err != nil {
		return nil, listenerMCPErrorStatus(err), err
	}
	return result, http.StatusOK, nil
}

// ListenerMCPDelete 删除(stdio 面);畸形 id 在共享处理器的 id-guard 本地
// 拒绝,零云端调用。
func ListenerMCPDelete(ctx context.Context, args map[string]interface{}) (interface{}, int, error) {
	result, err := listenerToolDelete(ctx, inputStringField(args, "id"))
	if err != nil {
		return nil, listenerMCPErrorStatus(err), err
	}
	return result, http.StatusOK, nil
}
