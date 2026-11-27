package services

import (
	"errors"

	inboundhttp "aliang.one/nursorgate/inbound/http"
)

// endpoint_selector_adapter.go — EndpointService → inbound/http.AIEndpointSelector
// 的适配(spec 2026-11-25 Task 3)。
//
// import 方向:services → inbound/http 合法(run.go 已 import);反向成环,所以
// 路由层的窄接口定义在 inbound/http,本文件只做形状翻译:
//   - Select 返回整行 *config.LLMEndpoint → 拆成接口的五返回值;
//   - ModelsUnion []ModelOwner → []map[string]any;
//   - 哨兵错误(ErrNoEndpoint/ErrEndpointNotAvailable/ErrQueueTimeout)→
//     路由层哨兵(errors.Is 语义,wrapped raw error 原样透传 → 路由层 500)。

// aiEndpointSelectorAdapter 是 inbound/http.AIEndpointSelector 的 services 侧实现。
type aiEndpointSelectorAdapter struct {
	svc *EndpointService
}

// 编译期契约:adapter 必须满足路由层接口。
var _ inboundhttp.AIEndpointSelector = (*aiEndpointSelectorAdapter)(nil)

// NewAIEndpointSelectorAdapter 把 registry service 适配成 56432 AI path 的选择器。
// svc 为 nil(GetEndpointService 构造失败)时返回 nil——调用方原样传给
// SetAIEndpointSelector(nil) 即降级,AI path 走原透明代理。
func NewAIEndpointSelectorAdapter(svc *EndpointService) inboundhttp.AIEndpointSelector {
	if svc == nil {
		return nil
	}
	return &aiEndpointSelectorAdapter{svc: svc}
}

func (a *aiEndpointSelectorAdapter) Select(protocol, model, explicitID, excludeID string) (string, string, string, bool, error) {
	// excludeID 非空 = 路由层重试 attempt 传来的上一失败 endpoint id:透传给
	// service 排除,天然命中下一候选(final-review 必改 2,「失败切下一
	// endpoint 重试一次」)。显式路径的排除语义在 service Select 内处理。
	ep, err := a.svc.Select(protocol, model, explicitID, excludeID)
	if err != nil {
		return "", "", "", false, translateAISelectorError(err)
	}
	return ep.ID, ep.BaseURL, ep.APIKey, ep.Builtin, nil
}

func (a *aiEndpointSelectorAdapter) Acquire(id string, maxConcurrency int) (func(), error) {
	release, err := a.svc.Acquire(id, maxConcurrency)
	if err != nil {
		return nil, translateAISelectorError(err)
	}
	return release, nil
}

func (a *aiEndpointSelectorAdapter) ReportSuccess(id string) { a.svc.ReportSuccess(id) }

func (a *aiEndpointSelectorAdapter) ReportFailure(id string, status int, errMsg string) {
	// 路由层接口仍是三参(Task 3 才加 until):本任务先传 0 = 无预解析,
	// service 按状态自行推导(429 回落组策略)。Task 5 补响应头解析后在此透传。
	a.svc.ReportFailure(id, status, errMsg, 0)
}

func (a *aiEndpointSelectorAdapter) ModelsUnion() []map[string]any {
	owners := a.svc.ModelsUnion()
	out := make([]map[string]any, 0, len(owners))
	for _, m := range owners {
		out = append(out, map[string]any{"id": m.ID, "owner": m.Owner})
	}
	return out
}

// translateAISelectorError 把本包哨兵翻译成路由层哨兵:inbound/http 不 import
// 本包(防环),它的哨兵只能由 adapter 侧映射。wrapped raw error 原样透传,
// 路由层按非哨兵映射 500。
func translateAISelectorError(err error) error {
	switch {
	case errors.Is(err, ErrEndpointNotAvailable):
		return inboundhttp.ErrAIEndpointNotAvailable
	case errors.Is(err, ErrQueueTimeout):
		return inboundhttp.ErrAIQueueTimeout
	case errors.Is(err, ErrNoEndpoint):
		return inboundhttp.ErrAINoEndpoint
	default:
		return err
	}
}
