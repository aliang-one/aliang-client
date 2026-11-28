package app

import (
	"errors"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// dist 页面(仪表盘 index.html + 托盘面板 panel.html)引用的每一份静态资产,
// 都必须真实存在于 WebsiteFS。
//
// 背景(2026-11-27 托盘面板白屏):go:embed 目录形态默认排除 `_`/`.` 前缀
// 文件,Vite 的 Vue 共享 chunk 恰好叫 `_plugin-vue_export-helper-*.js`,
// 被静默剔除出嵌入 FS → 页面 JS 首行 import 404 → Vue 挂不上 #app →
// 仪表盘/面板双双白屏,且服务端无任何报错(文件级丢失,HTTP 面上是干净
// 的 200 页面 + 一个普通 404)。此测试钉住这一类回归:任何被 HTML 引用
// 却没进 embed 的资产,构建期就红。
func TestWebsiteFSContainsAllReferencedAssets(t *testing.T) {
	const distRoot = "website/dist"

	pages := []string{"index.html", "panel.html"}
	attrRe := regexp.MustCompile(`(?:src|href)="([^"]+)"`)

	for _, page := range pages {
		raw, err := fs.ReadFile(WebsiteFS, distRoot+"/"+page)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s 尚未构建(dist 为本地构建产物):%v", page, err)
			}
			t.Fatalf("打开 %s 失败: %v", page, err)
		}

		pageDir := distRoot + "/"
		if i := strings.LastIndex(page, "/"); i >= 0 {
			pageDir = distRoot + page[:i+1]
		}

		for _, m := range attrRe.FindAllStringSubmatch(string(raw), -1) {
			ref := m[1]
			// 外链与内联数据不落 FS;仅校验站点内路径。
			if strings.HasPrefix(ref, "data:") ||
				strings.HasPrefix(ref, "http://") ||
				strings.HasPrefix(ref, "https://") ||
				strings.HasPrefix(ref, "//") {
				continue
			}

			var p string
			if strings.HasPrefix(ref, "/") {
				p = distRoot + ref
			} else {
				p = pageDir + ref
			}
			// 远程协助 P2(Task 3):构建产物改相对 base 后引用带 ./ 前缀
			// (./assets/main-*.js)。embed FS 只收 clean 路径,fs.Stat 不做
			// 目录清理——此处必须显式 Clean,否则相对化改造会被本测试误红。
			p = path.Clean(p)
			if _, err := fs.Stat(WebsiteFS, p); err != nil {
				t.Errorf("%s 引用的资产不在嵌入 FS: %s (%v)", page, ref, err)
			}
		}
	}
}
