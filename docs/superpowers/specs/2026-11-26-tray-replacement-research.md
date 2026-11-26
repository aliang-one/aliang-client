# 托盘替换调研（v2）：三平台「炫酷托盘菜单」能做成什么样、代价是什么

> 调研日期：2026-11-26 ｜ 对象仓库：alianggate（调查期间 HEAD 由 68e35f19 → 56b26f8b，并行会话的合入均未触及 `app/tray`、`third_party/systray` 与 cmd 层托盘入口）｜ 本报告未改动仓库任何文件
>
> v2 说明：v1 草稿经三位评审（鲁棒性 / 体验 / 工程迁移）和一名事实核查员对抗评审，结论是「方向成立，需修改后交付」。v2 修正了全部 7 条阻塞项与主要重要项，并补入 PoC 实测与 macOS 内存泄漏根因。
>
> 证据标记：〔源码〕读了一手源码 ｜〔文档〕官方文档或规范原文 ｜〔实测〕本机或容器实测 ｜〔单源〕只有一个来源 ｜〔推断〕由间接证据推出 ｜〔未复核〕未能独立验证

## 0. 结论先行

**一句话**：你觉得「跟不上」是对的，但卡点不在某一个库——Go 托盘库只负责把菜单交给系统画。要炫酷，必须拆成两层看：**托盘底座**（值得换，而且该换）和**菜单表现力**（换库换不来，得另做面板）。

### 0.1 先说一个意外收获：macOS companion 内存泄漏的根因找到了

- **根因**：内嵌库的 macOS 后端 `third_party/systray/systray_darwin.m` 在没有 autorelease pool 的 goroutine 线程上创建 ObjC 对象，每次 UI 调用都漏一截，整个文件 0 处 `@autoreleasepool`。这是上游 getlantern/systray 的设计缺陷（#135 自 2020 年开着至今）。
- **现网实测**：本机 companion 跑了 2 天 22 小时，footprint 5866MB（MALLOC_SMALL 脏内存 5.8GB、2870 万个小块）。RSS 只有 32MB，因为这些页几乎全被系统压缩了——所以一直看不出来。速率约 49KB / 2 秒 tick ≈ **2.1GB/天**。
- **受控复现**：与线上旧代码同构的 tick 每次漏 52.7KB，数字对得上；给 9 个入口各包一层 `@autoreleasepool` 后，1200 次 SetIcon 与 1600 轮完整 tick 全程零增长。
- **修法**：约 20 行纯增量补丁，已验证、未实施。详见 §1。重启 companion 可立即回收约 5.8GB（需要你操作）。

### 0.2 调研结论

1. **现用库确实该换**。getlantern/systray 最新版仍是 v1.2.2（2023-03-27），2023-11-10 之后无提交，97 个 open issue（另有 17 个 PR）。活跃分支 fyne-io/systray（v1.12.2，2026-06-09 发版，2026-11-22 仍在提交）API 同名可平移，Linux 是纯 Go D-Bus。但 Windows 菜单项图标的 HBITMAP 泄漏在 fyne-io v1.12.2 和 master 里原样存在（全文 0 处 `DeleteObject`），你的 3 处补丁必须带走。
2. **两个前提需要纠正**：
   - 本仓 Linux 发行物里**没有托盘**：`cmd/tray.go` 的构建标签是 `darwin || windows`，Linux 编进去的是报错桩。Linux 托盘是「从零新增」。
   - 调研到的全部 Go 托盘库渲染的都是系统原生菜单，**没有任何一个提供自绘**。光换库，菜单长相不会变。
3. **「炫酷」在三端的上限差得很远**：
   - **Windows**：可以自绘弹出面板，而且左键弹内容、右键出菜单是微软文档写明的形态。上限最高。
   - **macOS**：Apple HIG 要求点击出菜单而不是面板。原生菜单还有一批现库没暴露的能力（分组标题、副标题、徽标、模板图标），但 27 起默认隐藏菜单项图标，26 起有「允许在菜单栏中」的系统开关。
   - **Linux**：菜单只能由桌面环境渲染；Wayland 下无法把窗口贴到托盘图标旁；GNOME 默认没有托盘。上限最低。
4. **推荐分步走**（§6）：
   - **步骤 0 与 1a：纯加固，行为不变**。先堵测试闸与 IPC 隐患，修泄漏，推广变更门控。
   - **1b：换底座，行为不变**。Windows / Linux 切到内嵌 fyne-io v1.12.2 并带走补丁，macOS 的 ObjC 后端按新接口重写。旧后端保留一个版本的回滚开关。
   - **1c：增强，逐项开关、逐项验收**。macOS 模板图与分组菜单、Windows 状态图标与 explorer 重试、Linux companion（是否做单独拍板）。
   - **阶段 2：左键富面板**，仅 Windows 默认开启。面板放在「点击才拉起、关闭即退出」的独立辅助进程里，右键永远保留原生菜单兜底。
   - 这里不再说「第 1 步只升鲁棒性」：它以鲁棒性为硬约束，但 1c 和 Linux 是行为变更，要逐项列明代价与兜底。
5. **不推荐**：整体迁到 Wails / Tauri / Qt（与「鲁棒性不让路」直接冲突）；gogpu/systray（v0.x）；energye/systray（2023 年的旧 fork）。

需要你拍板的事项见 §7。

---

## 1. macOS companion 内存泄漏：根因、证据、修法

### 1.1 现象〔实测〕

本机现网 companion（PID 17187，/Applications/Aliang.app，1.1.126-local，2026-11-23 10:37 签的包，已运行 2 天 22 小时）：

| 指标 | 数值 |
|---|---|
| RSS（`ps`） | 32MB |
| footprint | 5866MB |
| 其中 MALLOC_SMALL 脏内存 | 5796MB，2870 万个存活块（平均 208 字节/块），1459 个区域 |
| ImageIO 区域 | 33,976 个 |
| 压缩内存（CMPRS） | 5.8GB |
| 09:47:19 → 09:51:20 两次采样 | 5860MB → 5866MB，约 25KB/s |

- 折算：每个 2 秒 tick 约 49KB，约 2.1GB/天。「5866MB ÷ 运行时长」与当前速率一致——从启动起就在匀速漏。
- **为什么一直没被发现**：漏出来的页几乎全被 macOS 压缩换出，常驻内存常年 32MB 左右。用 `ps`、`top` 的常驻内存看，它是个很轻的进程。代价转嫁到了交换区——本机 swap 此刻 33.7GB / 34.8GB 已用。

### 1.2 根因〔源码〕

`third_party/systray/systray_darwin.m` 的每个入口都在**调用者 goroutine 所在的线程**上创建 ObjC 对象，然后才切到主线程：

```objc
void setIcon(const char* iconBytes, int length, bool template) {
  NSData* buffer = [NSData dataWithBytes: iconBytes length:length];  // autoreleased
  NSImage *image = [[NSImage alloc] initWithData:buffer];            // 解码出一整棵对象树，大量 autoreleased 中间对象
  [image setSize:NSMakeSize(16, 16)];
  image.template = template;
  runInMainThread(@selector(setIcon:), (id)image);                  // 到这里才切主线程
}
```

- Go 运行时的线程上没有 autorelease pool，autoreleased 对象永远不会被释放。整个文件 0 处 `@autoreleasepool`。
- `setTitle` / `setTooltip` / `add_or_update_menu_item` / `hide_menu_item` 等 8 个入口同理，每次漏得少（几百字节）。
- macOS companion 每 2 秒一个 tick，而且这条路径没有变更门控（状态没变也全量落 UI），所以匀速漏。
- fyne-io 的同名文件只给两条图片路径加了 pool，`NSData` 还在 pool 外，其余 7 个入口没有——换库只能修掉大半。

### 1.3 证据：受控实验〔实测〕

全部用仓内内嵌库，macOS 26.2 / Apple Silicon，图标与调用序列取自仓库。对照列的「加 pool」指 §1.4 的补丁。

| 实验 | 现库原样（MALLOC_SMALL 脏内存） | 加 `@autoreleasepool` |
|---|---|---|
| 对照组：不调任何 setter，同等时长 | 5.84MB 不变 | — |
| A：每秒 10 次 `SetIcon` × 600，静置 60 秒，再 600 次「SetIcon + SetTooltip + SetTitle + Check + Uncheck」 | 6.33 → 9.75 → 14.00MB，线性；静置不回落。ImageIO 区域 62 → 2400（每次 SetIcon +2） | 6.12MB 全程不变，ImageIO 区域恒为 2 |
| B：与当前 HEAD 同构的 tick（真实图标 + 14 次调用）× 600 | 6.72 → 12.00MB，8.8KB/tick | 6.31 → 6.22MB 不变 |
| C：与线上一致（去图标化之前的旧代码：每 tick 主图标 + 4 个 provider 菜单项图标）× 400 | 7.91 → 29.00MB，**52.7KB/tick**（线上实测 49KB） | 6.25 → 6.30MB 不变 |
| 旁证：对线上进程做 6 秒调用栈采样 | 主线程周期性出现在 `-[NSImage initWithData:]` → `PNGReadPlugin::decodeImageImp`，以及 `-[NSMenuItem _setImage:imageType:resettingSize:]`；没有其他框架活动 | |

怎么读这张表：

- 本机装的是 11-23 的包，早于 11-25 的「去图标化」提交（b2dc1525），每个 tick 解码 5 张图，所以是 49KB/tick。
- 当前 HEAD 已经去掉菜单项图标，泄漏降到约 8.8KB/tick ≈ 380MB/天，但**仍在漏**。
- 「加 pool 且缓存 NSImage」的变体同样平坦，缓存只省 CPU（每次约 1.8ms），不是修复的必要条件。

### 1.4 修法（已验证，未实施）

最小补丁：把 `systray_darwin.m` 里 9 个 C 入口（`setIcon`、`setMenuItemIcon`、`setTitle`、`setTooltip`、`add_or_update_menu_item`、`add_separator`、`hide_menu_item`、`show_menu_item`、`quit`）的函数体各包一层 `@autoreleasepool { … }`。纯增量，约 20 行。

顺手可做：

- 缓存 NSImage，同一份字节只解码一次。
- 把 Windows companion 已有的变更门控推广到 macOS：状态不变时 0 次 UI 调用。

**立即缓解**：重启 companion 可以回收约 5.8GB。本机 swap 快满了，建议尽快。我没有动线上进程。

复现材料在 `/tmp/tray-leak/`（仓库之外）：探针源码、harness、两个补丁变体、结果 JSON、线上进程的调用栈采样。

---

## 2. 两步之后，你会看到什么

| | 现状 | 第 1 步之后 | 第 2 步之后 |
|---|---|---|---|
| macOS | 彩色小 logo，不随深浅色变色；12 项平铺菜单；AI 区隐藏；每 2 秒刷一遍 UI | 单色模板图标，随深浅色与选中态自适应；带分组标题的菜单（14+ 才有分组标题，12–13 回退为分隔线）；状态行改成人类可读；只在状态翻转时刷新；不再泄漏 | 默认不变。macOS 上「点击出菜单」和「左键面板」互斥，见 §6.5 |
| Windows | 左键右键都弹经典菜单；新装用户的图标默认在「^」溢出区 | 菜单观感基本不变（圆角本来就有；深色系统下仍是浅色菜单）；图标多几个状态变体；泄漏、登录竞态、幽灵图标修掉 | 左键弹出圆角亚克力面板（内容待定），右键仍是原生菜单；首次点击要等约 0.5–1 秒 |
| Linux | 没有托盘 | 首次有托盘：桌面环境自己画的菜单，带图标和单选 | 不变。Wayland 做不了贴图标的面板 |

**诚实结论**：第 1 步等于「底座加固 + macOS 精致化 + Linux 从无到有」，Windows 菜单观感基本不变。真正称得上「炫酷」的只有第 2 步的 Windows 面板。要不要做第 2 步，取决于你要不要只有面板才放得下的内容（实时速率图、设备互访列表）——见 §7 第 1 问。

---

## 3. 现状：库卡在哪

### 3.1 库的身份与维护状态

| | getlantern/systray（现用） | fyne-io/systray（活跃分支） |
|---|---|---|
| 最新版本 | v1.2.2（2023-03-27） | v1.12.2（2026-06-09）；master 2026-11-22 |
| 活跃度〔源码〕 | 2023-11-10 后无提交，2024 年起 0 个 PR 合并；97 个 open issue（另 17 个 PR） | 2025 年以来 21 个 PR 合并；10 个 open issue（另 15 个 PR） |
| Windows 实现〔源码〕 | 纯 Go Win32（仅依赖 x/sys/windows，**不是** lxn/walk）+ `TrackPopupMenu` | 同一套代码，另修了菜单项图标黑底（32bpp DIB）、加了 `DestroyMenu` |
| macOS 实现〔源码〕 | cgo Objective-C，`[statusItem setMenu:]` 系统托管 | cgo；用 NSEvent 本地监视器 + 按钮上叠子视图区分左右键 |
| Linux 实现〔源码〕 | cgo GTK3 + libayatana-appindicator（加载期硬依赖） | 纯 Go D-Bus（SNI + DBusMenu），无 GTK、无 cgo |
| Windows 菜单项图标泄漏〔源码〕 | 有（本仓已打 3 处补丁） | **同样有**：v1.12.2 与 master 全文 0 处 `DeleteObject` / `DestroyIcon` / `NIM_SETVERSION` |
| macOS autorelease 泄漏〔源码〕 | 有（§1） | 两条图片路径有 pool，其余没有（§1.2） |
| 托盘点击回调 | 无 | `SetOnTapped` / `SetOnSecondaryTapped`（v1.12.0 起） |
| 菜单打开事件 | 无 | `TrayOpenedCh`（macOS / Linux 有，Windows 无，PR #140 未合并） |
| 删除菜单项 | 无（只能 Hide） | `MenuItem.Remove()`（会关闭 `ClickedCh`，见附录 B） |
| License | Apache-2.0 | Apache-2.0 |
| 知名使用方〔源码〕 | — | Tailscale（Linux 托盘直接 require v1.12.2）、Fyne、Fleet/Kolide、Portmaster |

本仓对现库的改动经逐文件比对核实：`third_party/systray` 与上游 v1.2.2 逐字节一致，仅 `systray_windows.go` 多 8 行（即 PATCH_NOTES 所列 3 处 GDI 补丁）。

### 3.2 想要的能力 vs 现库能不能做

| 想要的能力 | 现库 | 说明 |
|---|---|---|
| 左键开面板/仪表盘、右键弹菜单 | ❌ | 托盘图标没有点击回调；Windows 左右键都走 `showMenu()`，macOS 启动即 `setMenu:` |
| 菜单项图标（provider 图标） | ⚠️ | Windows 每次调用新建 HBITMAP（0.196 事故源头），透明区是黑底；macOS 强制 16×16；Linux 是空函数。HEAD 已整体移除（b2dc1525） |
| 菜单项 tooltip 承载提示 | ❌ | 三端都不显示（Windows 不传给 Win32，Linux 收到即 free，macOS 被忽略）。注意：「未登录不能启动」的原因**已经在常显的 Status 行里可见**，缺的是就地提示与可操作性 |
| 分组标题 / 副标题 / 徽标 | ❌ | macOS NSMenu 原生支持（14+），库没暴露 |
| 模板图标（随深浅色自适应） | ⚠️ | 只有 macOS 真支持，项目从未使用；现在是 128px 彩色 PNG 缩到 16×16 |
| 托盘旁文字 | 未用 | `SetTitle` 在 macOS 现在就能用（库里有、项目零调用） |
| 动态增删菜单项 | ⚠️ | 只能 Hide 不能删；靠预建 4 个隐藏槽位模拟，超过 4 个 provider 不显示，还会留一条悬空分隔线 |
| 点击不丢 | ❌ | `ClickedCh` 无缓冲 + 非阻塞投递，处理期间的点击被丢弃；companion 启动窗口内的点击全部丢失 |
| 动作失败有反馈 | ❌ | companion 里 Start / Stop / 切模式失败只写日志，菜单毫无变化（只有单进程 TrayApp 有桌面通知兜底） |
| 状态没变就不刷 UI | ⚠️ | 库无内容去重；只有 Windows companion 靠手写签名门控 |
| 深色模式 / 外观变化事件 | ❌ | 无。Windows 原生菜单跟随深色主题只能靠未文档化的 uxtheme 序号〔未复核〕 |
| 气泡通知 | ❌ | 另接了 beeep（Windows 上因此多链接了一个托盘库 tadvi/systray，每次通知另起一个临时图标） |
| 自定义视图 / 开关 / 图表 / 动画 | ❌ | 三平台都只是系统原生文本菜单 |
| 错误可感知 | ❌ | 所有 API 无返回值。Windows 上初始化失败（含 explorer 未就绪）只打日志，既不回调 onReady 也不返回错误，菜单项一个都没建 |
| Linux 托盘 | ❌ | 发行物里没有；库的 Linux 路径是 cgo + GTK 加载期硬依赖 |

### 3.3 项目怎么用它（替换时必须保形的契约）

- **爆炸半径很小**：全仓只有 `app/tray` 的 4 个文件 import systray；cmd 层经 5 个调用点进入。
- **API 面很窄**：包级 `Run / Quit / SetIcon / SetTooltip / AddMenuItem / AddMenuItemCheckbox / AddSeparator`，加 `MenuItem.SetTitle / SetTooltip / Enable / Disable / Hide / Show / Check / Uncheck` 与 `ClickedCh`。这些符号在 fyne-io v1.12.2 里同名同签名存在（对照见附录 B）。
- **三套入口各写了一份近似重复的菜单代码**（554 / 549 / 568 行）：
  - TrayApp 单进程：`aliang tray`（macOS 无参启动的默认根命令；Windows 上是带控制台的非主路径），代理与仪表盘在进程内，不走 IPC。
  - macOS companion：双击 .app / `tray-agent` LaunchAgent，经 unix socket 连 root LaunchDaemon 的 core。
  - Windows companion：快捷方式 `companion` 或双击 GUI 模式，经命名管道连 session 0 的服务。
  - Linux：无。
- **菜单**：12 项 + 4 分隔线，onReady 一次性预建，之后只原地改属性。
- **状态同步**：2 秒轮询 IPC `get_status`，没有推送。只有 Windows companion 有变更门控。
- **一个现状陷阱**：代理运行中点另一个模式，会**立刻停掉正在运行的代理且不重启**，托盘里没有任何提示（`app/http/services/run.go` 的 SwitchMode，返回文案是「Proxy remains stopped until start is called」）。
- **测试**：17 个纯函数单测，没有任何测试触碰到菜单构建与状态应用；发布 CI 不跑任何 go test（两个 workflow 里 `go test` 零命中）。

### 3.4 事故史：近 12 次托盘相关提交里 9 次是缺陷修复

| 日期 | 事故 | 根因 | 修法 |
|---|---|---|---|
| 2026-08-18 / 09-14 | 未登录时 Start 可以点 | 登录门控在 companion 重写时丢失；macOS 菜单项 tooltip 不显示 | 新增 IPC action + 三态决策，原因改由 Status 行承载 |
| 2026-09-14 | Win11 非 100% 缩放下托盘菜单是空白方块 | 进程未声明 DPI 感知（库不管） | 自补 `enableWindowsDPIAwareness` |
| 2026-10-05 ~ 10-09 | agent 掉线静默 | 看护逻辑寄居在 companion | watchdog 退避 / 注册巡检 / .app 单实例 |
| 2026-11-16 | core 重启后 dashboard 不复活 | — | companion 按 30 秒冷却补发 `start_http` |
| 2026-11-20 | 0.196 托盘图标消失（代理仍在） | `MenuItem.SetIcon` 每次新建 HBITMAP 不回收，每 2 秒 × 4，约 83 分钟耗尽 GDI 配额（1 万是业界通称的默认值） | 变更门控 + 内嵌补丁版（3 处） |
| 2026-11-25 | 菜单项去图标化 | 上一条的「再加一层保险」 | 删掉 `MenuItem.SetIcon`，状态改用 ✅/⏳ 前缀 |
| 长期存在，本次定位 | macOS companion 内存持续增长 | 无 autorelease pool 的线程上创建 ObjC 对象（§1） | 待实施：9 处 `@autoreleasepool` |

> **仓库状态提醒**：本次调查开始时暂存区里有一份把菜单项图标加回来的改动（`ai_status_menu.go`、`tray_quit_test.go` 等 4 个文件）。它在 00:34 被另一个并行会话存成 stash「other-session-wip-4files」之后被 drop，现在既不在工作区也不在 stash 列表里。它仍以悬空提交 `2975961a` 的形式存在（`git fsck --unreachable` 可见，gc 前可找回）：`git stash apply 2975961a`。本次调查没有改动仓库。

其余现存隐患（IPC 并发、退出语义、companion 无监管等）见附录 A。

---

## 4. 「炫酷」的天花板：三个平台分别能炫到哪

### 4.1 先分清两件事：托盘底座 ≠ 菜单绘制

托盘库的职责只有三件：注册图标、把菜单项列表交给系统、把点击回传。菜单长什么样由系统决定。

调研到的全部 Go 托盘库在 Windows 都是 `TrackPopupMenu` 弹 HMENU，macOS 都是 NSMenu，Linux 都是 DBusMenu 属性表，没有一家做 owner-draw 或自定义视图〔源码，对抗核查已证实〕。

所以「炫酷」只有两条路：

- **把原生菜单用满**：分组标题、图标、模板图、徽标……前提是库把这些能力暴露出来。
- **自己画一个面板窗口贴着图标弹**：这是另一套 UI 栈，和托盘库无关，只需要托盘库给两个钩子——点击回调和图标坐标。

### 4.2 macOS

**规范的立场**〔文档〕：
- 点击菜单栏额外项时应显示菜单而不是 popover，除非功能对菜单来说太复杂。Apple 自己也给复杂功能留了官方口子（SwiftUI 的 MenuBarExtra window 样式，13+），但那是 SwiftUI 的 API，本仓的 Go / cgo 工程用不上。
- 是否放进菜单栏由用户决定；不要依赖它存在。
- 菜单项图标要少而有目的，同一组要么都有要么都没有（2025-07 / 2026-06 更新）；不可操作的项应禁用而不是隐藏。

**原生菜单还没用上的能力**〔源码，本机 SDK 头文件〕：分组标题 `sectionHeaderWithTitle:`（14.0+）、副标题（14.4+）、徽标（14.0+）、状态列、快捷键、模板图 / SF Symbols、调色板式子菜单。现库一项都没暴露。**本仓最低支持 macOS 12.0**，12–13 上这些要回退成「分隔线 + 禁用的标题行」，状态行主标题必须自足。还有一条中间路线：`NSMenuItem.view` 可以在原生菜单里嵌自绘内容，不离开菜单形态，但不画标准标题、不支持键盘输入（eul 把整块 SwiftUI 塞进去的做法在 27 上已有失效点）。

**模板图**：现用 128×128 彩色 PNG 缩成 16×16，非模板，不随深浅色和选中态自适应。规范做法是黑 + 透明的模板图〔单源，Bjango〕。图标从彩色品牌图变成单色模板图是品牌层面的决定。

**2026 年的新约束**：
- **macOS 27（Golden Gate，2026-09-14 发布，当前 27.2.1）起按所链接的 SDK 分档隐藏菜单项图标**：链接 26 SDK 的隐藏符号图，链接 27 SDK 的位图也隐藏；要用 `preferredImageVisibility = .visible` 逐项打开，且 AppKit 仍可能隐藏〔文档〕。官方包链接的是 15.5 SDK，目前不受影响；本机 local 包是 26.2 SDK。
- **「系统设置 › 菜单栏 › 允许在菜单栏中」逐应用开关（26+）**：关掉后永不再出现，而且按 responsible process 归属。必须有不依赖图标的入口，并设稳定的 `autosaveName`（现库没设，落在共享的 Item-N 槽位上）。全新安装的应用是否默认被隐藏未核实〔单源，Stats 称需显式打开〕。
- **27 把整条菜单栏画成一个窗口并自带溢出收纳**〔Ice#954 + 二手来源〕：依赖「每个状态项各有独立窗口」的定位与可见性技巧不再可信。
- **AppKit 层面 27 才有「点图标弹自绘窗口」的生命周期 API**（`NSStatusItem.expandedInterfaceDelegate`）：27 以下要自己处理焦点、高亮、点外部关闭；且它要求状态项**不挂菜单**。

**图标动效**：实测图标每切换一次约 1.2–2.1ms CPU，连续 SF Symbol pulse 动效约 0.5% 单核〔实测，macOS 26.2〕。可行，但 27 上状态项更新改经新进程 MenuBarAgent，有「高频更新拖高 CPU」的报告（beta 期，正式版未证实）——动效要事件驱动并节流。

**结论**：macOS 的「炫酷」= 克制而精致的原生菜单。天花板 ★★★☆，方向由 Apple 定。

### 4.3 Windows

**规范的立场**〔文档〕：右键出普通快捷菜单；左键「显示用户最想看的内容——弹出窗口、对话框或程序窗口」；不要闪烁图标，不要长时动画。注意这两份微软文档是 Win7 时代写的、自述未随 Win11 更新。

**原生 HMENU**：Win11 下自动圆角（旁证来自微软 Q&A 与 Windhawk issue，圆角文档原文说的是「菜单弹出窗口默认不圆角，自定义菜单需 opt-in」〔未复核〕）；第三方菜单没有亚克力（由第三方钩子工具的存在反推〔推断〕，系统自己的 flyout 是 XAML，不对外开放）；放不了富内容。WinUI 3 / Windows App SDK 至今没有托盘 API（microsoft-ui-xaml #2020 在 2026-11-25 刚被移进 Backlog）。

**自绘弹出面板**（可做，坑都已知）：
- 窗口样式 `WS_POPUP + WS_EX_TOOLWINDOW + WS_EX_TOPMOST`；圆角用 `DWMWA_WINDOW_CORNER_PREFERENCE`（22000+）；瞬态材质用 `DWMSBT_TRANSIENTWINDOW` 亚克力（22621+，对无边框 WS_POPUP 是否直接生效需实机验证）。
- 用分层窗口或窗口区域就永远不能圆角。
- 弹之前必须 `SetForegroundWindow`，否则点外部不消失；拿不到前台时没有任务栏按钮可闪。
- 锚定用 `Shell_NotifyIconGetRect`，但图标在溢出区时 Win10 返回 S_FALSE + 箭头坐标、Win11 返回屏外坐标〔单源，Qt〕。要用「矩形是否落在任务栏内」判可见。
- 26H2 起任务栏可以置顶 / 左 / 右，「任务栏在底部」的假设不再成立。
- 新图标默认进溢出区，只有用户能提升；Win11 没有全局「显示全部图标」的开关。

**现库的具体缺口**〔源码〕：停在 v0 回调协议（库不转发鼠标移动，拿不到 `NIN_POPUPOPEN/CLOSE`、`NIN_SELECT`、`WM_CONTEXTMENU`）；无 GUID；explorer 未就绪不重试；图标按首次尺寸永久缓存，不随 DPI 重载；`NOTIFYICONDATA` 结构体错位导致 tooltip 约 64 字符截断；弹菜单未补发 `WM_NULL`、未带 `TPM_RIGHTBUTTON`。

**别人怎么做**：
- PowerToys：精简 Win32 进程持有托盘 + 原生菜单；富面板是**常驻**的独立 WinUI 进程（runner 启动时拉起，点击只是通知它显示），靠 Job 对象 KILL_ON_JOB_CLOSE + 管道 IPC。
- EarTrumpet：左键混音浮窗，启动时就建好、随点随显。
- TranslucentTB：XAML Islands 渲染菜单，保留 HMENU 回退。
- Proton VPN：右键 Win32 原生菜单，左键 WinUI 窗口。
- Docker Desktop：Electron，Chromium 自绘的仿原生菜单。Tailscale（Windows 形态未核实）、Clash Verge Rev：原生菜单。

**结论**：Windows 的上限最高且被官方背书——左键亚克力圆角面板 + 右键圆角原生菜单。★★★★。

### 4.4 Linux

**前提**：本仓 Linux 发行物里没有托盘。CI 和 deb 脚本注释里的 GTK 依赖是过时残留。

**协议上限**〔文档，规范原文〕：SNI 规范是最后编辑于 2017-05 的 v0.1 草案（Publication Date: TBD），重写议题 xdg-specs #84 至今 open。菜单就是 DBusMenu 条目树（label / enabled / visible / icon-name 或 icon-data / toggle-type / toggle-state / children-display / shortcut），不含任何布局能力，规范明写「不定义条目的外观」。

- 能炫的：主题化菜单项图标、radio 勾选表达模式（协议能力，fyne-io 不暴露，要自己补）、NeedsAttention 时宿主自带的脉冲、tooltip 文本（KDE / waybar；GNOME 扩展不显示 tooltip）。
- 不能的：富文本、自定义布局、嵌入控件、tooltip 带图标。

**宿主矩阵**〔源码，各宿主仓库〕：

| 桌面 | SNI 宿主 | 备注 |
|---|---|---|
| KDE Plasma 5 / 6 | 原生 | 6.8（2026-10-14）起不再提供 X11 会话；XEmbed 仍由 xembedsniproxy 桥接 |
| XFCE 4.20 / MATE / Budgie | 原生 | XFCE / MATE 默认仍是 X11；Budgie 10.10 已迁 Wayland |
| Cinnamon | 经 xapp 桥接 | — |
| COSMIC | 自带 DBusMenu 渲染 | Activate 坐标恒为 (0,0) |
| GNOME 45–51 | **无原生支持** | 必须装「AppIndicator and KStatusNotifierItem Support」扩展；Ubuntu 默认装，Fedora / Debian 不默认 |
| elementary Pantheon | 默认无 | 只有极小众的社区小程序 |
| Hyprland / Sway | 靠 waybar（自评 beta） | swaybar 1.12 新增托盘但不渲染菜单 |

左键语义各宿主不同：Plasma / XFCE / waybar / Budgie 左键发 Activate；GNOME 扩展单击出菜单、双击才 Activate。

**自绘面板**：
- Wayland 下 xdg-shell 不允许客户端定位弹出面（必须与父面相交或相邻）。
- layer-shell 六年没进标准：KWin、wlroots 系、COSMIC、Mir 支持，GNOME 的 Mutter 不支持。
- 托盘协议 ext-tray-v1 的 MR 至今未合并。
- Syncthing Tray 文档明写「Wayland 下无法使用弹出窗口」。
- X11 会话可以用 override-redirect 贴图标弹，但 Plasma 6.8、GNOME 50、Budgie 10.10 都已经没有 X11 会话。

**分发代价**〔实测，Docker〕：
- cgo + GTK / ayatana 路线是加载期硬依赖：只空白 import getlantern/systray 的 Go 二进制，放到没装桌面库的 debian:bookworm-slim 里直接 `exit 127`；`CGO_ENABLED=0` 根本编不过。
- 对照组：fyne-io v1.12.2 以 `CGO_ENABLED=0` 构建出静态二进制（6.1MB，无 `DT_NEEDED`），在无 GTK 环境正常运行，并在 Xvfb + xfce4-panel 4.18.2 下实测注册、渲染、菜单事件闭环成功。KDE / GNOME / waybar / Cinnamon / MATE / Budgie 均未实测。

**结论**：Linux 的「炫酷」= 宿主原生菜单做到位 + 打开仪表盘。★★。左键面板不做。

### 4.5 上限对照

| 能力 | macOS 26 / 27 | Windows 10 / 11 | Linux |
|---|---|---|---|
| 原生菜单分组与图标 | ✅ 分组标题 + 副标题 + 徽标（14+；27 按 SDK 分档隐藏图标） | ✅ 位图图标；Win11 圆角〔未复核〕；无亚克力〔推断〕 | ◑ 宿主而定 |
| 模板图 / 深浅色自适应 | ✅ | ◑ 单色图才需要自管深浅两套 | ◑ icon-name 跟主题 |
| 左键弹自绘面板 | ◑ 可行但有条件：须不挂菜单；27 走 expandedInterface，≤26 自管焦点；WebView 透明要私有 API（公证影响未评估） | ✅ 官方推荐 | ❌ Wayland 不可行，X11 会话在萎缩 |
| 面板材质（毛玻璃 / 圆角） | ◑ NSGlassEffectView（26+）可用；WebView 面板依赖 `-tags private_mac_apis` | ✅ DWM 圆角 + 亚克力（22621+，WS_POPUP 上待实机验证） | — |
| 图标动画 | ◑ 可行，需在按钮上加子视图 | ◑ 官方不建议长时动画 | ◑ 只能换图发 NewIcon；脉冲由宿主提供 |
| 托盘可见性可控 | ❌ 26+ 用户开关，27 溢出收纳 | ❌ 默认进溢出区，仅用户能提升 | ❌ GNOME 需扩展，宿主可隐藏 |

---

## 5. 候选全景

### 5.1 Go 托盘库

| 库 | 版本 / 活跃度 | 判断 |
|---|---|---|
| getlantern/systray（现用） | v1.2.2，事实停更 | 不宜继续：缺陷只能自己长期扛，Linux 无托盘 |
| **fyne-io/systray** | v1.12.2，活跃 | ✅ Windows / Linux 底座首选，内嵌并带走补丁 |
| energye/systray | v1.0.3，2023-09 后基本停更 | ❌ 旧 fork，缺近两年修复，泄漏同款 |
| gogpu/systray | v0.3.0（2026-08），2026-04 才新建，85 star | ❌ v0.x：`Run()` 不返回连修 5 个补丁版、非主线程调 AppKit、Windows 没实现菜单项图标。列入观察 |
| NordSecurity/systray、kolide/systray | 企业 fork；NordSecurity 无 tag，kolide 停在 v1.10.x | 不直接依赖，作补丁来源 |
| deedles.dev/tray（Linux）、menuet（macOS）、go-macos/statusitem | 小众，平台单一 | 仅作参考实现 |
| purego/objc 自研（零 cgo 调 AppKit） | purego v0.11.3，自述 beta | ❌ 本仓主程序因 sqlite 本就 cgo，去 cgo 换不来构建收益；Wails 的 purego 托盘后端依赖的正是 27 上不再推荐的手法 |
| windigo（纯 Go Win32 封装） | v0.2.7 | 只有底层 API，没有托盘与弹窗的高层抽象 |
| gotk4（GTK4 绑定） | 活跃，仅 Linux | GTK4 已删 StatusIcon、move、keep_above，做不了贴图标置顶面板 |
| go-gui | v0.100.0，2026-03 才创建 | ❌ 0.x；macOS 只支持 Apple Silicon，本仓仍发 darwin-amd64 |
| darwinkit | v0.5.0，2024-07 后无代码变更 | ❌ 缺 14 / 26 / 27 的新 API，README 明言不推荐大型程序；仅作调用序列参考 |
| crawshaw.io/objc | 导入路径已 404 | ❌ 不可用 |

### 5.2 富面板技术栈

| 技术 | 成熟度 | 托盘 | 依赖与体积 | 判断 |
|---|---|---|---|---|
| Wails v3 | v3.0.0（2026-10-26）、v3.1.0（2026-11-01）稳定，刚满月 | 内置；Windows 实现的鲁棒细节到位，Linux `openMenu` 未实现 | WebView2（Win11 自带）/ WKWebView / GTK4 + WebKitGTK 6.0；最小程序 8.6–15.5MB〔实测，仅 macOS arm64〕 | ⚠️ 只适合放进独立面板辅助进程，且 Linux 必须用构建标签整体排除 |
| Tauri v2 | 2.12.7 | tray-icon / muda；Linux 默认后端不发任何事件、取不到图标矩形 | 需 Rust；sidecar 公证有未决问题 | ❌ 引入第二语言，架构方向相反 |
| webview_go | 2024-08 停更 | 无 | cgo + C++ | ❌ |
| Electron + Go | 成熟 | 有 | 115–151MB | ❌ 太重 |
| Qt（miqt） | v0.15.0，活跃 | QSystemTrayIcon 完整 | cgo + Qt 工具链，随包 Qt 运行库，LGPLv3 合规 | ❌ 为一个面板引入整套 Qt |
| Fyne | v2.8.3 | 有，但底层就是 fyne-io/systray，同款泄漏且每次刷新重建菜单 | cgo + GL，12–40MB | ❌ |
| Gio | v0.10.3 | 无 | 无窗口定位 API | ❌ |
| lxn/walk | 2021 停更 | 有 NotifyIcon | 无 DWM | ❌ |
| 自研原生（ObjC + Win32 + godbus） | 自己维护 | 完全可控 | 零新增运行时 | ✅ 作为底座的扩展手段 |

### 5.3 参考产品怎么做

| 产品 | 形态 | 启示 |
|---|---|---|
| Docker Desktop | Chromium 自绘的仿原生菜单（Electron） | 出过菜单点不动、改版后图标消失 |
| Tailscale | 原生菜单（Linux 用 fyne-io；Windows 形态未核实）；macOS 界面含设备 / 出口节点 | 升级后菜单栏项消失，用户连退出都做不了 |
| Clash Verge Rev | 原生菜单（Tauri 2） | 移除托盘网速被追问；Linux 左键无响应 |
| Mullvad | 右键原生菜单 + 左键主窗口（Electron） | 菜单极简：Open / Connect / Disconnect / Quit；忽略双击 |
| Cloudflare One Client | 2026 重做为窗口应用 | 砍掉统计与分流入口、macOS 无法退出，口碑翻车 |
| PowerToys | 精简进程持托盘 + **常驻**独立面板进程 | 结构同构，但不是「点击才拉起」 |
| EarTrumpet / TranslucentTB / Proton VPN | 左键富浮窗 + 右键原生 | 浮窗都是启动即建；TranslucentTB 保留 HMENU 回退 |
| NetBird | 主窗口 + Go 托盘（Wails v3 beta + React） | 因「纯托盘让人以为没在运行」而重写；双击没反应被当成崩溃 |
| GUI.for.SingBox | Wails + Vue 3 + Go 托盘 | Go 托盘 + Vue 面板可行 |
| Stats / 1Password 7 | 菜单栏图标 + 左键弹窗图表（Swift / AppKit；iStat Menus 技术栈未核实） | 1Password 8 砍掉迷你窗后被追念 |
| Surge Mac | 原生菜单 + Panels 子菜单信息卡（闭源原生） | 重内容放独立窗口 |
| Little Snitch Linux | 只做 Web UI，不做托盘 | 「Linux 不做面板」的旁证 |

材料里口碑较好的托盘，共同特征是：**图标即状态**；**左键面板 / 右键菜单双形态**；首屏给「状态行 + 主操作」；用分组和勾选表达模式；图标克制且同组统一；实时数据点到为止；保留清晰的退出；**不假设图标可见**；与系统材质一致；**重绘不砍功能**。

---

## 6. 推荐方案

### 6.1 路线取舍

| 路线 | 能得到什么 | 鲁棒性 | 工作量 | 结论 |
|---|---|---|---|---|
| A 原生精致菜单 | macOS 精致化、Windows 观感基本不变、Linux 从无到有 | ↑（1a/1b）；1c 逐项评估 | 大（Linux 可单独拆出延期） | 作为底座 |
| B = A + 左键富面板（独立辅助进程） | Windows 左键面板 | 持平（隔离，不提升） | 大到很大 | 第二步，选做 |
| C 整体迁桌面框架（Wails / Tauri / Qt） | 三端同一套自绘 UI | ↓↓ | 很大 | 不推荐 |

### 6.2 分步计划

| 步骤 | 内容 | 是否改变行为 | 放行闸 |
|---|---|---|---|
| **0 测试闸与前置修复** | CI 三平台跑 go test（至少 ./app/tray/ ./internal/ipc/ ./internal/singleinstance/ ./cmd/，Windows 专属测试只能在 windows runner 跑）；修 IPC 客户端（请求级互斥 + 响应 ID 校验 + 读写 deadline）；先落一个「假后端 + 调用序列快照」测试 | 否 | 现有测试全绿并进 CI |
| **1a 纯加固** | 修 macOS autorelease 泄漏（§1.4）；变更门控推广到 macOS 与 TrayApp（先修现有门控缺口：点击路径出错的界面变更必须回写签名）；把 IPC 连接、start_http、agent 看护从 onReady 解耦并加就绪超时；退出同步注销图标；macOS 设 `autosaveName`、启动置一次 visible；companion 自启动与监管 | 否 | 三端行为与旧版逐项对照一致；泄漏曲线平坦 |
| **1b 换底座** | Windows / Linux 切到内嵌 fyne-io v1.12.2，重放补丁（§6.8）；macOS 按 fyne-io 的 darwin 内部接口重写胶水并扩展现有 .m；旧 getlantern 后端与新后端用构建标签或开关并存 | 否 | 与旧版逐项行为对照；§6.9 验收 |
| **1c 增强** | macOS 模板图 / 分组标题 / 副标题 / 徽标；图标状态变体；动作失败反馈；退出语义；菜单文案；Windows explorer 重试；Linux companion（是否做单独拍板） | **是** | 逐项独立开关、独立验收，逐项写明「用鲁棒性换了什么、兜底是什么」 |
| 可选增强 | Windows v4 协议、GUID；图标随 DPI 重载；托盘旁文字 | 是 | 仅当确有悬停 / 键盘事件需求时才做 v4 |
| **阶段 2 面板** | §6.7 | 是 | PoC 先行 |

工作量判断：阶段 1 = 大（Linux 可拆出单独延期）；阶段 2 = 大到很大。对照本仓历史，仅「内嵌补丁 + 变更门控」就是一次事故修复批次。

**回滚**：旧后端保留一个发布周期，出问题用构建开关切回，而不是只能 git revert 加重新出包。注意托盘改版必须重装 MSI，而 MSI 重装会冲掉带外配置的服务恢复动作（2026-11-24 的事故同因）——出包前先把恢复动作与 companion 自启动收进安装器。

### 6.3 架构

```
            ┌──────────────── 同一个 aliang 二进制 ────────────────┐
 core（root / SYSTEM 服务：LaunchDaemon / Windows 服务 session 0 / systemd）
      ▲ IPC（命名管道 / unix socket，沿用既有 action）
      │
 companion（用户会话常驻，保持极小极稳）
   ├─ 统一菜单模型  TrayModel{ Icon, Tooltip, Sections[] } + Apply(diff)
   │     三入口共用一份；保留 2 秒轮询与既有退避参数，只做 diff 应用
   ├─ 平台后端
   │     macOS ：自有 cgo ObjC（statusItem.menu 系统托管）+ 模板图 / 分组标题 / autosaveName
   │     Windows：fyne-io Win32 底座 + 补丁 + explorer 重试
   │     Linux  ：fyne-io 纯 Go D-Bus（SNI + DBusMenu），无 GTK
   └─ 既有看护循环原样保留，但不再依赖 onReady 才启动

 panel（可选，阶段 2：点击才拉起、关闭即退出、崩溃隔离）
   └─ WebView 面板（Wails v3）：不暴露任何业务绑定，只允许导航到 core 的回环源
        Windows：左键弹出     macOS：默认不做     Linux：不做，退回「打开仪表盘」
```

- **事件模型**：回调只把事件投进有界队列，处理器统一在 worker goroutine 执行，绝不在 UI 线程做 IPC、等待或拉进程。按动作合并（Start / Stop / Restart 在途时丢弃同类）。菜单项保留 `ClickedCh` + 每项常驻接收 goroutine，读 channel 必须 ok 判定。
- **「状态不变 → 0 次 UI 调用」要写成单测并进 CI。**
- **面板的二进制形态**：同一源码树，Windows / macOS 用同一个 aliang 二进制的子命令（不新增文件）；Linux 用构建标签把 Wails 整体排除——Wails 的包级 init 会 `LockOSThread`、设环境变量，cgo 版还依赖 gtk4 / webkitgtk-6.0，绝不能进服务器二进制。不变量：主二进制在 Linux 的依赖闭包里不得出现 Wails。

### 6.4 各平台落地形态

**菜单模型要定义的事**：

- **不可用的项是隐藏还是置灰**：两家规范相反——Apple 要求禁用而不是隐藏，Windows 指南要求移除不适用项。建议 TrayModel 定义「unavailable」态并按平台渲染：macOS 保留并禁用（副标题说明原因），Windows 隐藏。AI 分区标题在未运行时保留并置灰，让功能可被发现，顺带消灭悬空分隔线。
- **图标状态矩阵**（运行 / 停止 / 需注意 / 不可用）：macOS 用模板字形；Windows 用预生成的「基础图 + 角标」ICO 变体，不做动画；Linux 用 Status=NeedsAttention + AttentionIcon。触发条件至少覆盖：未登录、core 不可达、上次启动失败。
- **模式切换**：代理运行中把模式项置灰并说明「停止代理后可切换」，或改成显式的「切换并重启」——消除现在的一键断网陷阱。
- **动作失败反馈**：失败原因落到状态行 / 副标题或桌面通知，点击后立即回写。
- **退出语义**：拆成两个自解释的项（「退出托盘（后台服务继续）」/「停止代理并退出」），三端统一；被退出闸否决时给可见提示。
- **双击**：等同单击（打开并保持），或干脆忽略（Mullvad 的做法）。
- **未登录态**：把置灰的 Start 换成可点的「登录后启动…」直接打开仪表盘。
- **菜单语言**：现在是英文夹两句中文。TrayModel 集中管理文案，先做中英两套，跟随系统语言。
- **实时数据**：菜单打开期间以约 1Hz 刷新一行速率（macOS / Linux 有 TrayOpenedCh，Windows 需自补 WM_INITMENUPOPUP），不动图标。
- **面板为纯指针交互**，所有操作在原生菜单里都有等价项（键盘可达）。

**macOS**：默认「点击 = 原生菜单」。分组标题 / 徽标 14.0+、副标题 14.4+，12–13 回退为分隔线 + 禁用标题行。所有新增 ObjC 调用做 `respondsToSelector` / `@available` 双守卫；本地构建脚本同样钉 `MACOSX_DEPLOYMENT_TARGET=12.0`，CI 用 `vtool -show-build` 断言 minos==12.0（否则本机能跑、发布包在 12/13 上一打开菜单就崩）。

**Windows**：第 1 步左键仍然弹菜单（不注册 tap 回调）；「左键 = 面板」整体放到第 2 步并做成设置项。图标默认在溢出区，需要首启引导（深链 `ms-settings:taskbar`）。

**Linux**：见 §6.6。

### 6.5 macOS 左键面板的取舍

挂了 NSMenu 的状态项拿不到单击动作，27 的 expandedInterfaceDelegate 也只给不挂菜单的状态项。所以「留在系统托管菜单」和「左键面板」在 macOS 上互斥，三选一：

| 选项 | 代价 | 建议 |
|---|---|---|
| (a) 只做原生精致菜单，不做面板 | 无新增风险，合 HIG | **默认** |
| (b) 用户在设置里打开「左键面板」才摘掉 statusItem.menu，改 button action 分发 + 右键程序化弹菜单 | 正是 §4.2 里 Apple 不再推荐的本地事件监视器手法；≤26 要自管焦点、高亮、点窗口外关闭；27 有右键失效报告；WebView 透明要 `-tags private_mac_apis`（私有 API，公证影响无一手结论） | 仅作实验开关 |
| (c) 仅 27+ 走 expandedInterfaceDelegate，≤26 回退 (a) | 覆盖面窄；无法编程化激活会话；触摸「点窗口外」无检测手段 | (b) 之后的增量 |

### 6.6 Linux：净新增，六个设计点要先定

Linux 不是「三入口收敛后复用」，而是第四份入口逻辑：

1. **服务探测**：现有 `companion.go` 的构建标签是 `!windows`，Linux 也会编入，但行为是 macOS 专属——依赖 launchd 的三个调用在非 darwin 上是恒 false 的 stub，直接复用会永远落到「core service not installed」。改为 `systemctl is-active` 或直接以 IPC 可达为准。
2. **dashboard 归属与 Quit 语义**：Linux core 有测试钉死的策略「无托盘 → core 自起 dashboard」，deb 的桌面启动器就是 `xdg-open 127.0.0.1:56431`。companion 的 Quit 会发 `stop_proxy` + `stop_http`，点一下就停代理并关掉启动器依赖的 dashboard。Linux companion 退出时不应发这两个动作。
3. **单实例守卫**：127.0.0.1:56430 是机器级的，Linux 上 `aliang start`（裸 `aliang` 的默认根命令）与它共用。companion 占了守卫，用户的 `aliang start` 静默退出，反之亦然。需要独立锁或明确的互斥规则。
4. **agent 看护范围**：companion 启动即 `EnsureStarted` 拉起 user-agent 并带看护循环。Linux 今天是 root core 刻意跳过 spawn，新增 companion 等于把设备互访 / AI 能力的 agent 自动带进 Linux 桌面，属于产品范围扩张，要单独拍板。建议首版不接。
5. **IPC 鉴权**：unix socket 被 chmod 0666，协议无鉴权，`shutdown` / `start_proxy` / `switch_mode` 对任意本地用户开放。新增用户会话 → root core 的消费者之前，应先做对端校验（SO_PEERCRED）或收窄权限。
6. **autostart 的交付载体**：CI 的 Linux 发布物是只含裸二进制的 tar.gz；deb 在 GitHub Release 里从未出现。需要 `aliang companion --install-autostart` 之类写 `~/.config/autostart` 的路径，或先修 deb 的上传路径。

另外三条约束：

- 内嵌副本把 `LockOSThread` 从包级 init 收进 darwin 专属文件（Windows 挪到 `Run()` 入口，Linux 不需要）——今天 Linux 的 core / agent / CLI 根本不链接托盘包，别让服务器二进制第一次带上这个副作用。验收：Linux 发行物 `readelf -d` 的 `DT_NEEDED` 仍仅 libc，core / agent 启动时间与线程数不变。
- 「无宿主就降级」要写明三种情形（无会话总线 / 有总线无宿主 / 运行中宿主消失）各自「是否启动看护、是否常驻、提示方式、日志落点」，并明确 ItemIsMenu=true（左键处处出菜单）。
- Linux 证据面最窄：只在 xfce4-panel 4.18.2 + Xvfb 上实测过，菜单打开期间发 LayoutUpdated 是否闪烁 / 关闭未测。

### 6.7 面板辅助进程：PoC 实测与前置问题

**实测**（macOS 26.2 / Apple Silicon，Wails v3.1.0，隐藏窗口，仓库外 /tmp/tray-poc）〔实测〕：

| 指标 | 空页面 | 真实仪表盘包（app/website/dist，无后端） |
|---|---|---|
| 冷启动：进程启动 → 导航完成（5 次） | 中位 363ms（340–550） | 中位 473ms（409–549）；另一批 612ms（572–911） |
| 稳态进程树（宿主 + GPU + Networking + WebContent，共 4 个进程）footprint / RSS 之和 | 66MB / 173MB | 146MB / 336MB（瞬时峰值 footprint 199MB） |
| `Close()` 之后 7 秒内 | 三个辅助进程一个不退，内存不降 | 同左 |
| 同进程内第二次打开 | 134ms | 268ms |
| 对照：只挂托盘、不开窗口 | footprint 17MB / RSS 78MB | — |

局限：隐藏窗口不出帧，这是可见面板的下界；终点是「导航完成」而不是「可交互」；没有测 Windows（WebView2）与 Linux。

**对设计的含义**：

1. **面板代价与托盘代价不在一个量级**（差 4–9 倍，还多 3 个不受自己控制生命周期的系统进程）。WebView 不能进常驻 companion。
2. **「关闭」在 Wails v3.1.0 里不等于「回收」**，内存只能靠进程退出回收。所以面板辅助进程必须是「失焦 / 关闭 → 进程退出」。
3. **不预热**：常驻一个隐藏窗口要 66–146MB，换每次省 0.2–0.3 秒，而且隐藏窗口根本没渲染好。
4. 冷启动到导航完成 0.5–0.9 秒，到可交互还需另测。首版加载态按 1 秒设计。
5. 托盘底座不用 Wails：只挂托盘的 Wails 进程 RSS 是现 companion 的两倍多，没有内存收益。

**这个形态没有先例**：PowerToys、EarTrumpet 都是常驻面板进程；「每次点击冷启动一个 WebView 进程并退出」在知名开源项目里找不到，属于自证项。

**PoC 之前要先定的**：

- 左键决策顺序：先看 IPC 状态，core 不可达就弹原生菜单或提示后台服务不可用，不拉面板。
- WebView2 可用性由 companion 启动时探测一次并缓存，缺失就走浏览器。
- 面板单实例与点击防抖（companion 记录子进程）。
- 前台交接（挂起创建，`AllowSetForegroundWindow(子进程 PID)`，恢复）。
- 面板放进 KILL_ON_JOB_CLOSE 的 Job 对象；companion 对面板零同步依赖（只异步 Wait），面板挂死不能拖住托盘。
- companion 退出时面板随之结束，不留孤儿。
- 导航白名单：只允许 core 回环源。Wails 即使不绑定任何 Go 方法也会在每次导航后注入自己的运行时（`window._wails`），「不开 JS 桥」的说法不准确，准确说法是「不暴露任何业务绑定」。
- 面板访问的是 core 的回环 `/api`，回环来源免 cookie 即完整控制权——面板等于一个无鉴权控制台，不得加载任何第三方内容。

**面板里放什么**：状态 / 启停 / 模式 / AI 开关，原生菜单都能放；真正只有面板才放得下的是实时速率图表和设备互访列表。

### 6.8 内嵌 fyne-io 的补丁清单

沿用现有 PATCH_NOTES 的纪律（逐字节忠实于上游 + 仅功能补丁 + `本仓补丁` 注释锚点）：

| 补丁 | 平台 | 性质 |
|---|---|---|
| 3 处 GDI 补丁（`DeleteObject`、`iconToBitmap` 失败路径、覆盖前销毁旧 HBITMAP） | Windows | 必须，照搬 |
| `NOTIFYICONDATA` 的 union 写成两个 uint32、cbSize 错位（#133） | Windows | 必须 |
| 弹菜单补发 `WM_NULL`、带 `TPM_RIGHTBUTTON` | Windows | 顺手 |
| 首次 `NIM_ADD` 有界退避重试，失败把错误返回调用方（#95） | Windows | 必须 |
| 9 个入口加 `@autoreleasepool` | macOS | 必须（§1.4） |
| `LockOSThread` 移出包级 init | 全部 | 必须 |
| `stayRegistered` 遇到短信号就永久退出（#131，修复 PR #132） | Linux | 必须——否则笔记本合盖恢复后面板重启后图标悄悄消失 |
| ARGB 像素转换用 `byte()` 截断（#92，修复 PR #93） | Linux | 必须 |
| `nativeEnd` 对 nil 连接调 Close（master 已修，未发版） | Linux | 必须 |
| darwin 内部接口胶水：`add_separator` 多了 parentId，另需 `remove_menu_item` / `reset_menu` / `show_menu` / `nativeStart` / `nativeEnd` / `setRemovalAllowed` / `setInternalLoop` 及三个新的 Go 导出 | macOS | 重写——等于在 fyne-io 包内长期维护第三个平台后端，每次同步上游都要重新适配这些私有符号 |

v4 协议与 GUID 不在这张清单里：左键动作 + 右键菜单在 v0 协议下就能做，v4 只服务悬停与键盘事件，而迁移它要同时处理 `WM_CONTEXTMENU`、补 `NIF_SHOWTIP`、每次 `NIM_ADD` 后重发、左右键去重、键盘 `NIN_KEYSELECT`；改 GUID 会换掉图标身份，本仓 Windows 不做代码签名，老用户的「始终显示」提升偏好可能失效、图标掉回溢出区。

### 6.9 验收口径

分平台写死通过条件：

- **Linux 发行物**：`readelf -d` 的 `DT_NEEDED` 仍仅 libc；core / agent 的启动时间与线程数不变。
- **Windows**：
  - 脚本驱动 1 万次状态翻转、500 次菜单开关、20 次 explorer 重启、DPI 切换、锁屏睡眠解锁之后，`GetGuiResources` 的 GDI / USER 增量为 0。
  - 登录竞态下图标与菜单最终出现，且看护循环已启动。
  - 退出后无残留图标；退出闸三态不变；Win11 125% / 150% 缩放下菜单可见。
- **macOS**：companion RSS 与 footprint 24 小时平坦（泄漏曲线）；关掉「允许在菜单栏中」后仍有可达入口；LaunchAgent 下 Quit 后不复活。
- **Linux 桌面**：重启面板与挂起恢复各 10 次后图标仍在；支持矩阵内的桌面各跑一遍。
- **面板**：连点只出一个实例；kill / 挂死 / 泄漏不影响托盘；core 不可达与 WebView2 缺失有降级；点击到导航完成 p50 ≤ 0.7s、p95 ≤ 1.2s（本机 Apple Silicon）；打开时进程树 footprint ≤ 150MB。
- **通用**：状态不变时 0 次 UI 调用（单测，进 CI）；处理 start / stop / restart 期间连点其它项不丢、不卡；任何一次失败的点击 1 秒内在菜单或通知里可见原因；看护节奏参数不变（10s / 3 次 / 5min / 15min / 1h / 30s）。
- **灰度**：先在自有机器浸泡不少于 7 天（注意 4070 处于 WER 取证态且 companion / agent 是你的红线，需另选机器），再分批放量。

### 6.10 与选型无关、现在就能做的

按性价比排序：

1. **修 macOS 泄漏**（§1.4）：约 20 行，立刻止住 2GB/天。
2. **重启本机 companion**：回收约 5.8GB。
3. **IPC 客户端**加请求级互斥、响应 ID 校验、读写 deadline：几十行，`internal/ipc` 已有测试注入口。
4. **CI 加 go test**：几十行 YAML，是后面所有托盘重构的保险。
5. **变更门控推广到 macOS 与 TrayApp**（先修门控缺口）。
6. **把 IPC 连接、start_http、agent 看护从 onReady 解耦**，加就绪超时：消除登录竞态下的僵尸 companion。
7. **macOS 设 `autosaveName` + 启动置一次 visible；LaunchAgent 改 `KeepAlive{SuccessfulExit=false}`**：几行，解决「被移除后不回来」和「Quit 被拉回」。
8. **companion 自启动与监管、MSI 自带服务恢复动作**：RUN-11 已列为 P1 待评估。
9. **修 CI 的 deb 上传路径**：一行，否则 Linux autostart 没有交付载体。
10. **Windows 第二次启动 companion 时不再静默退出**，而是通知现有实例打开仪表盘，让「图标之外的入口」真正可用。
11. **观感快赢**（不必等底座迁移）：macOS 用 `SetTemplateIcon` 换模板图；状态行改成人类可读并统一文案；未登录态改成可点的「登录后启动…」；模式项运行中置灰；Windows 给 Start / Stop 加粗为默认命令。

### 6.11 风险与对策

| 风险 | 对策 |
|---|---|
| fyne-io 的已知未修缺陷 | 内嵌即自管，补丁清单见 §6.8；补丁清单、上游同步节奏与责任人要写进 PATCH_NOTES |
| macOS 27 变化多（图标隐藏、允许在菜单栏、MenuBarAgent） | 不靠图标承载信息；设 `autosaveName`；保留图标外入口；CI 升到 27 SDK 时同步 `preferredImageVisibility` 补丁（fyne PR #138 可作范本） |
| 同源换版仍可能出回归，而 Windows 专属逻辑开发机不可测 | 步骤 0 的测试闸 + Windows 真机回归清单 + 旧后端保留一个周期 |
| WebView 面板依赖的 Wails v3 刚稳定，形态无先例 | 面板进程隔离；钉死版本；面板永远可选；PoC 先在 Win11 / Win10 + WebView2 上量「点击 → 可交互」与内存 |
| 面板冷启动延迟 | 不预热；首版加载态按 1 秒设计；实测后再定 |
| Linux 桌面碎片化 | 明确支持矩阵（KDE / XFCE / MATE / Budgie，GNOME 需扩展）；不做面板；Linux 证据面最窄，先在目标桌面实测 |
| 三份重复代码再次不同步 | 步骤 1 的 TrayModel 要消灭的正是它 |
| companion 无监管 | §6.10 第 8 条，列为阶段 1 前置 |

---

## 7. 需要你拍板

1. **面板里到底要放什么？** 这决定第 2 步值不值得做。只有实时速率图表和设备互访列表是原生菜单放不下的；其余都能留在菜单里。
2. **「炫酷」对你来说是哪一档？**（按平台看，因为能力不同）
   - macOS / Linux：原生精致菜单（分组、图标、模板图、状态徽标）。Linux 上还受宿主限制。
   - Windows：原生菜单，还是「左键面板 + 右键菜单」？
3. **Linux 桌面托盘做不做、支持到哪？** 净新增。KDE / XFCE 优先？GNOME 需要装扩展是否可接受？Linux companion 是否带 agent 看护？
4. **要不要我现在就把 macOS 泄漏补丁落下去？**（在 worktree 里做，带复现用例；约 20 行。）
5. **被 stash 的那份「恢复菜单项图标」改动要不要找回？** 它恢复的是旧代码的 provider 菜单项图标；在 macOS 上它正是泄漏放大 6 倍的路径（每 tick 多解码 4 张图），补了 pool 之后可以恢复。
6. **Windows 上的 `aliang tray` 单进程路径还留不留？** 不留可以少维护一份代码。
7. **图标从彩色品牌图变成单色模板图**（macOS）是品牌层面的决定，需要你点头。

---

## 8. 调研方法与置信度

- 第一轮 20 个子代理：3 个读码 + 7 个联网调研 + 10 组对抗核查。第二轮 5 个代理：3 位评审 + 1 名事实核查员 + 1 个离屏 PoC。
- 共 208 条结论，其中 138 条标为承重。70 条进入对抗核查，56 条有结论（34 条证实、21 条细节有误已更正、1 条被证伪），14 条未能判定。R4–R6（WebView 外壳、Go 原生工具箱、参考产品）与三份读码材料**未经对抗核查**——阶段 2 所依赖的 Wails 结论均在这部分，其中库状态由主控用 GitHub API 逐条复核过。
- 被纠正的典型例子：「tray.go 是 Linux 主路径」（实为报错桩）；「Windows 后端用 lxn/walk」（实为纯 Win32）；「Plasma 6 不再支持 XEmbed」（错，靠 xembedsniproxy 桥接）；「macOS 27 beta 上 hover 事件失效」（dev beta 5 已修，但高亮失效与 occlusionState 失效仍未解决）；「第 1 步只升鲁棒性」（含行为变更）；「PowerToys 是点击才拉起」（实为常驻）。
- 本机实测项：macOS companion 泄漏（线上采样 + 6 组受控实验）；Wails v3.1.0 面板辅助进程开销（离屏）；cgo + GTK 二进制加载期失败（Docker）；fyne-io 静态二进制在 Xvfb + xfce4-panel 注册成功；macOS 状态项换图 CPU；官方包 SDK 版本。
- **未验证或单源的项**：Windows 11 上 GetRect 的屏外坐标；Win11 经典菜单圆角与「无亚克力」；WebView2 首实例约 250MB（微软工程师 2021 年口径）；macOS 27 的全部运行期行为（本机是 26.2）；DWM 系统背景材质对无边框 WS_POPUP 是否生效；Wails `private_mac_apis` 对公证的影响；闭源产品（Surge、Proxyman、iStat Menus、Raycast）的内部实现。
- 两个静态阅读结论未复现：viewKey 门控缺口、IPC 客户端并发隐患。

---

## 附录 A：现存隐患清单

多数不是库的锅，但换托盘时一定会撞上（第 4、5、6 条本身是库行为）：

1. macOS companion / TrayApp 没有变更门控：状态完全不变也每 2 秒刷一批 UI。
2. viewKey 门控有缺口（Windows，静态阅读结论）：点击路径绕过门控且不回写签名。
3. IPC 客户端没有请求级互斥、不校验响应 ID、连接没有 deadline。
4. 包级 init 调 `LockOSThread`：凡链接该包的二进制主 goroutine 都被锁到线程。fyne-io 同样如此。
5. Windows 首次 `NIM_ADD` 失败只打日志不重试；而两个 companion 的 IPC 连接、start_http、agent 看护循环全在 onReady 里启动——onReady 不来，就是一个没有托盘、没有看护、却握着单实例锁的僵尸进程，用户再点快捷方式只会静默退出（fyne-io #95 报告登录时约半数复现）。
6. 退出时 `systray.Quit()` 后立即 `os.Exit`，`NIM_DELETE` 很可能不执行，留下幽灵图标（未实机复现）。fyne-io 的 `quit()` 会同步执行 `NIM_DELETE` 与 onExit，换底座后这条自动改善。
7. macOS LaunchAgent `KeepAlive=true` 下点「Quit Aliang」会被 launchd 立即拉回（plist 语义推演，未实机验证）。
8. Windows companion 没有仓库内的自启动与监管；服务恢复动作是带外手工配置的，MSI 重装会冲掉。
9. 文档与 CI 残留：`docs/tray-usage.md`、`app/tray/README.md` 仍写 Linux 托盘、Run 注册表自启、`nonelane`；CI 的 Linux 构建仍在装 GTK dev 包；deb 的上传路径与产物名不匹配，GitHub Release 里从未出现。
10. SDK 版本：官方 macOS 包链接的是 15.5 SDK，拿不到 Tahoe / 27 的新外观；本机 local 包 minos=26.0，与 Info.plist 的 12.0 不一致。
11. 系统登出 / 关机时进程内 TrayApp 被终止，系统代理 / TUN 残留未处理。
12. companion 是 windowsgui 构建，原生回调崩溃默认无痕；面板进程需要同样的落盘取证。
13. Windows 上 beeep 走 tadvi/systray 另起临时托盘图标弹气泡，与本仓托盘是两套 `Shell_NotifyIcon`。

## 附录 B：迁到 fyne-io/systray v1.12.2 的注意清单

| 现用 | fyne-io v1.12.2 |
|---|---|
| `Run(onReady, onExit)`、`Quit`、`SetIcon`、`SetTooltip` | 同名 |
| `AddMenuItem`、`AddMenuItemCheckbox`、`AddSeparator` | 同名 |
| `MenuItem.SetTitle / SetTooltip / Enable / Disable / Hide / Show / Check / Uncheck` | 同名 |
| `MenuItem.ClickedCh` | 同名，仍是无缓冲的非阻塞投递 |
| — | 新增：`SetOnTapped`、`SetOnSecondaryTapped`、`TrayOpenedCh`、`MenuItem.Remove`、`AddSubMenuItem` |

- 模块路径变为 `fyne.io/systray`；仍需 `replace` 指向内嵌目录并带 §6.8 的补丁。
- `SetOnTapped` 的回调在 UI 线程里同步执行，且回调变量无锁读写：回调里只能入队。
- `MenuItem.Remove()` 会 `close(ClickedCh)`：本仓三处 `handleMenuEvents` 都是 `for { select { case <-ch: … } } }`，对已关闭的 channel 会立即就绪并空转、反复执行处理器。读 channel 必须 ok 判定。第 1 步保留「onReady 预建 + 原地改属性」，Remove 只用于启动期；菜单正被显示时增删 HMENU 属未定义行为，而 Windows 拿不到菜单开闭事件。
- Linux：onReady 早于 D-Bus 连接，不能当成功信号；GetLayout 会等 onReady 返回，初始菜单必须在 onReady 内同步建完；勾选框点击后 `toggle-state` 不会自动翻转；label 里的首个下划线会被当访问键；每次 Hide / Show 同时发 ItemsPropertiesUpdated 与 LayoutUpdated。
- Windows：`TrayOpenedCh` 不触发；tooltip 约 63 字符截断（结构体同款，需补）。
- macOS：菜单项图标在 27 SDK 下默认隐藏（PR #138 未合并，需自己补）。
- 顺带可以去掉：无用的 `-framework WebKit` 链接，以及 getlantern/golog 及其传递依赖。

## 附录 C：关键来源

**库与框架**
- getlantern/systray：https://github.com/getlantern/systray （tags、commits、issues 经 GitHub API）
- fyne-io/systray：https://github.com/fyne-io/systray ｜ https://raw.githubusercontent.com/fyne-io/systray/v1.12.2/systray_windows.go ｜ https://raw.githubusercontent.com/fyne-io/systray/v1.12.2/systray.go ｜ https://raw.githubusercontent.com/fyne-io/systray/v1.12.2/systray_unix.go ｜ https://raw.githubusercontent.com/fyne-io/systray/master/systray_darwin.m ｜ PR #138、#140、#132；issues #95、#131、#133、#92
- Tailscale 的 Linux 托盘：https://github.com/tailscale/tailscale/blob/main/client/systray/systray.go
- gogpu/systray：https://github.com/gogpu/systray ；energye：https://github.com/energye/systray ；NordSecurity：https://github.com/NordSecurity/systray ；kolide：https://github.com/kolide/systray
- Wails v3：https://github.com/wailsapp/wails/releases/tag/v3.1.0 ｜ issues #6505、#6375、#6238、#6327、#6154 ｜ https://github.com/wailsapp/wails/security/advisories
- Tauri tray-icon：https://github.com/tauri-apps/tray-icon/blob/tray-icon-v0.27.0/src/lib.rs
- miqt：https://github.com/mappu/miqt ；Qt LGPL 义务：https://www.qt.io/licensing/open-source-lgpl-obligations
- webview_go：https://github.com/webview/webview_go ；darwinkit：https://github.com/progrium/darwinkit ；purego：https://github.com/ebitengine/purego ；windigo：https://github.com/rodrigocfd/windigo

**平台规范与文档**
- Apple HIG：https://developer.apple.com/design/human-interface-guidelines/the-menu-bar ｜ https://developer.apple.com/design/human-interface-guidelines/menus
- NSStatusItem.menu：https://developer.apple.com/documentation/appkit/nsstatusitem/menu
- macOS 27 Release Notes：https://developer.apple.com/documentation/macos-release-notes/macos-27-release-notes
- NSStatusItem expandedInterfaceDelegate：https://developer.apple.com/documentation/appkit/nsstatusitem/expandedinterfacedelegate
- WWDC26 Session 289：https://developer.apple.com/videos/play/wwdc2026/289
- Windows 通知区：https://learn.microsoft.com/en-us/windows/win32/shell/notification-area ｜ https://learn.microsoft.com/en-us/windows/win32/uxguide/winenv-notification
- Shell_NotifyIconW / NOTIFYICONDATAW：https://learn.microsoft.com/en-us/windows/win32/api/shellapi/nf-shellapi-shell_notifyiconw ｜ https://learn.microsoft.com/en-us/windows/win32/api/shellapi/ns-shellapi-notifyicondataw
- 圆角：https://learn.microsoft.com/en-us/windows/apps/desktop/modernize/ui/apply-rounded-corners
- WebView2 分发：https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution
- SNI 规范：https://www.freedesktop.org/wiki/Specifications/StatusNotifierItem/StatusNotifierItem/
- GNOME AppIndicator 扩展：https://github.com/ubuntu/gnome-shell-extension-appindicator
- Syncthing Tray 的 Wayland 已知问题：https://github.com/Martchus/syncthingtray/blob/master/docs/known_bugs_and_workarounds.md
- KDE HIG：https://develop.kde.org/hig/status_changes

**参考实现**
- PowerToys 托盘与面板进程：https://github.com/microsoft/PowerToys/blob/main/src/runner/tray_icon.cpp ｜ https://github.com/microsoft/PowerToys/blob/main/src/runner/quick_access_host.cpp
- EarTrumpet：https://github.com/File-New-Project/EarTrumpet ；TranslucentTB：https://github.com/TranslucentTB/TranslucentTB
- Stats：https://github.com/exelban/stats ；NetBird：https://github.com/netbirdio/netbird/tree/main/client/ui ；GUI.for.SingBox：https://github.com/GUI-for-Cores/GUI.for.SingBox

**仓库内关键位置**
- `third_party/systray/systray_darwin.m`（泄漏点）、`third_party/systray/PATCH_NOTES.md`
- `app/tray/companion.go`、`companion_windows.go`、`tray.go`、`state_gate.go`
- `app/http/services/run.go`（SwitchMode 运行中先停代理）
- `internal/ipc/client.go`、`internal/ipc/transport_unix.go`
- `.github/workflows/tag-build-release.yml`、`scripts/build-msi.ps1`、`scripts/build-pkg.sh`、`scripts/build-deb.sh`

**实验产物（仓库之外）**
- `/tmp/tray-leak/`：泄漏探针、两个补丁变体、结果 JSON、线上进程调用栈采样
- `/tmp/tray-poc/`：Wails v3.1.0 面板辅助进程 PoC
- `/tmp/tray-research/v/`：第一轮调研的 10 份带来源摘要；`/tmp/tray-research/review/`：第二轮评审原文
