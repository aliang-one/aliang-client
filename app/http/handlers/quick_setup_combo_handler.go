package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
)

// parseQuickSetupComboID 解析路径 {id}（routes.go 的 ServeMux 通配路由注入），
// 失败统一 400。
func parseQuickSetupComboID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		common.ErrorBadRequest(w, "combo id is not valid", nil)
		return 0, false
	}
	return id, true
}

// classifyQuickSetupComboError 把组合 service 错误映射为 HTTP 状态码：
// 哨兵优先（NotFound→404、NameTaken→409、CapExceeded→400），再按字符串分类
// （isBadRequestError 的口径之外，补组合特有的 "is not supported" 与 disk 入口
// 的 "cannot import ... missing or unreadable"，均属客户端输入问题），否则 500。
func classifyQuickSetupComboError(err error) int {
	switch {
	case errors.Is(err, storage.ErrComboNotFound):
		return http.StatusNotFound
	case errors.Is(err, storage.ErrComboNameTaken):
		return http.StatusConflict
	case errors.Is(err, storage.ErrComboCapExceeded):
		return http.StatusBadRequest
	case isBadRequestError(err):
		return http.StatusBadRequest
	default:
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "is not supported") || strings.Contains(msg, "cannot import") {
			return http.StatusBadRequest
		}
		return http.StatusInternalServerError
	}
}

// writeQuickSetupComboError 按分类结果写错误响应。注意必须走具名 helper：
// common.Error 的入参是业务码（100/103/104/200…），直接传 HTTP 状态码会被
// ErrorCodeToHTTPStatus 归到 500。
func writeQuickSetupComboError(w http.ResponseWriter, operation string, err error) {
	switch classifyQuickSetupComboError(err) {
	case http.StatusNotFound:
		common.ErrorNotFound(w, err.Error())
	case http.StatusConflict:
		common.ErrorConflict(w, err.Error())
	case http.StatusBadRequest:
		common.ErrorBadRequest(w, err.Error(), nil)
	default:
		common.ErrorInternalServer(w, operation, map[string]interface{}{"error": err.Error()})
	}
}

// HandleCombosCreate 创建配置组合（POST /api/quick-setup/combos）。
func (h *QuickSetupHandler) HandleCombosCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}

	var req models.QuickSetupComboCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	view, err := h.comboService.Create(strings.TrimSpace(req.Software), strings.TrimSpace(req.Name), strings.TrimSpace(req.Source), req.CopyFromID, req.Variables, req.Files)
	if err != nil {
		writeQuickSetupComboError(w, "Quick setup combo create failed", err)
		return
	}

	common.Success(w, map[string]interface{}{"combo": view})
}

// HandleCombosUpdate 保存（部分更新）配置组合（PUT /api/quick-setup/combos/{id}）。
func (h *QuickSetupHandler) HandleCombosUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := parseQuickSetupComboID(w, r)
	if !ok {
		return
	}

	var req models.QuickSetupComboUpdateRequest
	r.Body = http.MaxBytesReader(w, r.Body, quickSetupRequestMaxBytes)
	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", map[string]interface{}{"error": err.Error()})
		return
	}

	view, err := h.comboService.Update(id, req.Name, req.Variables, req.Files)
	if err != nil {
		writeQuickSetupComboError(w, "Quick setup combo update failed", err)
		return
	}

	common.Success(w, map[string]interface{}{"combo": view})
}

// HandleCombosDelete 删除配置组合（DELETE /api/quick-setup/combos/{id}）。
func (h *QuickSetupHandler) HandleCombosDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := parseQuickSetupComboID(w, r)
	if !ok {
		return
	}

	if err := h.comboService.Delete(id); err != nil {
		writeQuickSetupComboError(w, "Quick setup combo delete failed", err)
		return
	}

	common.Success(w, map[string]interface{}{"id": id})
}

// HandleCombosSetDefault 设默认组合（POST /api/quick-setup/combos/{id}/default），
// 响应带该 software 的最新全部组合。
func (h *QuickSetupHandler) HandleCombosSetDefault(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !middleware.RequireDashboardSession(w, r) {
		return
	}
	id, ok := parseQuickSetupComboID(w, r)
	if !ok {
		return
	}

	combos, err := h.comboService.SetDefaultAndList(id)
	if err != nil {
		writeQuickSetupComboError(w, "Quick setup combo set default failed", err)
		return
	}

	common.Success(w, map[string]interface{}{"combos": combos})
}
