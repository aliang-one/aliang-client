package handlers

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	tutorialdocs "aliang.one/nursorgate/docs/tutorials"
)

var allowedTutorialLocales = map[string]struct{}{
	"en":     {},
	"zh_CN":  {},
}

var allowedTutorialDocs = map[string]struct{}{
	"getting-started": {},
	"usage-guide":     {},
}

type TutorialHandler struct{}

func NewTutorialHandler() *TutorialHandler {
	return &TutorialHandler{}
}

func (h *TutorialHandler) HandleGetTutorial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	locale := strings.TrimSpace(r.URL.Query().Get("locale"))
	doc := strings.TrimSpace(r.URL.Query().Get("doc"))
	if locale == "" || doc == "" {
		common.ErrorBadRequest(w, "locale and doc are required", nil)
		return
	}

	if _, ok := allowedTutorialLocales[locale]; !ok {
		common.ErrorBadRequest(w, "unsupported locale", nil)
		return
	}
	if _, ok := allowedTutorialDocs[doc]; !ok {
		common.ErrorBadRequest(w, "unsupported doc", nil)
		return
	}

	filePath := path.Join(locale, doc+".md")
	content, err := fs.ReadFile(tutorialdocs.FS, filePath)
	if err != nil {
		common.Error(w, http.StatusNotFound, "tutorial not found", nil)
		return
	}

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}
