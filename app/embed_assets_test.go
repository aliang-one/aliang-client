package app

import (
	"errors"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestWebsiteFSContainsAllTransitiveImports —— dist 页面静态资产(JS)里
// 的 ES module import 依赖,必须递归真实存在于 WebsiteFS。
//
// 背景(2026-11-28 托盘面板白屏第二层):embed_test.go(HTML src/href 扫描)
// 只能抓「HTML 直接引用」的资产;Vite 的**代码分包**是 JS 内部
// `import{...}from"./xxx-hash.js"` 引用的,不经过 HTML——当 go:embed 或
// 构建链静默丢掉任何一个分包(如 `_plugin-vue_export-helper-*.js`),页面
// 照常 200、入口 JS 照常 200,只有深链 import 404,Vue 挂不上,白屏且
// 服务端零报错。本测试沿 HTML→JS→JS 递归扫 import 说明符,任何一环缺失
// 构建期即红。
//
// 路径口径:HTML 引用是站点绝对路径(/assets/x.svg,相对 distRoot);
// JS 内 import 是相对**本 JS 所在目录**的说明符(./y 即 assets/y),
// 用 path.Join(jsDir, spec) 解析。
func TestWebsiteFSContainsAllTransitiveImports(t *testing.T) {
	const distRoot = "website/dist"

	exists := func(relPath string) bool {
		_, err := fs.Stat(WebsiteFS, distRoot+"/"+relPath)
		return err == nil
	}

	// minified ES module 里的 import 形态:`from"./x.js"` / `import"./x.js"`
	// (压缩代码无空格;说明符必须 ./ 开头——字符串里的伪 import 无 ./ 前缀
	// 天然滤除)。
	importRe := regexp.MustCompile(`(?:from|import)\(\s*["'](\./[^"']+)["']|(?:from|import)["'](\./[^"']+)["']`)
	refRe := regexp.MustCompile(`(?:src|href)="([^"]+)"`)

	entries, rdErr := fs.ReadDir(WebsiteFS, distRoot)
	t.Logf("DEBUG ReadDir(%s) err=%v n=%d", distRoot, rdErr, len(entries))
	for _, e := range entries {
		t.Logf("DEBUG entry: %s dir=%v", e.Name(), e.IsDir())
	}
	t.Logf("DEBUG Stat icon.svg err=%v", func() error { _, e := fs.Stat(WebsiteFS, distRoot+"/icon.svg"); return e }())

	pages := []string{"index.html", "panel.html"}
	visited := map[string]bool{}
	var missing []string

	var scan func(relPath string) // 递归扫 JS,链路缺失时直接报错
	scan = func(relPath string) {
		if visited[relPath] {
			return
		}
		visited[relPath] = true
		raw, err := fs.ReadFile(WebsiteFS, distRoot+"/"+relPath)
		if err != nil {
			missing = append(missing, relPath+" (读取失败:"+err.Error()+")")
			return
		}
		jsDir := path.Dir(relPath)
		for _, m := range importRe.FindAllStringSubmatch(string(raw), -1) {
			rawSpec := m[1]
			if rawSpec == "" {
				rawSpec = m[2] // 动态 import(...) 分支
			}
			target := path.Join(jsDir, rawSpec)
			if exists(target) {
				if strings.HasSuffix(target, ".js") {
					scan(target) // 递归:分包还可能再 import 分包
				}
				continue
			}
			missing = append(missing, relPath+" → import \""+rawSpec+"\" 解析为 "+target+" 在嵌入 FS 中不存在")
		}
	}

	for _, page := range pages {
		raw, err := fs.ReadFile(WebsiteFS, distRoot+"/"+page)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				t.Skipf("%s 尚未构建(dist 为本地构建产物):%v", page, err)
			}
			t.Fatalf("读取嵌入 %s 失败:%v", page, err)
		}
		for _, m := range refRe.FindAllStringSubmatch(string(raw), -1) {
			// 远程协助 P2 相对化改造后页面引用带 ./ 前缀;embed FS 只收
			// clean 路径(与 embed_test.go 的「先拼 distRoot 再 Clean」同
			// 口径),此处先拼后 Clean 归一。
			target := strings.TrimPrefix(path.Clean(distRoot+"/"+m[1]), distRoot+"/")
			if target == "" {
				continue
			}
			if exists(target) {
				if strings.HasSuffix(target, ".js") {
					scan(target)
				}
				continue
			}
			missing = append(missing, page+" → "+m[1]+" 在嵌入 FS 中不存在")
		}
	}

	if len(missing) > 0 {
		t.Fatalf("嵌入 FS 缺失被引用的静态资产(go:embed 是否用了 all: 前缀?Vite 共享分包以下划线开头会被目录形态静默剔除):\n%s",
			strings.Join(missing, "\n"))
	}
}
