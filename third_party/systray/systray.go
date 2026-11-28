/*
Package systray is a cross-platform Go library to place an icon and menu in the notification area.
*/
package systray

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/getlantern/golog"
)

var (
	log = golog.LoggerFor("systray")

	systrayReady  func()
	systrayExit   func()
	menuItems     = make(map[uint32]*MenuItem)
	menuItemsLock sync.RWMutex

	currentID = uint32(0)
	quitOnce  sync.Once
)

func init() {
	runtime.LockOSThread()
}

// MenuItem is used to keep track each menu item of systray.
// Don't create it directly, use the one systray.AddMenuItem() returned
type MenuItem struct {
	// ClickedCh is the channel which will be notified when the menu item is clicked
	ClickedCh chan struct{}

	// id uniquely identify a menu item, not supposed to be modified
	id uint32
	// title is the text shown on menu item
	title string
	// tooltip is the text shown when pointing to menu item
	tooltip string
	// disabled menu item is grayed out and has no effect when clicked
	disabled bool
	// checked menu item has a tick before the title
	checked bool
	// has the menu item a checkbox (Linux)
	isCheckable bool
	// parent item, for sub menus
	parent *MenuItem
}

func (item *MenuItem) String() string {
	if item.parent == nil {
		return fmt.Sprintf("MenuItem[%d, %q]", item.id, item.title)
	}
	return fmt.Sprintf("MenuItem[%d, parent %d, %q]", item.id, item.parent.id, item.title)
}

// newMenuItem returns a populated MenuItem object
func newMenuItem(title string, tooltip string, parent *MenuItem) *MenuItem {
	return &MenuItem{
		ClickedCh:   make(chan struct{}),
		id:          atomic.AddUint32(&currentID, 1),
		title:       title,
		tooltip:     tooltip,
		disabled:    false,
		checked:     false,
		isCheckable: false,
		parent:      parent,
	}
}

// Run initializes GUI and starts the event loop, then invokes the onReady
// callback. It blocks until systray.Quit() is called.
func Run(onReady func(), onExit func()) {
	Register(onReady, onExit)
	nativeLoop()
}

// Register initializes GUI and registers the callbacks but relies on the
// caller to run the event loop somewhere else. It's useful if the program
// needs to show other UI elements, for example, webview.
// To overcome some OS weirdness, On macOS versions before Catalina, calling
// this does exactly the same as Run().
func Register(onReady func(), onExit func()) {
	if onReady == nil {
		systrayReady = func() {}
	} else {
		// Run onReady on separate goroutine to avoid blocking event loop
		readyCh := make(chan interface{})
		go func() {
			<-readyCh
			onReady()
		}()
		systrayReady = func() {
			close(readyCh)
		}
	}
	// unlike onReady, onExit runs in the event loop to make sure it has time to
	// finish before the process terminates
	if onExit == nil {
		onExit = func() {}
	}
	systrayExit = onExit
	registerSystray()
}

// Quit the systray
func Quit() {
	quitOnce.Do(quit)
}

// ⚠️ 跨 goroutine 读写(M2 起为必修,2026-11-28 评审升级):写侧是 Go 侧
// SetPanelClickHook(onReady goroutine 装配,M2 兜底/恢复路径
// enterPanelFallback/restorePanelMode 在【运行期】再次写);读侧是 Windows
// wndProc(systray_windows.go 的消息循环 goroutine)与 macOS
// systray_tray_clicked(AppKit 主线程经 cgo 进来)。裸 var 是数据竞争
// (-race 在真实路径必报;单测用替身绕过了这条链才没红)。
//
// 本仓补丁:面板模式点击回调。设置后,后端把托盘图标的左/右键点击都交给
// 回调(每次点击起新 goroutine 异步执行),不再弹原生菜单;nil(默认)保持
// 上游行为 = 点击弹原生菜单(原生菜单兜底模式)。读取此回调的是 Windows 的
// wndProc 与 macOS 的 systray_tray_clicked(2026-11-26 面板胶水接入 darwin)。
// 应在 Run/Register 之前设置;运行期切换的时序责任由面板接线层自担。
// 存取一律经 loadPanelClickHook,禁止直接读字段。用 atomic.Value 而非泛型
// atomic.Pointer[T]:本 module 声明 go 1.13,不为单处改动抬语言版本;
// atomic.Value 不接受 nil Store,经 panelClickBox 包一层统一类型。
var panelClickFn atomic.Value // 存 *panelClickBox;零值=未进面板模式

// panelClickHook 具名函数类型(box 字段类型;与裸 func() 等价,具名只为
// 可读)。
type panelClickHook func()

// panelClickBox 回调盒:fn 为 nil 表示「摘回调」(SetPanelClickHook(nil)),
// 与「从未设置」(零值 atomic.Value)在 load 侧同为 nil 语义。
type panelClickBox struct{ fn panelClickHook }

// loadPanelClickHook 读侧唯一入口(两平台读点都走这里);nil=未进面板模式。
func loadPanelClickHook() panelClickHook {
	box, _ := panelClickFn.Load().(*panelClickBox)
	if box == nil {
		return nil
	}
	return box.fn
}

// SetPanelClickHook 注册托盘图标点击回调(面板模式)。
// fn 为 nil 时恢复原生菜单兜底模式。
// 回调契约:应快速返回——每次点击都会起新 goroutine,耗时回调会按连点
// 次数无上限堆积 goroutine;回调可能并发执行(双击 = 两个并发回调),
// 串行化由接线层负责;回调 panic 发生在无 recover 的 goroutine 上,
// 会终止整个进程。
func SetPanelClickHook(fn func()) {
	panelClickFn.Store(&panelClickBox{fn: fn})
}

// AddMenuItem adds a menu item with the designated title and tooltip.
// It can be safely invoked from different goroutines.
// Created menu items are checkable on Windows and OSX by default. For Linux you have to use AddMenuItemCheckbox
func AddMenuItem(title string, tooltip string) *MenuItem {
	item := newMenuItem(title, tooltip, nil)
	item.update()
	return item
}

// AddMenuItemCheckbox adds a menu item with the designated title and tooltip and a checkbox for Linux.
// It can be safely invoked from different goroutines.
// On Windows and OSX this is the same as calling AddMenuItem
func AddMenuItemCheckbox(title string, tooltip string, checked bool) *MenuItem {
	item := newMenuItem(title, tooltip, nil)
	item.isCheckable = true
	item.checked = checked
	item.update()
	return item
}

// AddSeparator adds a separator bar to the menu
func AddSeparator() {
	addSeparator(atomic.AddUint32(&currentID, 1))
}

// AddSubMenuItem adds a nested sub-menu item with the designated title and tooltip.
// It can be safely invoked from different goroutines.
// Created menu items are checkable on Windows and OSX by default. For Linux you have to use AddSubMenuItemCheckbox
func (item *MenuItem) AddSubMenuItem(title string, tooltip string) *MenuItem {
	child := newMenuItem(title, tooltip, item)
	child.update()
	return child
}

// AddSubMenuItemCheckbox adds a nested sub-menu item with the designated title and tooltip and a checkbox for Linux.
// It can be safely invoked from different goroutines.
// On Windows and OSX this is the same as calling AddSubMenuItem
func (item *MenuItem) AddSubMenuItemCheckbox(title string, tooltip string, checked bool) *MenuItem {
	child := newMenuItem(title, tooltip, item)
	child.isCheckable = true
	child.checked = checked
	child.update()
	return child
}

// SetTitle set the text to display on a menu item
func (item *MenuItem) SetTitle(title string) {
	item.title = title
	item.update()
}

// SetTooltip set the tooltip to show when mouse hover
func (item *MenuItem) SetTooltip(tooltip string) {
	item.tooltip = tooltip
	item.update()
}

// Disabled checks if the menu item is disabled
func (item *MenuItem) Disabled() bool {
	return item.disabled
}

// Enable a menu item regardless if it's previously enabled or not
func (item *MenuItem) Enable() {
	item.disabled = false
	item.update()
}

// Disable a menu item regardless if it's previously disabled or not
func (item *MenuItem) Disable() {
	item.disabled = true
	item.update()
}

// Hide hides a menu item
func (item *MenuItem) Hide() {
	hideMenuItem(item)
}

// Show shows a previously hidden menu item
func (item *MenuItem) Show() {
	showMenuItem(item)
}

// Checked returns if the menu item has a check mark
func (item *MenuItem) Checked() bool {
	return item.checked
}

// Check a menu item regardless if it's previously checked or not
func (item *MenuItem) Check() {
	item.checked = true
	item.update()
}

// Uncheck a menu item regardless if it's previously unchecked or not
func (item *MenuItem) Uncheck() {
	item.checked = false
	item.update()
}

// update propagates changes on a menu item to systray
func (item *MenuItem) update() {
	menuItemsLock.Lock()
	menuItems[item.id] = item
	menuItemsLock.Unlock()
	addOrUpdateMenuItem(item)
}

func systrayMenuItemSelected(id uint32) {
	menuItemsLock.RLock()
	item, ok := menuItems[id]
	menuItemsLock.RUnlock()
	if !ok {
		log.Errorf("No menu item with ID %v", id)
		return
	}
	select {
	case item.ClickedCh <- struct{}{}:
	// in case no one waiting for the channel
	default:
	}
}
