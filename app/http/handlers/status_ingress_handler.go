// Package handlers — 状态页「接入客户端」handler：GET /api/status/ingress。
// 数据源=inbound/ssh 进程级 reconcile 实例的会话台账（谁经 SSH 入口接进来）。
// 只读无副作用；入口未配置/未启动返回 ok=false + 空列表（前端渲染「未运行」
// 态而非报错），与 SSHChainStatus 的未配置语义同构。
package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	sshin "aliang.one/nursorgate/inbound/ssh"
)

// HandleIngressSessions GET /api/status/ingress：在线接入客户端 + 已关闭
// 环形历史（300 条,新→旧）。非 GET 一律 405（真实 HTTP 状态码,同
// SSHIngressHandler.HandleHostkey 的 http.Error 先例——common.Error 的
// 业务码会被映射成 500）。
func HandleIngressSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snapshot, ok := sshin.SessionsSnapshotOf()
		if !ok {
			common.Success(w, map[string]interface{}{
				"ok":     false,
				"active": []sshin.Session{},
				"closed": []sshin.Session{},
			})
			return
		}
		common.Success(w, map[string]interface{}{
			"ok":     true,
			"active": snapshot.Active,
			"closed": snapshot.Closed,
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
