package common

import (
	"encoding/json"
	"net/http"
	"time"
)

// Response 通用的HTTP响应结构体 (deprecated: use CommonResponse instead)
type Response struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data"`
}

// CommonResponse 标准的API响应格式
type CommonResponse struct {
	Code      int         `json:"code"`
	Msg       string      `json:"msg"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"timestamp"`
	TraceID   string      `json:"trace_id,omitempty"`
}

// ErrorDetail 错误详情结构体
type ErrorDetail struct {
	ErrorCode string      `json:"error_code,omitempty"`
	ErrorMsg  string      `json:"error_msg,omitempty"`
	Details   interface{} `json:"details,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

// ListResponse 列表响应结构体（带分页）
type ListResponse struct {
	Items      interface{} `json:"items"`
	Total      int         `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	TotalPages int         `json:"total_pages"`
}

// SendResponse 发送成功响应 (deprecated: use Success instead)
func SendResponse(w http.ResponseWriter, data interface{}) {
	resp := Response{
		Code: 0,
		Msg:  "success",
		Data: data,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// SendError 发送错误响应 (deprecated: use Error instead)
func SendError(w http.ResponseWriter, msg string, statusCode int, data interface{}) {
	resp := Response{
		Code: statusCode,
		Msg:  msg,
		Data: data,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(resp)
}

// Success 发送成功响应（新方式）
func Success(w http.ResponseWriter, data interface{}) {
	resp := CommonResponse{
		Code:      CodeSuccess,
		Msg:       ErrorCodeToMessage(CodeSuccess),
		Data:      data,
		Timestamp: time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// SuccessList 发送列表成功响应
func SuccessList(w http.ResponseWriter, items interface{}, total int, page int, pageSize int) {
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	listResp := ListResponse{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}

	resp := CommonResponse{
		Code:      CodeSuccess,
		Msg:       ErrorCodeToMessage(CodeSuccess),
		Data:      listResp,
		Timestamp: time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// Error 发送错误响应
func Error(w http.ResponseWriter, code int, msg string, details interface{}) {
	httpStatus := ErrorCodeToHTTPStatus(code)
	resp := CommonResponse{
		Code: code,
		Msg:  msg,
		Data: ErrorDetail{
			ErrorCode: ErrorCodeToMessage(code),
			ErrorMsg:  msg,
			Details:   details,
			Timestamp: time.Now().Unix(),
		},
		Timestamp: time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(resp)
}

// ErrorBadRequest 发送400错误响应
func ErrorBadRequest(w http.ResponseWriter, msg string, details interface{}) {
	Error(w, CodeBadRequest, msg, details)
}

// ErrorUnauthorized 发送401错误响应
func ErrorUnauthorized(w http.ResponseWriter, msg string) {
	Error(w, CodeUnauthorized, msg, nil)
}

// ErrorForbidden 发送403错误响应
func ErrorForbidden(w http.ResponseWriter, msg string) {
	Error(w, CodeForbidden, msg, nil)
}

// ErrorNotFound 发送404错误响应
func ErrorNotFound(w http.ResponseWriter, msg string) {
	Error(w, CodeNotFound, msg, nil)
}

// ErrorConflict 发送409错误响应
func ErrorConflict(w http.ResponseWriter, msg string) {
	Error(w, CodeConflict, msg, nil)
}

// ErrorInternalServer 发送500错误响应
func ErrorInternalServer(w http.ResponseWriter, msg string, details interface{}) {
	Error(w, CodeInternalServer, msg, details)
}

// ErrorServiceUnavailable 发送503错误响应
func ErrorServiceUnavailable(w http.ResponseWriter, msg string) {
	Error(w, CodeServiceUnavailable, msg, nil)
}
