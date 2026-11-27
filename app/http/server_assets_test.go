package http

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

// /assets/ 缓存语义回归(2026-11-27 托盘面板白屏残留根因):immutable 只许
// 上**命中**响应;404/400 必须 no-store。此前缓存头在进任何 miss 判定前
// 就已设好,404 带一年期 immutable 出门——浏览器按规范合法负缓存,而资产
// URL 是内容寻址(修复版前端引用同一 URL),毒缓存永不自愈,用户侧白屏
// 只能手清缓存。
func TestAssetsHandlerNegativeResponsesAreNotCacheable(t *testing.T) {
	websiteRoot, _, err := resolveWebsiteRoot()
	if err != nil {
		t.Skipf("嵌入站点不可用(dist 未构建): %v", err)
	}
	handler := assetsFileHandler(websiteRoot)

	// 命中:200 + immutable(既有语义不得漂移)。
	var hitPath string
	if entries, err := fs.ReadDir(websiteRoot, "assets"); err == nil && len(entries) > 0 {
		hitPath = "/assets/" + entries[0].Name()
	}
	if hitPath != "" {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, hitPath, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("命中资产 %s 应 200, got %d", hitPath, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("命中资产缓存头漂移: got %q", cc)
		}
	} else {
		t.Log("dist/assets 为空,跳过命中分支")
	}

	// miss:404 必须 no-store(负缓存禁令——本案主断言)。
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/assets/definitely-missing-9e83b1.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("miss 应 404, got %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("404 当前缓存头 %q:可被浏览器负缓存且 URL 前后版本同址,毒缓存永不自愈;应 no-store", cc)
	}

	// 目录遍历 400 同禁。
	rec2 := httptest.NewRecorder()
	handler(rec2, httptest.NewRequest(http.MethodGet, "/assets/..%2Fsecret", nil))
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("遍历应 400, got %d", rec2.Code)
	}
	if cc := rec2.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("400 当前缓存头 %q,应 no-store", cc)
	}
}
