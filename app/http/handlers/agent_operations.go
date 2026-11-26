package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/services"
)

type operationStreamWriter struct {
	http.ResponseWriter
	flush func()
}

func (w operationStreamWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.flush()
	return n, err
}

func (h *AgentHandler) HandleOperationEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !services.IsUserAgentRuntime() {
		if !middleware.RequireDashboardSession(w, r) {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unavailable", http.StatusInternalServerError)
			return
		}
		h.proxyFSIfNeeded(operationStreamWriter{ResponseWriter: w, flush: flusher.Flush}, r)
		return
	}
	if !middleware.IsLoopbackRequest(r) && !middleware.RequireDashboardSession(w, r) {
		return
	}
	h.service.ServeOperationEvents(w, r)
}
