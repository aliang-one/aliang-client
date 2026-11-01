package agentcore

import (
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/internal/runtimepath"
)

// testHome 与生产代码同源取家目录（runtimepath.EffectiveAgentHome），
// 避免测试用 os.UserHomeDir 而漏掉 root 进程口径的偏差。
func testHome(t *testing.T) string {
	t.Helper()
	home, err := runtimepath.EffectiveAgentHome()
	if err != nil || home == "" {
		t.Skip("无法定位家目录")
	}
	return home
}

// TestUnderHomePrivacyDir 覆盖「遍历时必须整棵跳过」的家目录判定。
//
// 关键点：~/Music 与 ~/Pictures 正是 2026-11-01 事故里真正弹窗的两类
// （kTCCServiceMediaLibrary / kTCCServicePhotos），必须在名单里。
func TestUnderHomePrivacyDir(t *testing.T) {
	home := testHome(t)

	protected := []string{
		"Music",
		"Pictures",
		"Library",
		"Movies",
		"Desktop",
		"Documents",
		"Downloads",
		"Applications",
		".Trash",
		"Pictures/Photos Library.photoslibrary",
		"Library/Application Support",
		"Music/iTunes",
	}
	for _, rel := range protected {
		abs := filepath.Join(home, filepath.FromSlash(rel))
		if !underHomePrivacyDir(abs) {
			t.Errorf("%s 应当被判为隐私目录", abs)
		}
	}

	notProtected := []string{
		".",                     // 家目录本身
		"MyProgram/GoProgram/x", // 普通开发目录
		"DocumentsX/foo",        // 前缀相似但不是同一目录
		"workspace/Music",       // 名字相同但不在家目录一级
	}
	for _, rel := range notProtected {
		abs := filepath.Join(home, filepath.FromSlash(rel))
		if underHomePrivacyDir(abs) {
			t.Errorf("%s 不应被判为隐私目录", abs)
		}
	}

	// 家目录之外一律不判。
	for _, abs := range []string{"/tmp/x", "/opt/homebrew/lib", "/"} {
		if underHomePrivacyDir(abs) {
			t.Errorf("%s 不在家目录内，不应被判为隐私目录", abs)
		}
	}
}

// TestUnderHomePrivacyDirIsCaseInsensitive：macOS 默认卷大小写不敏感。
func TestUnderHomePrivacyDirIsCaseInsensitive(t *testing.T) {
	home := testHome(t)
	for _, rel := range []string{"music", "PICTURES", "library"} {
		if !underHomePrivacyDir(filepath.Join(home, rel)) {
			t.Errorf("%s 应当被判为隐私目录（大小写不敏感）", rel)
		}
	}
}

// TestShouldSkipWalkDir 覆盖 Glob/Grep 遍历回调的跳过判定——这是「遍历不会走进
// 隐私目录」的实际开关，比计时测试更可靠（家目录本身很大，慢不等于卡死）。
func TestShouldSkipWalkDir(t *testing.T) {
	home := testHome(t)

	cases := []struct {
		name string
		root string
		p    string
		want bool
	}{
		{"根自身永不跳过", home, home, false},
		{"音乐库跳过", home, filepath.Join(home, "Music"), true},
		{"照片库跳过", home, filepath.Join(home, "Pictures"), true},
		{"Library 跳过", home, filepath.Join(home, "Library"), true},
		{"普通开发目录不跳过", home, filepath.Join(home, "MyProgram"), false},
		{"vendor 跳过", home, filepath.Join(home, "MyProgram", "vendor"), true},
		{"node_modules 跳过", home, filepath.Join(home, "MyProgram", "node_modules"), true},
		{"项目内的同名目录不跳过", "/tmp/proj", "/tmp/proj/Music", false},

		// 关键回归：项目自己就住在隐私目录里（~/Documents、~/Desktop、iCloud
		// 是常见选择）。root 被显式点名后，它的后代必须照常下潜，否则 Grep
		// 退化成「只搜顶层」且不提示结果不完整——静默给出错误答案。
		{"隐私目录内项目的子目录不跳过", filepath.Join(home, "Documents", "work", "app"), filepath.Join(home, "Documents", "work", "app", "src"), false},
		{"隐私目录内项目的深层目录不跳过", filepath.Join(home, "Desktop", "proj"), filepath.Join(home, "Desktop", "proj", "internal", "pkg"), false},
		{"隐私目录内项目仍跳过 vendor", filepath.Join(home, "Documents", "proj"), filepath.Join(home, "Documents", "proj", "vendor"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSkipWalkDir(tc.root, tc.p); got != tc.want {
				t.Fatalf("shouldSkipWalkDir(%q, %q) = %v, want %v", tc.root, tc.p, got, tc.want)
			}
		})
	}
}

// TestShouldSkipWalkDirDepthCap 验证层数上限：人为构造超过 maxWalkDepth 的路径。
func TestShouldSkipWalkDirDepthCap(t *testing.T) {
	root := "/tmp/proj"
	deep := root
	for i := 0; i <= maxWalkDepth+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if !shouldSkipWalkDir(root, deep) {
		t.Fatalf("超过 %d 层的目录应当被跳过: %s", maxWalkDepth, deep)
	}
	shallow := filepath.Join(root, "a", "b")
	if shouldSkipWalkDir(root, shallow) {
		t.Fatalf("浅层目录不应被跳过: %s", shallow)
	}
}
