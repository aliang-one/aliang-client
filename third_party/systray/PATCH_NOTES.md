# 本仓对 getlantern/systray 的内嵌修补版

## 基线

上游 `github.com/getlantern/systray` **v1.2.2**(2023-03-27 发布)逐字节拷贝,
Apache-2.0(LICENSE 原样保留)。本目录的 go.mod 保持上游 module path 不变,
由主仓 go.mod 的 `replace github.com/getlantern/systray => ./third_party/systray`
接管解析。

## 为什么内嵌(而非升级)

- 上游自 v1.2.2 后 2 年半无发版,master 分支同样带漏——GDI 泄漏上游不会修。
- fyne-io/systray v1.12.2(活跃分支)已验尸:泄漏逐行相同,换分支无解。
- 事故现场:2026-11-20 0.196,companion 托盘 AI 菜单每 2s×4 providers 调
  MenuItem.SetIcon,HBITMAP 泄漏 ~83min 耗尽 10k GDI 配额,托盘图标消失
  (WER GDIObjectLeak ×3 实锤)。
- macOS 侧同病(2026-11-26 定位):`systray_darwin.m` 没有 autorelease pool,
  每次 UI 调用都泄漏 ObjC 对象,上游 #135 自 2020 年开着至今;fyne-io 只给
  两条图片路径加了 pool,其余 7 个入口没有,换分支同样不能全修。

## 相对上游的改动

除本节所列外,其余文件逐字节 = 上游 v1.2.2。所有补丁处均有 `本仓补丁`
注释锚点(升级时 grep 此词重放补丁)。

### systray.go(1 处,2026-11-26)

1. `panelClickFn` + `SetPanelClickHook(fn func())`:面板模式点击回调。设置后
   后端把托盘图标的左/右键点击交给回调(每次点击起新 goroutine
   异步执行),不再弹原生菜单;nil(默认)= 上游行为,原生菜单兜底。读取此
   回调的是 Windows 的 wndProc 与 macOS 的 `systray_tray_clicked`(见下方
   面板胶水小节)。回调契约已写进注释:应快速返回(否则 goroutine 按连点
   次数无上限堆积)、可能并发执行(串行化归接线层)、panic 会终止整个进程
   (无 recover 的 goroutine)。

### systray_windows.go(12 处;1~3=2026-11-20 GDI 修复,4~12=2026-11-26 面板胶水)

1. proc 表新增 `pDeleteObject = g32.NewProc("DeleteObject")`。
2. `iconToBitmap`:`pDrawIconEx` 失败路径上游直接 `return 0`,已创建的
   hMemBmp 无人接管 = 泄漏;补丁在返回前销毁。
3. `MenuItem.SetIcon`:上游覆盖 `wt.menuItemIcons[id]` 前不销毁旧句柄,
   每次调用泄漏一个 HBITMAP(主泄漏点);补丁在覆盖前 `DeleteObject` 旧值。
4. proc 表新增 `pShell_NotifyIconGetRect = s32.NewProc("Shell_NotifyIconGetRect")`
   + 新增 `notifyIconIdentifier` 结构体(NOTIFYICONIDENTIFIER):与库内
   `notifyIconData`(NOTIFYICONDATAW)是两个不同结构体,不得混用;字段名
   保持 Win32 原名(cbSize/hWnd/uID/guidItem)防转录错位。(两处锚点)
5. 新增 `Rect` 结构体:新版 golang.org/x/sys 已不再提供 RECT,库内自备,
   风格同上游的 `point`;导出(而非 `rect`)是因为 TrayRect 返回它——Go
   惯例反对 exported func 返回未导出类型,接线层无法命名。
6. 新增 `HRESULT` 类型 + 哨兵 `errTrayNotInitialized` + 包级 `TrayRect()`:
   拿托盘图标屏幕矩形(面板窗口锚定用)。要点:①cbSize 必须用
   `uint32(unsafe.Sizeof(notifyIconIdentifier{}))` 表达式——该结构 x64
   sizeof=40(已用编译期断言实编译验证),照抄上游字面常量风格写 24 会让
   API 恒 E_INVALIDARG 且被兜底静默掩盖;②Call 是两参形态
   `Call(uintptr(unsafe.Pointer(&idf)), uintptr(unsafe.Pointer(&r)))`
   ——真实 Win32 签名第二参是 `RECT *`(调用方分配好的矩形指针),MSDN
   语法块的 `LPRECT *` 是文档笔误,照抄会传 RECT** 导致脏写;③成功只认
   `ret == 0`(S_OK),S_FALSE(1)= 图标在溢出区(Win10 给溢出箭头矩形 /
   Win11 给屏外坐标),连同已填充的 Rect 原样透传,由调用方判废;④托盘
   未初始化的前置错误用包级哨兵 `errTrayNotInitialized`(可 errors.Is
   判定),与 HRESULT 的类型化错误对称。(三处锚点)
7. `wndProc` 的 WM_LBUTTONUP/WM_RBUTTONUP 分支:`panelClickFn != nil` 时
   `go panelClickFn()`(面板模式),否则落回 `showMenu()` 原生菜单兜底。
8. `showMenu` 两处:①TrackPopupMenu flags 加 `TPM_RIGHTBUTTON`(右键路径
   WM_RBUTTONUP 弹出的菜单也要跟踪右键选择);②TrackPopupMenu 返回后
   `PostMessageW(wnd, WM_NULL, 0, 0)`——经典托盘菜单坑(KB135788):本窗口
   已 SetForegroundWindow,不喂一条无害消息则菜单点外不收起、二次点击
   图标弹不出。(两处锚点)

结构测试:`app/tray/systray_windows_patch_test.go` 的
`TestSystrayWindowsPanelPatch` 钉住 4~11 的形态(含 cbSize 禁止字面常量、
两参 Call、只认 S_OK 三条「照做必错」红线),同步上游漏重放即红。

### systray_darwin.m(9 处,2026-11-26)

9 个 C 入口(`setIcon`、`setMenuItemIcon`、`setTitle`、`setTooltip`、
`add_or_update_menu_item`、`add_separator`、`hide_menu_item`、`show_menu_item`、
`quit`)的函数体各包一层 `@autoreleasepool`,原有行不动,每处 +2 行。

- 根因:这些入口都在调用者 goroutine 所在的线程上创建 ObjC 对象
  (NSData/NSImage/NSString/NSNumber 及图片解码的中间对象,大量是
  autoreleased),之后 `runInMainThread` 才切到主线程。Go 运行时的线程上
  没有 autorelease pool,这些对象永不释放。
- 事故现场:macOS companion 每 2s 一轮同步,且该路径没有变更门控。每次图标类
  调用漏约 6~11KB(随图片内容与尺寸而异):去图标化之前每轮 5 次图标解码,
  线上实测约 2GB/天;去掉 provider 菜单项图标后每轮 1 次,按探针数据折算约
  280~380MB/天,仍在漏。RSS 只有 ~32MB——泄漏页全被系统压缩,要看
  `footprint` 的 MALLOC_SMALL,看 RSS 发现不了。
- `quit` 不创建对象,包进 pool 只为 9 个入口形态统一。

### systray_darwin.m / systray_darwin.go / systray_nonwindows.go / systray.h(6 处,2026-11-26 面板胶水 Task 4)

macOS 底座补上与 Windows 侧同构的面板胶水(托盘面板调研 §6.5 的机制):
`SetPanelMode(true)` → `statusItem.menu = nil`(摘菜单)+ `button.target/action
= @selector(trayClicked:)` + `sendActionOn`(左/右键按下都触发)→ 点击经
`systray_tray_clicked()` 交给 Go 侧 `panelClickFn`(自绘面板);`SetPanelMode(false)`
→ 摘 action + `[statusItem setMenu:menu]` 接回(原生菜单兜底)。**菜单项对象与
companion 的刷新循环完全不动**——面板模式期间菜单对象照常被更新(不可见而已),
随时无损接回。

1. AppDelegate 新增三个方法(接口声明带注释锚点,四处锚点):`setPanelMode:`
   (主线程执行,两态切换如上;false 侧另把按钮掩码恢复为默认的抬起触发)、
   `trayClicked:`(转发 `systray_tray_clicked()`,Go 侧起新 goroutine 异步执行,
   不在 AppKit 主线程做耗时工作)、`fillTrayRect:`(窗口 frame 装箱回传;
   macOS 的 NSScreen 全局坐标系是左下原点,经主屏高度换算成与 Windows 同基准
   的全局左上原点——主屏 = `[NSScreen screens]` 首个,带菜单栏的那块,全局
   坐标零点在其左下角)。
2. 新增 C 入口 `set_panel_mode(bool)`:经既有 `runInMainThread` 模式封送
   `setPanelMode:`(照 `setIcon` 等入口写法);会被 goroutine 直接调用,函数体
   整体包 `@autoreleasepool` 同其余 9 入口的纪律。(两处锚点)
3. 新增 C 入口 `tray_rect(int* x, int* y, int* w, int* h, bool* ok)`:取
   `statusItem.button.window.frame`,经主屏高度换算为全局左上原点,装箱进
   `NSArray` 回传;窗口 frame 只能主线程读,同样经 `runInMainThread` 同步封送;
   包 pool 同上。(两处锚点)
4. `systray_nonwindows.go` 新增 `//export systray_tray_clicked`:与 Windows 侧
   wndProc 点击分支对齐——非阻塞地 `go panelClickFn()`,hook 为 nil 则忽略。
   (一处锚点)
5. `systray.h` 新增三处声明:`extern void systray_tray_clicked()`、
   `set_panel_mode`、`tray_rect`。export 三处分工按仓内惯例(与既有
   `systray_menu_item_selected` 同款):Go 侧持 `//export` 实现、systray.h 持
   extern 声明、.m 持调用点。(两处锚点)
6. `systray_darwin.go` 新增 `SetPanelMode(bool)` 与 `TrayRect() (x, y, w, h int,
   ok bool)` 包装;darwin 侧是整数四元组、Windows 侧是导出 `Rect` 类型,两种
   形态并存是既定设计(见 `systray_windows.go` 的 TrayRect)。(两处锚点)

结构测试:`app/tray/systray_darwin_patch_test.go` 的
`TestVendoredSystrayDarwinPanelPatch` 钉住形态(两态切换/摘菜单接回/左右键
掩码/点击转发链路/坐标换算/export 三处分工/Go 包装签名),
`goroutineCallableDarwinEntries` 增补 `set_panel_mode`、`tray_rect`(pool 纪律
由既有清单测试自动生效);同步上游漏重放即红。

### 删除的文件

- `example/`、`webview_example/`:演示目录,含 package main,保留会拖累
  主仓 `go build ./...`。
- `systray_windows_test.go`:上游自带坏测试(v1.2.2 里该文件调用的还是旧
  API 签名 `addOrUpdateMenuItem(int32,string,bool,bool)`,而库已改为
  `(uint32,uint32,string,bool,bool)`),windows/amd64 下编译不过,零测试
  价值,删除。

### 格式豁免(刻意不做)

vendored 文件保留上游旧式 `// +build` 构建标签与 `ioutil` 等写法,不跑
gofmt 现代化——本目录以「逐字节忠实于上游 + 仅功能补丁」为纪律,仓库级
`gofmt -l` 会点名本目录属预期行为。

## 升级流程

上游若发新版:替换本目录为新版源码 → `grep -n 本仓补丁 systray_windows.go`
按上文 12 处、`grep -n 本仓补丁 systray_darwin.m` 按上文 9+6=15 处
(锚点注释共 18 行)、`grep -n 本仓补丁 systray.go` 按上文 1 处、面板胶水
另三个文件(`grep -n 本仓补丁 systray_darwin.go` 2 处 /
`grep -n 本仓补丁 systray_nonwindows.go` 1 处 / `grep -n 本仓补丁 systray.h`
2 处)重放补丁 → 重新评估是否恢复被删文件 → 主仓按下一节双平台验证。

## 验证方式

本目录是嵌套模块,主模块不认 `./third_party/systray/` 这种路径写法;编译
验证要写 import 路径 `github.com/getlantern/systray`,由主仓的 replace 解析。

### Windows(GDI 与面板胶水)

GDI/托盘行为无法在 darwin 上运行时验证,分四层:
1. 编译层:`GOOS=windows GOARCH=amd64 go build github.com/getlantern/systray`。
2. 结构层:`go test ./app/tray/ -run TestSystrayWindowsPanelPatch`(用例在
   `app/tray/systray_windows_patch_test.go`)钉住面板胶水的形态——公共层
   hook、wndProc 点击分支、TrayRect(cbSize 必须 Sizeof 表达式且禁字面
   常量/两参 Call/只认 S_OK)、showMenu(WM_NULL + TPM_RIGHTBUTTON),
   每处带「本仓补丁」锚点,同步上游漏重放即红。
3. 审阅层:GDI 三处补丁语义直白(销毁即将被覆盖/丢弃的句柄)。
4. 实机层:部署后在真机用 GetGuiResources 观测 companion 的 GDI 对象数,
   AI 状态翻转下应保持平坦(不再线性增长);面板接线任务落地后另验
   TrayRect 矩形与点击回调(溢出区两态判废在接线层)。

### macOS(autorelease 与面板胶水)

1. 结构层:`go test ./app/tray/ -run TestVendoredSystrayDarwin`(用例在
   `app/tray/systray_darwin_patch_test.go`)钉住四件事——9 个入口的函数体
   整体在 `@autoreleasepool` 内、每处带 `本仓补丁` 锚点、文件里新增的 C
   入口必须归类(会被 goroutine 直接调用的要包 pool,面板胶水的
   `set_panel_mode` / `tray_rect` 已入清单)、面板胶水形态
   (`TestVendoredSystrayDarwinPanelPatch`:两态切换/摘菜单接回/左右键
   sendActionOn 掩码/点击转发链路/tray_rect 的左下原点→全局左上原点换算/
   export 三处分工/Go 侧包装签名)。同步上游漏重放即红。
2. 面板胶水探针层(2026-11-26 实测,macOS 26.2):探针程序
   (/tmp/tray-probe-mac,LSUIElement .app,经 replace 引用本目录,全透明
   128×128 PNG 图标)程序化验证三条——`SetPanelMode(true)` 后经
   `performClick:` 程序化点击托盘按钮,`panelClickFn` 回调被触发;
   `SetPanelMode(false)` 后 `statusItem.menu` 接回、按钮 action/target 摘除;
   `TrayRect()` 返回值在主屏范围内(与 `[NSScreen screens]` 首个的 frame
   对照)。
3. 运行层(autorelease,2026-11-26 实测):探针程序经 replace 引用本目录,
   在 onReady 里建与 companion 相同的菜单,再按 companion 每 tick 的调用
   序列以 20Hz 批量调用,`footprint <pid>` 看 MALLOC_SMALL 脏内存——补丁前
   6.4KB/tick(去图标化后的序列)/ 51KB/tick(含 4 个 provider 菜单项图标的
   序列),补丁后 1000 轮累计 +0.2MB,平坦。同一探针同时比对
   statusItem.button 的图标内容哈希与 tooltip、每个菜单项的
   title/enabled/state/hidden/图标哈希,补丁前后逐字节一致。
   覆盖范围:逐 tick 序列在运行期实际走到 6 个入口
   (`add_or_update_menu_item`、`setTooltip`、`setIcon`、`setMenuItemIcon`、
   `hide_menu_item`、`show_menu_item`);`add_separator` 只在初始化时调用;
   `setTitle` 与 `quit` 只有结构测试和人工审读覆盖——两者形态安全
   (`setTitle` 与已覆盖的 `setTooltip` 同构,且 companion 不调用它;`quit`
   不创建对象)。
4. 实机层:装机重启 companion 后 `footprint <pid>` 观测,24h 内 MALLOC_SMALL
   应平坦(旧版 3 天涨到 5.8GB)。
