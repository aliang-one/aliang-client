package services

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// mesh runbook 自愈安装测试(D-9):managed 纪律/内容寻址幂等/原子替换/
// 总闸尊重/源真值 frontmatter 可解析。home 与 content 走 ensureMeshRunbook
// InstalledFor 注入缝(t.TempDir 仿 home,绝不写真机 ~/.claude);顶层
// EnsureMeshRunbookInstalled 的进程周期语义经 meshRunbookHome 缝验证。

func meshRunbookTargetFor(home string) string {
	return filepath.Join(home, ".claude", "skills", meshRunbookSkillDirName, "SKILL.md")
}

// 内容寻址幂等:两次安装第二次零写入。以 os.Chtimes 把目标 mtime 钉到过去,
// 第二次安装后 mtime 不动 = 未写(比 mtime 粒度更硬的确定性证据)。
func TestMeshRunbookInstallerIdempotent(t *testing.T) {
	home := t.TempDir()
	contentA := []byte("---\nname: mesh-runbook\ndescription: probe\n---\nbody A\n")

	if got := ensureMeshRunbookInstalledFor(home, contentA); !got.Installed || got.Error != "" {
		t.Fatalf("首次安装应为 Installed=true err 空, got %+v", got)
	}
	target := meshRunbookTargetFor(home)
	pinned := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(target, pinned, pinned); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	got := ensureMeshRunbookInstalledFor(home, contentA)
	if got.Installed {
		t.Fatalf("内容未变时第二次安装应幂等跳写, got %+v", got)
	}
	if got.Skipped != "" || got.Error != "" {
		t.Fatalf("幂等跳写不是 skipped/error, got %+v", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if !info.ModTime().Equal(pinned) {
		t.Fatalf("第二次安装动了目标文件 mtime=%v, want 钉死 %v", info.ModTime(), pinned)
	}
	if cur, err := os.ReadFile(target); err != nil || string(cur) != string(contentA) {
		t.Fatalf("幂等后内容漂移 err=%v", err)
	}
}

// 内容寻址替换:embed 源真值变了(A→B)→ tmp+rename 整体替换,不留 tmp 残骸。
func TestMeshRunbookInstallerContentChangedReplaces(t *testing.T) {
	home := t.TempDir()
	contentA := []byte("---\nname: mesh-runbook\ndescription: probe\n---\nbody A\n")
	contentB := []byte("---\nname: mesh-runbook\ndescription: probe\n---\nbody B longer\n")

	if got := ensureMeshRunbookInstalledFor(home, contentA); !got.Installed {
		t.Fatalf("首次安装应落盘, got %+v", got)
	}
	got := ensureMeshRunbookInstalledFor(home, contentB)
	if !got.Installed || got.Error != "" {
		t.Fatalf("内容变后应整体替换, got %+v", got)
	}
	cur, err := os.ReadFile(meshRunbookTargetFor(home))
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(cur) != string(contentB) {
		t.Fatalf("替换后内容非新版, got %q", cur)
	}
	dir := filepath.Dir(meshRunbookTargetFor(home))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("替换后残留 tmp 文件 %s", e.Name())
		}
	}
}

// 总闸关:跳过不写,也不覆盖已装的旧文件(关闸=不动手,不是清场)。
func TestMeshRunbookInstallerGateOffSkips(t *testing.T) {
	fals := false
	old := config.GetGlobalConfig()
	t.Cleanup(func() { config.SetGlobalConfig(old) })
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		AgentCore: &config.AgentCoreTuningConfig{SkillsEnabled: &fals},
	}})

	home := t.TempDir()
	got := ensureMeshRunbookInstalledFor(home, []byte("never-landed"))
	if got.Skipped == "" || got.Installed || got.Error != "" {
		t.Fatalf("总闸关应 skipped 跳过, got %+v", got)
	}
	if _, err := os.Stat(meshRunbookTargetFor(home)); !os.IsNotExist(err) {
		t.Fatalf("总闸关时不应落盘, stat err=%v", err)
	}

	// 已有安装文件的场景:关闸跳过绝不改写。
	target := meshRunbookTargetFor(home)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := []byte("---\nname: mesh-runbook\n---\nuser-edited-or-old\n")
	if err := os.WriteFile(target, existing, 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	got = ensureMeshRunbookInstalledFor(home, []byte("never-landed"))
	if got.Skipped == "" || got.Installed {
		t.Fatalf("关闸+已有文件仍应跳过, got %+v", got)
	}
	cur, err := os.ReadFile(target)
	if err != nil || string(cur) != string(existing) {
		t.Fatalf("关闸跳过不得改写已装文件 err=%v", err)
	}
}

// home 缺失:跳过且绝不落盘(Join("",..) 会写进进程 CWD 的同款纪律)。
func TestMeshRunbookInstallerHomeUnavailableSkips(t *testing.T) {
	got := ensureMeshRunbookInstalledFor("", []byte("x"))
	if got.Skipped == "" || got.Installed || got.Error != "" {
		t.Fatalf("home 缺失应 skipped 跳过, got %+v", got)
	}
}

// managed 纪律:只碰 mesh-runbook 名下路径,用户自装技能零触碰。
func TestMeshRunbookInstallerManagedScope(t *testing.T) {
	home := t.TempDir()
	userSkill := filepath.Join(home, ".claude", "skills", "user-own", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userBody := []byte("user installed skill body")
	if err := os.WriteFile(userSkill, userBody, 0o644); err != nil {
		t.Fatalf("seed user skill: %v", err)
	}

	if got := ensureMeshRunbookInstalledFor(home, []byte("mesh body")); !got.Installed {
		t.Fatalf("安装应落盘, got %+v", got)
	}
	cur, err := os.ReadFile(userSkill)
	if err != nil || string(cur) != string(userBody) {
		t.Fatalf("用户自装技能被触碰 err=%v", err)
	}
}

// 进程周期语义:总闸关 → 跳过不落盘;开闸后下次安装调用即补齐落盘。
// 走顶层 EnsureMeshRunbookInstalled + meshRunbookHome 缝,验证 embed 源真值
// → 磁盘的整条生产路径。
func TestMeshRunbookInstallerEnablesNextInstall(t *testing.T) {
	fals := false
	oldCfg := config.GetGlobalConfig()
	t.Cleanup(func() { config.SetGlobalConfig(oldCfg) })
	config.SetGlobalConfig(&config.Config{Customer: &config.CustomerConfig{
		AgentCore: &config.AgentCoreTuningConfig{SkillsEnabled: &fals},
	}})
	oldHome := meshRunbookHome
	home := t.TempDir()
	t.Cleanup(func() { meshRunbookHome = oldHome })
	meshRunbookHome = func() string { return home }

	if got := EnsureMeshRunbookInstalled(); got.Skipped == "" || got.Installed {
		t.Fatalf("关闸期顶层调用应跳过, got %+v", got)
	}
	if _, err := os.Stat(meshRunbookTargetFor(home)); !os.IsNotExist(err) {
		t.Fatalf("关闸期不应落盘, stat err=%v", err)
	}

	// 开闸(模拟仪表盘打开总闸后 agent 重启/下一进程周期)→ 下次调用落盘。
	config.SetGlobalConfig(&config.Config{})
	got := EnsureMeshRunbookInstalled()
	if !got.Installed || got.Skipped != "" || got.Error != "" {
		t.Fatalf("开闸后下次安装应落盘, got %+v", got)
	}
	embedded, err := MeshRunbookSkillContent()
	if err != nil {
		t.Fatalf("MeshRunbookSkillContent: %v", err)
	}
	cur, err := os.ReadFile(meshRunbookTargetFor(home))
	if err != nil || string(cur) != string(embedded) {
		t.Fatalf("落盘内容应等于 embed 源真值 err=%v", err)
	}

	// 再跑一次:内容寻址幂等(二进制反复重启零写放大)。
	if got := EnsureMeshRunbookInstalled(); got.Installed || got.Skipped != "" || got.Error != "" {
		t.Fatalf("同内容再装应幂等零写, got %+v", got)
	}
}

// 源真值 frontmatter 可解析:embed 内容经 parseSlashFrontmatter,name/
// description 落在字段域内(注入清单块靠 description 触发,空=技能不可发现)。
func TestMeshRunbookSKILLFrontmatterParsable(t *testing.T) {
	embedded, err := MeshRunbookSkillContent()
	if err != nil {
		t.Fatalf("MeshRunbookSkillContent: %v", err)
	}
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, embedded, 0o644); err != nil {
		t.Fatalf("write temp SKILL.md: %v", err)
	}
	fm, ok := parseSlashFrontmatter(path)
	if !ok {
		t.Fatalf("embed SKILL.md 不可读")
	}
	if fm.name != meshRunbookSkillDirName {
		t.Fatalf("frontmatter name=%q, want %q", fm.name, meshRunbookSkillDirName)
	}
	if len(fm.description) < 20 {
		t.Fatalf("frontmatter description 过短(%d 字节),触发清单块会不可发现", len(fm.description))
	}
	// 渐进披露契约:不设 disable-model-invocation(默认 model-invocable)。
	if !fm.isModelInvocable() {
		t.Fatalf("runbook 应 model-invocable(渐进披露依赖模型按需 Read)")
	}
}

// 端点/字段 drift 护栏(D-11 验收口径「runbook 引用端点齐备断言」):
// embed SKILL.md 引用的每个 /api 路径必须在 core mux(routes.go)注册;mesh
// 家族必须同时在 56433 mux(server.go)有 forward 注册(双注册纪律);承重
// 字段 token 必须仍以 json tag 形态活在 handler 源码里。runbook 是给人+AI
// 读的操作面,端点/字段漂移 = AI 照着打 404/读不到键,漂移在构建期红。
func TestMeshRunbookEndpointDrift(t *testing.T) {
	embedded, err := MeshRunbookSkillContent()
	if err != nil {
		t.Fatalf("MeshRunbookSkillContent: %v", err)
	}
	coreRoutes := registeredRoutesForMeshRunbook(t, "../routes/routes.go", `register\("([^"]+)"`)
	agentRoutes := registeredRoutesForMeshRunbook(t, "../../agentruntime/server.go", `mux\.HandleFunc\("([^"]+)"`)

	seen := map[string]bool{}
	var paths []string
	for _, m := range regexp.MustCompile(`/api/[a-z_]+(?:/[a-z_]+)*`).FindAllString(string(embedded), -1) {
		if !seen[m] {
			seen[m] = true
			paths = append(paths, m)
		}
	}
	if len(paths) == 0 {
		t.Fatalf("SKILL.md 未提取到任何 /api 路径,提取正则失效")
	}
	for _, p := range paths {
		if !meshRunbookRouteCovered(coreRoutes, p) {
			t.Errorf("SKILL.md 引用 %s 但 core mux(routes.go)未注册", p)
		}
		if strings.HasPrefix(p, "/api/mesh/") && !meshRunbookRouteCovered(agentRoutes, p) {
			t.Errorf("mesh 端点 %s 未在 56433 mux(server.go)forward 注册(双注册纪律)", p)
		}
	}
	// 反向护栏:runbook 明文宣称「/api/logs 只在 56431,56433 无日志 HTTP 面」。
	if agentRoutes["/api/logs"] {
		t.Errorf("56433 mux 出现 /api/logs 注册,与 runbook「56433 无日志面」宣称漂移")
	}

	// 承重字段 token:仍以 json tag 形态活在 handler 源码(handlers 全目录 +
	// 载体分桶 sshexit.go)。
	var sb strings.Builder
	handlerFiles, _ := filepath.Glob("../handlers/*.go")
	if len(handlerFiles) == 0 {
		t.Fatalf("handlers 目录 glob 为空,相对路径失效")
	}
	for _, f := range handlerFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		sb.Write(b)
	}
	exit, err := os.ReadFile("../../../outbound/proxy/mesh/sshexit.go")
	if err != nil {
		t.Fatalf("read sshexit.go: %v", err)
	}
	sb.Write(exit)
	src := sb.String()
	for _, token := range []string{"beacon_running", "roster_sync", "mismatches", "side_b", "busy", "dial_fail"} {
		if !strings.Contains(src, `"`+token+`"`) {
			t.Errorf("承重字段 token %q 已不在 handler json tag 里,runbook 读法失效", token)
		}
	}
}

// registeredRoutesForMeshRunbook 从源码按正则抽路由路径,剥 Go 1.22 方法前缀。
func registeredRoutesForMeshRunbook(t *testing.T, relPath, pattern string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("read %s: %v", relPath, err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(string(raw), -1) {
		route := strings.TrimSpace(m[1])
		for _, method := range []string{"GET ", "POST ", "PUT ", "DELETE "} {
			route = strings.TrimSpace(strings.TrimPrefix(route, method))
		}
		out[route] = true
	}
	return out
}

// meshRunbookRouteCovered 精确命中;`/api/mesh` 裸前缀(runbook 的
// `/api/mesh/*` 家族记法)按「该前缀家族至少注册一条」覆盖判定。
func meshRunbookRouteCovered(routes map[string]bool, path string) bool {
	if routes[path] {
		return true
	}
	if path == "/api/mesh" {
		for r := range routes {
			if strings.HasPrefix(r, "/api/mesh/") {
				return true
			}
		}
	}
	return false
}
