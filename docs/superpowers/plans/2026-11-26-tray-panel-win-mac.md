# 托盘单形态面板（Windows + macOS）实现计划 v3

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **仓库铁律**：提交须用户明确同意。Commit 步骤在用户批准对应批次后执行（`git commit -- <路径>` 路径限定，防并行会话暂存混包）。

**Goal:** 分两个里程碑把 Windows/macOS 的托盘升级为样式 spec 定稿的单形态自绘面板。**里程碑 1**：托盘点击回调 + `/api/tray/summary` 数据面 + 面板前端（点图标开浏览器 `/panel.html`，先让面板 UI 落地）。**里程碑 2**：`internal/panelwin` 纯原生窗口壳（NSPanel+WKWebView / WS_POPUP+WebView2）+ launcher 状态机 + 兜底，同一份前端原地升级为贴图标弹出。Linux 完全不动；不换托盘库；不引入 UI 框架（用户拍板：纯原生，不用 Wails）。

**Architecture:** 托盘层保留内嵌 getlantern/systray v1.2.2 + 本仓补丁（点击回调 + 图标矩形）。面板页由 core 回环仪表盘伺服 `/panel.html`，数据走同源 `/api`。里程碑 1 的宿主动作 = 开浏览器（复用既有 openDashboard 模式）；里程碑 2 换成 `internal/panelwin` 独立辅助进程（同一 aliang 二进制的 `panel` 子命令，点击才拉起、失焦即退、崩溃自动重拉、连续失败接回原生菜单），宿主通信仅两个自定义 WebMessage（`quit` → exit 60 / `open` → 浏览器）。模式切换运行中禁用（不复刻 SwitchMode 停而不起）。

**Tech Stack:** Go 1.26 + 内嵌 systray（保留）；M2 增：macOS cgo ObjC（NSPanel + WKWebView，沿用 autoreleasepool 纪律）、Windows jchv/go-webview2 的 **pkg/edge.Chromium**（底层组件，见 Task 13 注意）+ x/sys/windows；Vue 3 + Vite 多页入口（panel.html，纯 .js 测试）；vitest。

**规格来源：** `docs/superpowers/specs/2026-11-26-tray-panel-style-design.md`（§3/§4/§5/§6/§9）、`tray-menu-style-mockups.html`（数值与 SVG）、`2026-11-26-tray-replacement-research.md`（§4.2/§4.3/§6.5/§6.7——**该文档仅在主检出目录，Task 0 先复制进 worktree**）。

**已拍板决策（用户 2026-11-26）：**
1. 面板崩溃兜底（M2）= 自动重拉 + 连续失败接回原生菜单。
2. 模式切换 = 运行中禁用；停止态切换只影响下次启动。
3. Linux 不做、库不换；窗口壳纯原生不引 Wails。
4. 交付节奏 = 两步走：先浏览器版（M1），后加壳（M2）；前端两阶段完全复用。

**工作分支：** `fix/systray-darwin-autorelease-leak`（基点 e3b0dceb）。worktree：`/Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/fix-systray-darwin-autorelease-leak`。

---

## 文件结构

```
third_party/systray/
  systray.go                改：panelClickFn/SetPanelClickHook 公共层（本仓补丁）
  systray_windows.go        改：wndProc 回调分支 / TrayRect(NOTIFYICONIDENTIFIER 两参) / WM_NULL+TPM_RIGHTBUTTON
  systray_darwin.m/.go      改：面板模式 set_panel_mode / attach/detach / tray_rect / trayClicked:
  systray_nonwindows.go     改：//export systray_tray_clicked（仓内 export 惯例位置）
  systray.h / PATCH_NOTES.md 改
internal/ipc/client.go      改：互斥 + 响应 ID + 可注入 deadline
app/http/services/
  tray_summary.go(+_test)      GET /api/tray/summary
  tray_endpoint.go(+_test)     POST /api/tray/endpoint/activate
app/tray/
  panel_bridge.go(+_test)      点击接线：M1=开浏览器；M2=spawn 面板进程/兜底
  companion.go / companion_windows.go  改：onReady 挂接、M2 菜单「打开面板」恢复项、退出码 60 分派
internal/panelwin/            【仅 M2】新包（windows/darwin 标签）
  panelwin.go(+_test)          锚定换算纯函数 + Host 接口
  panelwin_darwin.go/.m        NSPanel + NSVisualEffectView 材质 + WKWebView 透明底
  panelwin_windows.go          WS_POPUP + pkg/edge.Chromium + DWM 圆角/亚克力 + 自含 DPI 声明
cmd/
  panel.go                     【仅 M2】panel 子命令（windows/darwin 标签）
  linux_closure_test.go        Linux 依赖闭包回归门
app/website/
  panel.html + src/panel/      面板页（main.js/api.js/summary.js/state.js/components/*，纯 .js 测试）
.github/workflows/tag-build-release.yml  改：matrix job 按 goos 插 go test 闸
```

职责边界：companion（点击→M1 开浏览器 / M2 拉起面板进程；失败计数；兜底；退出码 60→既有 quit 闸）｜panel 进程【M2】（窗口、锚定、失焦退、消息→退出码/开浏览器）｜面板页（渲染 + /api）｜core（两个新端点）。

**通信协议（面板页 → 宿主）**：页面 `postMessageHost` 发 JSON `{"type":"quit"}` / `{"type":"open","kind":"dashboard"}`。M1 浏览器无宿主 → `postMessageHost` 返回 false，页面按「无宿主模式」降级（退出键隐藏，见 Task 11）。M2 宿主处理：quit → exit 60；open → 按 kind 取对应 URL 开默认浏览器（v1 两 kind 均传仪表盘根 URL，评审次#8）。**零桥接框架、零自动注入运行时；消息 handler 名固定 `"panel"`（Task 13 结构测试断言）。**

---

# 里程碑 1（浏览器版面板）

### Task 0: 分支基线（合并 master + 复制调研文档）与守卫

**Files:** `cmd/linux_closure_test.go`（新）；`docs/superpowers/specs/2026-11-26-tray-replacement-research.md`（复制进 worktree）

- [ ] **Step 1: 合并 master + 前置闸（评审 B1）**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate/.claude/worktrees/fix-systray-darwin-autorelease-leak
git status --short            # 期望空
git merge master -m "合并 master——接入 LLM endpoint manager 等批,面板数据面依赖"
git merge-base --is-ancestor ada0deda HEAD && echo ENDPOINT_BATCH_OK
grep -rn "protocol_anthropic" app/http/services/ | head -3   # 期望非空
cp /Users/mac/MyProgram/GoProgram/nursor/alianggate/docs/superpowers/specs/2026-11-26-tray-replacement-research.md docs/superpowers/specs/
```

Expected: ENDPOINT_BATCH_OK + grep 非空。**Task 5/6/10 的前置闸：不成立不得继续。**（调研文档复制后随 Task 15 批次一并提交，主检出目录的原件不动。）

- [ ] **Step 2: Linux 依赖闭包回归门**

Create `cmd/linux_closure_test.go`：

```go
//go:build linux

package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

// 面板窗口壳与其 WebView 依赖只允许进入 windows/darwin 构建。Linux 服务器
// 二进制的依赖闭包回归门（调研 §6.3 不变量；M2 落地 internal/panelwin 后
// 此门自动生效）。
func TestLinuxDepsExcludeGUI(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "./cmd/aliang").CombinedOutput()
	if err != nil {
		t.Skipf("go list failed (environment): %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "wailsapp") || strings.Contains(line, "go-webview2") || strings.Contains(line, "panelwin") {
			t.Fatalf("Linux 依赖闭包出现 GUI 依赖: %s", line)
		}
	}
}
```

- [ ] **Step 3: 基线全绿** `go test ./app/tray/ ./internal/ipc/ ./internal/singleinstance/ -count=1`

---

### Task 1: IPC 客户端加固（互斥 + 响应 ID + 可注入 deadline）

**Files:** Modify `internal/ipc/client.go`；Test 追加 `internal/ipc/ipc_test.go`

- [ ] **Step 1: 读现状** `sed -n '1,180p' internal/ipc/client.go` + `grep -n "NewClientWithTransport" internal/ipc/ipc_test.go`
- [ ] **Step 2: 三个失败测试**：`TestClientConcurrentSendsAreIsolated`（并发各得正确响应）；`TestClientSendTimesOutWhenServerHangs`（服务端不回包，Send 在**注入的 300ms deadline** 内报错——Client 增可注入 `readTimeout`，默认维持 `requestTimeout`）；`TestClientSkipsMismatchedResponseID`（错帧后到达对帧，Send 必须等到对帧）。
- [ ] **Step 3: 红** `go test ./internal/ipc/ -run 'TestClient(Concurrent|SendTimesOut|SkipsMismatched)' -count=1 -v`
- [ ] **Step 4: 最小实现**：`sendMu sync.Mutex` 全程持锁；读/写前 `SetDeadline(now+readTimeout)`；按 `resp.ID == req.ID` 过滤。退避/重拨一行不动。
- [ ] **Step 5: 绿 + 全包回归** `go test ./internal/ipc/ -count=1`
- [ ] **Step 6: Commit**（`修复:IPC 客户端请求级互斥+响应 ID 校验+可注入读超时`）

---

### Task 2: CI 测试闸

**Files:** Modify `.github/workflows/tag-build-release.yml`

构建是**单个 matrix job**（5 entry：ubuntu-24.04 / ubuntu-24.04-arm / windows-2022 / macos-15-intel / macos-15）。build 步骤前按 `matrix.goos` 条件插：linux → `go test ./internal/ipc/... ./internal/singleinstance/... ./cmd/...`；windows/darwin → 加 `./app/tray/...`。

- [ ] 本地等价（darwin）：`go test ./app/tray/ ./internal/ipc/ ./internal/singleinstance/ ./cmd/ -count=1`
- [ ] Commit（`新增:发布 CI 按平台插 go test 闸（DEBT-07 首步）`）

---

### Task 3: Windows 底座补丁——点击回调 + 图标矩形 + 弹菜单小修

**Files:**
- Modify: `third_party/systray/systray.go`（公共层 hook）、`systray_windows.go`、`PATCH_NOTES.md`
- Test: `app/tray/systray_windows_patch_test.go`（新，结构测试）

- [ ] **Step 1: 失败的结构测试**，断言六点：
1. `systray.go` 公共层存在 `var panelClickFn func()` 与 `func SetPanelClickHook(fn func())`。
2. wndProc `WM_LBUTTONUP`/`WM_RBUTTONUP` 分支有 `if panelClickFn != nil` → `go panelClickFn()`。
3. 存在 `notifyIconIdentifier` 结构体（`cbSize/hWnd/uID/guidItem`）与 `pShell_NotifyIconGetRect` proc。
4. **`TrayRect` 内 cbSize 用 `uint32(unsafe.Sizeof(notifyIconIdentifier{}))` 表达式（评审二审阻塞 1：该结构 x64 sizeof=40，字面常量 24 会让 API 恒 E_INVALIDARG 且被兜底静默掩盖；测试断言表达式出现、禁止字面常量）。**
5. `TrayRect` 调用为**两参形态** `Call(uintptr(unsafe.Pointer(&idf)), uintptr(unsafe.Pointer(&r)))` 且只认 `ret == 0`（S_OK）成功（真实签名 `HRESULT Shell_NotifyIconGetRect(const NOTIFYICONIDENTIFIER*, LPRECT*)`，与 NOTIFYICONDATAW 是两个结构体）。
6. `showMenu` 末尾 `PostMessageW(wnd, WM_NULL, 0, 0)` 且 flags 含 `TPM_RIGHTBUTTON`。

- [ ] **Step 2: 红** `go test ./app/tray/ -run TestSystrayWindowsPanelPatch -count=1`
- [ ] **Step 3: 实现**（全部 `本仓补丁` 锚点）：公共层 hook；wndProc 分支；identifier + 两参 Call；showMenu 补 WM_NULL + TPM_RIGHTBUTTON。溢出区两态（Win10 S_FALSE+箭头矩形 / Win11 屏外坐标）原样透传，**由 Task 14 接线统一判废**。
- [ ] **Step 4: 门**

```bash
go test ./app/tray/ -run TestSystrayWindowsPanelPatch -count=1
GOOS=windows GOARCH=amd64 go build github.com/getlantern/systray
go build ./app/tray/
```

- [ ] **Step 5: PATCH_NOTES 登记**；**Step 6: Commit**（`新增:Windows 托盘底座面板胶水——点击回调/图标矩形/弹菜单小修`）

---

### Task 4: macOS 底座补丁——面板模式 / 兜底接回 / 图标矩形

**Files:**
- Modify: `third_party/systray/systray_darwin.m`、`systray_darwin.go`、`systray_nonwindows.go`、`systray.h`、`PATCH_NOTES.md`
- Test: `app/tray/systray_darwin_patch_test.go`（追加）

机制：panelMode=true → `statusItem.menu=nil` + `button.target/action=@selector(trayClicked:)` + `sendActionOn: 左|右 mouseDown`（→ `systray_tray_clicked()`）；false → action 摘除 + `setMenu:` 接回。菜单项对象与刷新循环不动（面板模式期间照常刷新，随时无损接回）。

- [ ] **Step 1: 失败的结构测试**：①`goroutineCallableDarwinEntries` 增 `set_panel_mode`、`tray_rect`；②`.m` 含 `trayClicked:`、`sendActionOn`、detach `setMenu:nil`、attach 路径、`NSScreen` 左下→左上坐标换算；③export 三处分工按仓内惯例——`systray_nonwindows.go` 含 `//export systray_tray_clicked`、`systray.h` 含 extern、`.m` 含调用（各一断言）；④`systray_darwin.go` 有 `SetPanelMode(bool)`、`TrayRect()` 包装。
- [ ] **Step 2: 红** `go test ./app/tray/ -run TestVendoredSystrayDarwin -count=1`
- [ ] **Step 3: 实现**（新入口全部 `@autoreleasepool { // 本仓补丁 }`；tray_rect 经 `NSScreen.screens.firstObject.frame.size.height` 换算为全局左上原点）
- [ ] **Step 4: 门**

```bash
go test ./app/tray/ -run TestVendoredSystrayDarwin -count=1
go build github.com/getlantern/systray
GOOS=windows GOARCH=amd64 go build github.com/getlantern/systray
```

- [ ] **Step 5: PATCH_NOTES 登记**；**Step 6: Commit**（`新增:macOS 托盘底座面板胶水——面板模式/兜底接回/图标矩形`）
- [ ] **Step 7: ⚠️ 真机先验（硬闸，不绿不进 Task 12+）**：临时挂 SetPanelMode(true) 手测点击回调、SetPanelMode(false) 后原生菜单完整可用；macOS 27 机器加验右键与菜单接回（调研 §4.2 高亮/occlusionState/右键回归区）。结果写进任务备注。

---

### Task 5: GET /api/tray/summary 聚合端点

**Files:** Create `app/http/services/tray_summary.go` + `tray_summary_test.go`；Modify 路由注册（`grep -rn "api/run/status" app/http/` 照现有模式）

契约（唯一读通道；子源缺失→字段 null；handler 每子源 recover）：

```json
{
  "version": "v1.1.127-local",
  "run": { "is_running": true, "mode": "http", "status_desc": "...",
           "listen_addr": "127.0.0.1:56432",
           "blocked": { "code": "", "msg": "" } },
  "ai":  { "enabled": true,
           "providers": [ { "key": "anthropic", "label": "Claude", "detected": true,
                            "detail": "检测到 2 个会话经此路由" } ] },
  "agents": { "kernels": [ { "name": "claude", "active": 1, "idle": 2, "total_24h": 6,
              "sessions": [ { "title": "...", "source": "网页", "active": false, "ts": 1790000000 } ] } ] },
  "endpoints": { "available": true,
                 "groups": [ { "protocol": "anthropic", "current": "glm-coding-plan",
                   "items": [ { "name": "glm-coding-plan", "rank": 1, "state": "ok", "cooldown_remaining_sec": 0 } ] } ] }
}
```

数据源映射（实现前 grep 定位，禁止凭空造名）：run=进程内 run service（与 `/api/run/status` 同源）+ `ProxyStartBlockedReason` + 配置端口得 listen_addr；ai=`app/tray/ai_status.go` 同源 tracker + AI 开关存储（grep `ai_rules` 定位）；agents=复用仪表盘 agent 会话查询按内核聚合 24h；endpoints=endpoint-manager service（`endpoint_service.go`，Task 0 闸保证在分支）按 spec §4.4 映射——组=protocol toggle、`priority` 升序→rank（`auto_group=0`/未设→rank=0「—」）、`health_status=cooldown|disabled`→state、`enabled=0`→**不进 items**、registry 不可用→`available:false`。

- [ ] 失败测试（JSON 形状、cooldown→state+剩余秒>0、enabled=0 剔除、available=false、rank 升序）→ 红 → 实现（薄壳+recover）→ 绿 → 冒烟（`npm run build` 重建 → 重启 core → `curl -s http://127.0.0.1:56431/api/tray/summary`）
- [ ] Commit（`新增:/api/tray/summary 聚合端点——面板唯一读通道`）

---

### Task 6: POST /api/tray/endpoint/activate

**Files:** Create `app/http/services/tray_endpoint.go` + `_test.go`

契约：body `{"protocol":"anthropic","name":"..."}` → **薄委托** endpoint-service 现有激活语义（`grep -n "activate\|set-current" app/http/services/endpoint_service.go app/http/handlers/endpoint_handler.go`）；错误 400（cooldown/disabled）/404（组/名不存在）/503（registry 不可用），形状 `{"code":..,"msg":..}`。

- [ ] 失败测试（委托 1 次、cooldown 400、未知名 404、registry nil 503）→ 红 → 实现 → 绿 → Commit（`新增:/api/tray/endpoint/activate——托盘单选即激活(委托 registry)`）

---

### Task 7: 面板前端骨架（Vite 多页入口 + 数据层 + toast）

**Files:** Create `app/website/panel.html`、`src/panel/main.js`、`api.js`、`summary.js`、`state.js`、`App.vue`、`theme.css`、`components/Toast.vue`；Modify `vite.config.js`（input 增 panel.html）、`scripts/check-dist.mjs`（panel.html 校验，复制 index.html 分支）

要点：独立小入口；色板/几何抄 mockup；深浅色跟 `prefers-color-scheme`；**纯 .js**（工程无 TS，vitest include=`src/**/*.test.js`）。

- [ ] vite.config 加入口 → build 出 `dist/panel.html`
- [ ] `api.js`：`getSummary()`（同源 fetch `/api/tray/summary`，1.5s 超时）、`startProxy()/stopProxy()/restartProxy()`（路由名先 `grep -rn "api/run" app/http/` 核对）、`activateEndpoint()`、`postMessageHost(msg)`（`window.chrome.webview?.postMessage ?? window.webkit?.messageHandlers.panel?.postMessage`；双后端皆缺 → 返回 false = 无宿主模式）
- [ ] `summary.js`：容错解析 + `useSummary()` 2s 轮询（连续 2 次失败→connLost 错误态，恢复回绿）
- [ ] `Toast.vue` + `useToast()`（后续三组件共用）
- [ ] vitest：summary 容错/计数/恢复；toast 队列与超时；**postMessageHost 无宿主返回 false**
- [ ] build + check-dist 绿；Commit（`新增:托盘面板前端骨架——独立入口+数据层+toast`）

---

### Task 8: 状态头 + 传输行

**Files:** `components/StatusHeader.vue`、`TransportRow.vue`、`state.js`；Test: `__tests__/state.test.js`、`TransportRow.test.js`

`state.js`（纯函数）：summary+本地过渡态 → 七态 `running|stopped|starting|stopping|restarting|error|logged_out`（过渡态优先；blocked.code→logged_out；null/connLost→error）+ `allControlsDisabled(kind)`。

TransportRow（spec §4.2）：启停合一（stopped 绿▶ / running 红⏹ / 过渡琥珀 spinner+文案、三控件 `.locked` 防抖）；重启 38×38 仅 running；**模式 segmented：running/过渡一律 disabled（opacity .45 + pointer-events none + title「停止后可切换」——拍板 2）**；启停点击走 api.js + 本地过渡态。

vitest 必测：running 时 seg disabled 且点击无效；stopped→点启动立即 starting→summary 回 running 解除；error 态启动键可用；未登录三键禁用。数值抄 mockup `.transport-row/.t-btn/.seg`。

- [ ] 红→实现→绿→Commit（`新增:面板状态头+传输行——启停合一/七态/运行中禁用模式切换`）

---

### Task 9: AI 加速区

**Files:** `components/AISection.vue`；Test: `AISection.test.js`

spec §4.3：⚡+开关（32×19）+ provider 行（Anthropic/OpenAI/Cursor 可检测 + Gemini/Grok 预留）；detected→点亮（绿描边 inset + 9px 绿点弹性）；行尾「检测到 N / M」（M=3）；不可手点点亮（点击 toast `detail`，空则「暂无详情」）；AI 关 → 行 `.dimmed` + 文案「AI 加速已关 · 图标全灰」。开关写入：定位不到现成 REST 则 v1 只读展示 + toast「请到仪表盘开关」（代码注释标注）。

- [ ] 同节奏；Commit（`新增:面板 AI 加速区——检测驱动点亮+关态降透明`）

---

### Task 10: ENDPOINT 区

**Files:** `components/EndpointZone.vue`；Test: `EndpointZone.test.js`

spec §4.4：`available=false`/字段缺失 → **整区不渲染**（v-if 无空壳）；组头展开收起（max-height .25s）；radio 单选即激活（乐观勾移+toast「组 → X」+失败回滚）；cooldown → 置灰 + 琥珀「冷却 Xm」（= `cooldown_remaining_sec/60` 向上取整）不可选；disabled →「已摘除」；rank=0 →「—」。**`ai.enabled=false` 时本区同样 `.dimmed` + 锁（二审 I6）。**

vitest 必测：available=false 不渲染；cooldown 点击不发请求且显示分钟数；激活成功勾移/失败回滚；ai.enabled=false 整区 dimmed 且点击被锁。

- [ ] 同节奏；Commit（`新增:面板 ENDPOINT 区——单选即激活+降级+AI 关联动`）

---

### Task 11: AI AGENT 区 + 底部动作行（含无宿主降级）

**Files:** `components/AgentZone.vue`、`FooterBar.vue`；Test: 各一

Agent 区（§4.5）：标题「AI AGENT · 24H」；每内核一卡（🤖+名+右箭头），芯片「活跃 N（绿）/空闲 N/24h 共 N」，活跃 0 不显活跃芯片；点卡展开明细（状态点+标题+「来源 · 相对时间」纯函数）。底部（§4.6）：左下版本号；右三键仪表盘/设备互访/退出，退出 hover 红 + 确认弹层。**M1 无宿主降级（评审次#8 + 两步走）：`postMessageHost` 返回 false 时（浏览器模式）三键退化为「打开仪表盘根页」，退出键隐藏并显示 muted 说明「浏览器面板暂不支持退出」——M2 宿主模式下恢复完整三键。**错误行跳日志深链 v1 豁免（开仪表盘根页）。

- [ ] 同节奏；vitest 覆盖无宿主模式三键退化；Commit（`新增:面板 AGENT 区+底部动作行——含浏览器模式降级`）

---

### Task 12: M1 companion 接线——点击开浏览器 + M1 验收

**Files:**
- Create: `app/tray/panel_bridge.go` + `panel_bridge_test.go`（windows/darwin）
- Modify: `app/tray/companion.go`、`companion_windows.go`（onReady 挂 hook）

- [ ] **Step 1: 失败测试**：hook 安装后点击回调 → 调用「打开浏览器」（复用既有 openDashboard 的系统调用模式，URL=`http://127.0.0.1:<真实端口>/panel.html`）；`SetPanelClickHook(nil)`/`SetPanelMode(false)` 恢复菜单路径。
- [ ] **Step 2: 红** → **Step 3: 实现**：onReady 末尾 `SetPanelClickHook(openPanelInBrowser)` + macOS `SetPanelMode(true)`；openPanelInBrowser 复用既有「系统默认浏览器打开 URL」代码（TrayApp/companion 现成模式）。M1 无 launcher/兜底（浏览器不崩，拍板 4）。
- [ ] **Step 4: 绿**
- [ ] **Step 5: M1 手工验收（本机 macOS）**：点图标 → 浏览器弹面板页；面板启停/模式/endpoint/agent 各区联调真实 core；浏览器关闭重开状态正确；托盘图标无泄漏（`footprint` 对照）
- [ ] **Step 6: Commit**（`新增:M1 托盘面板——点击开浏览器 + 前端全量上线`）

---

# 里程碑 2（原生壳升级）

> **M2 开工前的技术复核点（2026-11-26 讨论）**：窗口壳实现技术在 M1 上线后可重估一次——若届时想换 Wails v3，先做一次 30 分钟实验（给 aliang 链上 Wails 跑 core 服务，验证其包级 init 副作用无害）：实验通过 → 允许 Wails 子命令版壳；不过 → 维持本计划的纯原生壳。无论哪种，`internal/panelwin` 的 Host 接口不变，M1 的全部产出不受影响。

### Task 13: internal/panelwin——纯原生窗口壳

**Files:**
- Create: `internal/panelwin/panelwin.go`（锚定纯函数 + Host 接口）、`panelwin_test.go`、`panelwin_darwin.go` + `panelwin_darwin.m`、`panelwin_windows.go`、`cmd/panel.go`
- Test: `internal/panelwin/panelwin_test.go`（锚定表驱动）+ `internal/panelwin/panelwin_darwin_struct_test.go`（.m 结构断言）
- 依赖：**本任务 Step 1 才执行 `go get github.com/jchv/go-webview2@latest`**（钉 pseudo-version；Task 0 不预装，避免死依赖被 `go mod tidy` 清掉——二审次#11）

行为（`aliang panel --url URL --ax X --ay Y --w 340 --h 560 --side top|bottom --open-dashboard URL --open-peer URL`）：
- 锚定纯函数 `anchorToWindowPos(ax,ay,w,h,side,panelW,panelH)`：side=bottom → `y=ay-panelH-8`；side=top → `y=ay+h+4`；x 居中并钳边距 8。表驱动测试。
- **Windows（panelwin_windows.go）**：①**自含 DPI 声明**（PerMonitorV2 `SetProcessDpiAwarenessContext` 十几行直接写在包内——`app/tray` 的同名函数未导出且 internal 不得反向依赖 app 包，二审阻塞 2）；②自建 `WS_POPUP` + `WS_EX_TOOLWINDOW|WS_EX_TOPMOST`；③`DWMWA_WINDOW_CORNER_PREFERENCE=ROUND` + `DWMSBT_TRANSIENTWINDOW`（22621+，低版本静默降级）；④WebView2 **必须用 `github.com/jchv/go-webview2/pkg/edge` 的 `Chromium`**：`Embed(自建HWND)` + `MessageCallback` 收 `{"type":...}` + 自建消息循环——**顶层 `WebView` 包装不可用**（其 Bind/JSON-RPC 会拦截 postMessage、无 WebMessageReceived 钩子，quit/open 链会静默断裂，二审重要 4）；⑤**显示前 `SetForegroundWindow(hwnd)`**（`AllowSetForegroundWindow` 只授予资格，不调则 `WM_ACTIVATE` 失活永不触发、失焦即退整条失效——二审阻塞 3）；⑥**尺寸 DPI 缩放**：创建后 `GetDpiForWindow` 取 scale，`SetWindowPos` 按 `W*scale/H*scale` 物理尺寸设置，CSS 视口维持 340 逻辑宽（二审重要 6：锚定坐标物理直通、尺寸逻辑×scale）；⑦`WM_ACTIVATE` 失活 → exit 0；⑧导航白名单：`NavigationStarting` 仅放行 `--url` 的回环 origin（调研 §6.3 不变量）；⑨消息 handler：quit → exit 60；open → 按 kind 取 `--open-dashboard/--open-peer` URL 开默认浏览器（ShellExecute）后驻留。
- **macOS（panelwin_darwin.m）**：NSPanel（borderless + `NSWindowStyleMaskNonactivatingPanel`，`canBecomeKey=YES`）+ **`NSVisualEffectView`（behindWindow）材质铺底 + WKWebView `drawsBackground=false` + NSPanel `opaque=false`**（spec §1/§7：毛玻璃归系统材质层，前端只画半透明底；`drawsBackground` 为私有 KVC 面——公证探针随 Task 16 亚克力探针同批，调研 §6.5）+ `windowDidResignKey` → exit 0 + `WKUserContentController addScriptMessageHandler name:@"panel"`（**handler 名必须为 "panel"**，与前端 `messageHandlers.panel` 对齐，结构测试断言）+ `makeKeyAndOrderFront` + `NSApp activate`（不取焦点则失焦即退永不触发）+ open → `NSWorkspace openURL`。导航白名单：`decidePolicyFor` 仅放行回环 origin。
- 结构测试（.m）：含 resignKey、`NSVisualEffectView`、`addScriptMessageHandler name:@"panel"`、全部 autoreleasepool、无裸 autorelease 创建点。

- [ ] 锚定测试红→绿；结构测试红→实现→绿；`go build ./cmd/...` + `GOOS=windows go build ./cmd/...` + `GOOS=linux go list -deps ./cmd/aliang | grep -cE "webview2|panelwin|wails"`（期望 0）
- [ ] **手工冒烟（本机 macOS）**：面板出现在图标下方、无 Dock 图标、毛玻璃材质可见、点击内部获焦、点外部退出、quit 消息退出码 60、open 开浏览器后驻留
- [ ] Commit（`新增:internal/panelwin——纯原生窗口壳(NSPanel+WKWebView / WS_POPUP+WebView2)`）

---

### Task 14: launcher 生命周期状态机（纯逻辑）

**Files:** Create `app/tray/panel_launcher.go` + `panel_launcher_test.go`

```go
type PanelLauncher struct {
    spawn func(SpawnOpts) (ChildProcess, error) // ChildProcess: Wait()<-chan int; Kill()
    now   func() time.Time
    onFallback      func()
    onFocusFallback func()
}
func (l *PanelLauncher) Click()   // 未开→spawn；已开→Kill(标有意收起)
func (l *PanelLauncher) childExited(code int, alive time.Duration, killedByUs bool)
func (l *PanelLauncher) RetryFromFallback()
```

失败判定（拍板 1「只兜崩溃」）：崩溃 = `code != 0 && code != 60` 且非主动 Kill（`kill -9` 的信号死亡 exit −1 亦落入）→ 记失败 + 1s 后重拉；`code == 0`（失焦自退）与 `killedByUs`（点图标收起）= 正常关闭，零惩罚零重拉；10 分钟滑窗 3 次崩溃 → `onFallback()` + 停拉；存活 >60s 清零；`code == 60` → OnQuitRequested；fallback 态 Click 无效，仅 RetryFromFallback 可退出。

表驱动必测：正常开合；快速双击 toggle 不计数；失焦自退（code=0, alive<2s）不计数；连崩 3 次→fallback；fallback 后 Click 无效、Retry 恢复；60 分派；>60s 清零。

- [ ] 红→实现→绿→Commit（`新增:面板 launcher 状态机——崩溃兜底/正常关闭零惩罚`）

---

### Task 15: companion M2 接线——spawn/兜底/恢复项 + 手工验收

**Files:**
- Create: `app/tray/panel_bridge_m2.go` 接线扩展 + 测试追加
- Modify: `app/tray/companion.go`、`companion_windows.go`

- onReady 菜单构建顺序：**「打开面板」项在 onReady 构建时排在 AI header 之前创建**（库只有 append 语义；面板模式 `Hide()`、兜底态 `Show()`，点击 → `RetryFromFallback()`）。
- `SetPanelClickHook(launcher.Click)`；macOS `SetPanelMode(true)`。
- Windows spawn 三件套：`CREATE_SUSPENDED` → `AllowSetForegroundWindow(pid)` → Resume + Job `KILL_ON_JOB_CLOSE`（companion 退出收割面板；**macOS 无收割机制，依赖失焦自退**——评审次#13）。
- 锚定判废（在接线层）：Windows `TrayRect()` 失败/超 1.5 倍主屏 → GetCursorPos 兜底；macOS 直传。
- bridge 测试：接线、fallback 切换、菜单项显隐翻转。
- **手工验收（本机 macOS）**：弹面板→再点收起（零惩罚）；`kill -9` 面板→自动重拉；连杀 3 次→菜单接回+「打开面板」→点击走通；面板内退出→确认→既有退出闸照旧。
- [ ] 红→实现→绿→验收记录→Commit（`新增:companion M2 接线——spawn/兜底切回/恢复项`）

---

### Task 16: 打包、文档与验收收口

- [ ] **Linux 排除门终验**：`GOOS=linux CGO_ENABLED=1 go list -deps ./cmd/aliang | grep -cE "webview2|panelwin|wails"`（期望 0）；`GOOS=linux CGO_ENABLED=0 go build ./cmd/...`
- [ ] 全量测试：`go test ./app/tray/ ./internal/ipc/ ./internal/singleinstance/ ./cmd/ ./internal/panelwin/ ./app/http/services/ -count=1` + `cd app/website && npm run test -- src/panel`
- [ ] PATCH_NOTES 登记 Task 3/4 补丁（重放锚点清单）；调研文档随本批提交入库
- [ ] 出包：macOS `VERSION=v1.1.129-local bash scripts/build-pkg.sh` 装机；Windows 走 MSI 链
- [ ] **验收走查（spec §9 + 调研 §7 探针）**：macOS 26/27 各一台七态逐项 + 深浅色 + 失焦 + 锚定 + 崩溃演练 + `footprint` 对照 + **毛玻璃材质可见**；Windows 11 真机（4070）：同清单 + GDI 平坦（GetGuiResources）+ 溢出区锚定回退 + DPI 125%/150%（CSS 视口宽必须恒 340）+ **亚克力/圆角实机探针**（不生效→回退纯半透明并回写 spec §7）+ **WKWebView 透明底公证探针**
- [ ] spec 回写备注（豁免项：错误行跳日志、设备互访 v1=仪表盘根页、§7 材质探针结论）
- [ ] Commit（`新增:托盘面板 M2 收口——壳验收与文档`）

---

## 明确不做

Linux 任何托盘能力；fyne-io/Wails 托盘与窗口栈；v4 协议/GUID；变更门控三端统一；endpoint/会话管理界面；provider 详情 popover（v1 toast）；面板高度自适应（固定 560）；日志页深链（开仪表盘根页）。

## 风险与执行注意

- macOS 点击接管仍是最高风险区：Task 4 Step 7 真机先验是硬闸；M1 先行正好提供一版真实使用数据。
- Windows 三处实机探针预算了回退：亚克力/圆角（Task 13/15/16）、WKWebView 透明公证（Task 16）、go-webview2 底层组件（pseudo-version 钉死，升级过实机回归）。
- M1 上线后收集一轮真实使用反馈再进 M2，是两步走的本意。
- 生产机不跑全量 go test（测试二进制污染 companion 日志的既有问题）；跑完看一眼日志。
- 手工验证步骤必须真实执行并记录；跳过的写进任务备注，不得静默勾选。
