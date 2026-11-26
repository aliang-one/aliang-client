#import <Cocoa/Cocoa.h>
#include "systray.h"

#if __MAC_OS_X_VERSION_MIN_REQUIRED < 101400

    #ifndef NSControlStateValueOff
      #define NSControlStateValueOff NSOffState
    #endif

    #ifndef NSControlStateValueOn
      #define NSControlStateValueOn NSOnState
    #endif

#endif

@interface MenuItem : NSObject
{
  @public
    NSNumber* menuId;
    NSNumber* parentMenuId;
    NSString* title;
    NSString* tooltip;
    short disabled;
    short checked;
}
-(id) initWithId: (int)theMenuId
withParentMenuId: (int)theParentMenuId
       withTitle: (const char*)theTitle
     withTooltip: (const char*)theTooltip
    withDisabled: (short)theDisabled
     withChecked: (short)theChecked;
     @end
     @implementation MenuItem
     -(id) initWithId: (int)theMenuId
     withParentMenuId: (int)theParentMenuId
            withTitle: (const char*)theTitle
          withTooltip: (const char*)theTooltip
         withDisabled: (short)theDisabled
          withChecked: (short)theChecked
{
  menuId = [NSNumber numberWithInt:theMenuId];
  parentMenuId = [NSNumber numberWithInt:theParentMenuId];
  title = [[NSString alloc] initWithCString:theTitle
                                   encoding:NSUTF8StringEncoding];
  tooltip = [[NSString alloc] initWithCString:theTooltip
                                     encoding:NSUTF8StringEncoding];
  disabled = theDisabled;
  checked = theChecked;
  return self;
}
@end

@interface AppDelegate: NSObject <NSApplicationDelegate>
  - (void) add_or_update_menu_item:(MenuItem*) item;
  - (IBAction)menuHandler:(id)sender;
  // 本仓补丁:面板模式胶水(实现在下方 @implementation,C 入口见
  // set_panel_mode / tray_rect;点击出口 systray_tray_clicked 由 Go 侧
  // systray_nonwindows.go //export 提供)。
  - (void) setPanelMode:(NSNumber*) panel;
  - (void) trayClicked:(id) sender;
  - (void) fillTrayRect:(NSMutableArray*) box;
  @property (assign) IBOutlet NSWindow *window;
  @end

  @implementation AppDelegate
{
  NSStatusItem *statusItem;
  NSMenu *menu;
  NSCondition* cond;
}

@synthesize window = _window;

- (void)applicationDidFinishLaunching:(NSNotification *)aNotification
{
  self->statusItem = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
  self->menu = [[NSMenu alloc] init];
  [self->menu setAutoenablesItems: FALSE];
  [self->statusItem setMenu:self->menu];
  systray_ready();
}

- (void)applicationWillTerminate:(NSNotification *)aNotification
{
  systray_on_exit();
}

- (void)setIcon:(NSImage *)image {
  statusItem.button.image = image;
  [self updateTitleButtonStyle];
}

- (void)setTitle:(NSString *)title {
  statusItem.button.title = title;
  [self updateTitleButtonStyle];
}

-(void)updateTitleButtonStyle {
  if (statusItem.button.image != nil) {
    if ([statusItem.button.title length] == 0) {
      statusItem.button.imagePosition = NSImageOnly;
    } else {
      statusItem.button.imagePosition = NSImageLeft;
    }
  } else {
    statusItem.button.imagePosition = NSNoImage;
  }
}


- (void)setTooltip:(NSString *)tooltip {
  statusItem.button.toolTip = tooltip;
}

- (IBAction)menuHandler:(id)sender
{
  NSNumber* menuId = [sender representedObject];
  systray_menu_item_selected(menuId.intValue);
}

- (void)add_or_update_menu_item:(MenuItem *)item {
  NSMenu *theMenu = self->menu;
  NSMenuItem *parentItem;
  if ([item->parentMenuId integerValue] > 0) {
    parentItem = find_menu_item(menu, item->parentMenuId);
    if (parentItem.hasSubmenu) {
      theMenu = parentItem.submenu;
    } else {
      theMenu = [[NSMenu alloc] init];
      [theMenu setAutoenablesItems:NO];
      [parentItem setSubmenu:theMenu];
    }
  }
  
  NSMenuItem *menuItem;
  menuItem = find_menu_item(theMenu, item->menuId);
  if (menuItem == NULL) {
    menuItem = [theMenu addItemWithTitle:item->title
                               action:@selector(menuHandler:)
                        keyEquivalent:@""];
    [menuItem setRepresentedObject:item->menuId];
  }
  [menuItem setTitle:item->title];
  [menuItem setTag:[item->menuId integerValue]];
  [menuItem setTarget:self];
  [menuItem setToolTip:item->tooltip];
  if (item->disabled == 1) {
    menuItem.enabled = FALSE;
  } else {
    menuItem.enabled = TRUE;
  }
  if (item->checked == 1) {
    menuItem.state = NSControlStateValueOn;
  } else {
    menuItem.state = NSControlStateValueOff;
  }
}

NSMenuItem *find_menu_item(NSMenu *ourMenu, NSNumber *menuId) {
  NSMenuItem *foundItem = [ourMenu itemWithTag:[menuId integerValue]];
  if (foundItem != NULL) {
    return foundItem;
  }
  NSArray *menu_items = ourMenu.itemArray;
  int i;
  for (i = 0; i < [menu_items count]; i++) {
    NSMenuItem *i_item = [menu_items objectAtIndex:i];
    if (i_item.hasSubmenu) {
      foundItem = find_menu_item(i_item.submenu, menuId);
      if (foundItem != NULL) {
        return foundItem;
      }
    }
  }

  return NULL;
};

- (void) add_separator:(NSNumber*) menuId
{
  [menu addItem: [NSMenuItem separatorItem]];
}

- (void) hide_menu_item:(NSNumber*) menuId
{
  NSMenuItem* menuItem = find_menu_item(menu, menuId);
  if (menuItem != NULL) {
    [menuItem setHidden:TRUE];
  }
}

- (void) setMenuItemIcon:(NSArray*)imageAndMenuId {
  NSImage* image = [imageAndMenuId objectAtIndex:0];
  NSNumber* menuId = [imageAndMenuId objectAtIndex:1];

  NSMenuItem* menuItem;
  menuItem = find_menu_item(menu, menuId);
  if (menuItem == NULL) {
    return;
  }
  menuItem.image = image;
}

- (void) show_menu_item:(NSNumber*) menuId
{
  NSMenuItem* menuItem = find_menu_item(menu, menuId);
  if (menuItem != NULL) {
    [menuItem setHidden:FALSE];
  }
}

- (void) quit
{
  [NSApp terminate:self];
}

// 本仓补丁:面板模式开关(主线程执行,经 runInMainThread 从 C 入口
// set_panel_mode 封送进来)。true=摘菜单、给按钮挂 trayClicked:(左右键按下
// 即触发);false=摘 action、接回原生菜单(原生菜单兜底,即上游
// applicationDidFinishLaunching 的形态)。菜单对象与 companion 的刷新循环
// 完全不动——面板期间菜单照常被更新(不可见而已),随时无损接回。
- (void)setPanelMode:(NSNumber *)panel {
  if ([panel boolValue]) {
    statusItem.menu = nil; // 摘菜单:点击改走 trayClicked:
    statusItem.button.target = self;
    statusItem.button.action = @selector(trayClicked:);
    [statusItem.button sendActionOn:NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown]; // 左右键按下都触发
  } else {
    statusItem.button.target = nil;
    statusItem.button.action = nil; // 摘 action
    [statusItem.button sendActionOn:NSEventMaskLeftMouseUp]; // 恢复按钮默认的抬起触发语义
    statusItem.menu = menu; // 接回原生菜单:兜底模式下点击由状态项的 menu 接管
  }
}

// 本仓补丁:面板模式托盘点击(左右键,掩码在 setPanelMode: 里设为按下即触发)
// → 转 Go 侧出口。Go 侧(systray_nonwindows.go 的 //export systray_tray_clicked)
// 起新 goroutine 异步执行 panelClickFn,不在 AppKit 主线程做耗时工作。
- (void)trayClicked:(id)sender {
  systray_tray_clicked();
}

// 本仓补丁:把托盘按钮窗口 frame 装箱回传给 C 入口 tray_rect(主线程执行)。
// macOS 的 NSScreen 全局坐标系是左下原点;换算成全局左上原点(AppKit 点(逻辑)
// 坐标,macOS 面板窗口 API 同单位直通,物理像素仅 Windows 侧):主屏 =
// [NSScreen screens] 首个(带菜单栏的那块,全局坐标零点在其左下角),
// y_top = 主屏高 - (frame.origin.y + frame 高)。screens 为空时 firstObject
// 返回 nil、高度取 0,兜底 ok=false(objectAtIndex:0 会抛 NSRangeException
// 带走主线程,故不用)。
- (void)fillTrayRect:(NSMutableArray *)box {
  NSWindow *win = statusItem.button.window;
  NSRect frame = win.frame;
  NSScreen *primaryScreen = [[NSScreen screens] firstObject];
  CGFloat primaryScreenHeight = primaryScreen.frame.size.height;
  [box addObject:[NSNumber numberWithDouble:frame.origin.x]];
  [box addObject:[NSNumber numberWithDouble:primaryScreenHeight - frame.origin.y - frame.size.height]];
  [box addObject:[NSNumber numberWithDouble:frame.size.width]];
  [box addObject:[NSNumber numberWithDouble:frame.size.height]];
  [box addObject:[NSNumber numberWithBool:(win != nil && frame.size.width > 0 && frame.size.height > 0 && primaryScreenHeight > 0)]];
}

@end

void registerSystray(void) {
  AppDelegate *delegate = [[AppDelegate alloc] init];
  [[NSApplication sharedApplication] setDelegate:delegate];
  // A workaround to avoid crashing on macOS versions before Catalina. Somehow
  // SIGSEGV would happen inside AppKit if [NSApp run] is called from a
  // different function, even if that function is called right after this.
  if (floor(NSAppKitVersionNumber) <= /*NSAppKitVersionNumber10_14*/ 1671){
    [NSApp run];
  }
}

int nativeLoop(void) {
  if (floor(NSAppKitVersionNumber) > /*NSAppKitVersionNumber10_14*/ 1671){
    [NSApp run];
  }
  return EXIT_SUCCESS;
}

void runInMainThread(SEL method, id object) {
  [(AppDelegate*)[NSApp delegate]
    performSelectorOnMainThread:method
                     withObject:object
                  waitUntilDone: YES];
}

// 本仓补丁(2026-11-26 macOS companion 内存泄漏):下面的入口在调用者 goroutine
// 所在的线程上创建 ObjC 对象(NSData/NSImage/NSString/NSNumber 及图片解码的
// 中间对象,大量是 autoreleased),之后 runInMainThread 才切到主线程。Go 运行时
// 的线程上没有 autorelease pool,这些对象永不释放:每次图标类调用漏约 6~11KB
// (随图片内容与尺寸而异)。macOS companion 以 2s 一轮同步,去图标化之前每轮
// 5 次图标解码,线上实测约 2GB/天;之后每轮 1 次,仍在漏。RSS 很小是因为泄漏页
// 全被系统压缩,要看 footprint。上游 #135 自 2020 年开着至今。
// 修法:每个入口的函数体整体包一层 @autoreleasepool,在调用线程上就地排空
// (quit 不创建对象,包进去只为 9 个入口形态统一)。重放与验证见 PATCH_NOTES.md。

void setIcon(const char* iconBytes, int length, bool template) {
  @autoreleasepool { // 本仓补丁
  NSData* buffer = [NSData dataWithBytes: iconBytes length:length];
  NSImage *image = [[NSImage alloc] initWithData:buffer];
  [image setSize:NSMakeSize(16, 16)];
  image.template = template;
  runInMainThread(@selector(setIcon:), (id)image);
  }
}

void setMenuItemIcon(const char* iconBytes, int length, int menuId, bool template) {
  @autoreleasepool { // 本仓补丁
  NSData* buffer = [NSData dataWithBytes: iconBytes length:length];
  NSImage *image = [[NSImage alloc] initWithData:buffer];
  [image setSize:NSMakeSize(16, 16)];
  image.template = template;
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(setMenuItemIcon:), @[image, (id)mId]);
  }
}

void setTitle(char* ctitle) {
  @autoreleasepool { // 本仓补丁
  NSString* title = [[NSString alloc] initWithCString:ctitle
                                             encoding:NSUTF8StringEncoding];
  free(ctitle);
  runInMainThread(@selector(setTitle:), (id)title);
  }
}

void setTooltip(char* ctooltip) {
  @autoreleasepool { // 本仓补丁
  NSString* tooltip = [[NSString alloc] initWithCString:ctooltip
                                               encoding:NSUTF8StringEncoding];
  free(ctooltip);
  runInMainThread(@selector(setTooltip:), (id)tooltip);
  }
}

void add_or_update_menu_item(int menuId, int parentMenuId, char* title, char* tooltip, short disabled, short checked, short isCheckable) {
  @autoreleasepool { // 本仓补丁
  MenuItem* item = [[MenuItem alloc] initWithId: menuId withParentMenuId: parentMenuId withTitle: title withTooltip: tooltip withDisabled: disabled withChecked: checked];
  free(title);
  free(tooltip);
  runInMainThread(@selector(add_or_update_menu_item:), (id)item);
  }
}

void add_separator(int menuId) {
  @autoreleasepool { // 本仓补丁
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(add_separator:), (id)mId);
  }
}

void hide_menu_item(int menuId) {
  @autoreleasepool { // 本仓补丁
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(hide_menu_item:), (id)mId);
  }
}

void show_menu_item(int menuId) {
  @autoreleasepool { // 本仓补丁
  NSNumber *mId = [NSNumber numberWithInt:menuId];
  runInMainThread(@selector(show_menu_item:), (id)mId);
  }
}

void quit() {
  @autoreleasepool { // 本仓补丁
  runInMainThread(@selector(quit), nil);
  }
}

// 本仓补丁(2026-11-26 托盘面板 M1 Task 4):面板模式胶水。panelMode=true 时
// AppDelegate 摘掉原生菜单、给按钮挂 trayClicked:(sendActionOn 掩码含左右键
// 按下,点击 → systray_tray_clicked() → Go 侧 panelClickFn,自绘面板);
// false 时摘掉 action、接回原生菜单(原生菜单兜底)。菜单对象与 companion 的
// 刷新循环完全不动——面板期间菜单照常被更新(不可见而已),随时无损接回。
// 两个入口都会被 goroutine 直接调用,包 pool 同其余入口;重放与验证见
// PATCH_NOTES.md。

void set_panel_mode(bool panel) {
  @autoreleasepool { // 本仓补丁
  NSNumber *flag = [NSNumber numberWithBool:panel];
  runInMainThread(@selector(setPanelMode:), (id)flag);
  }
}

// 本仓补丁:托盘图标屏幕矩形——AppKit 点(逻辑)坐标、全局左上原点;macOS
// 面板窗口 API 同单位直通,物理像素仅 Windows 侧(两侧 TrayRect 语义对应、
// 单位随平台),面板窗口锚定用。macOS 的 NSScreen 全局坐标系是左下原点,换算
// (经主屏高度)在 AppDelegate fillTrayRect: 里做;窗口 frame 只能主线程读,
// 经 runInMainThread 同步封送,结果装箱进 NSArray 回传。
void tray_rect(int* x, int* y, int* w, int* h, bool* ok) {
  @autoreleasepool { // 本仓补丁
  NSMutableArray *box = [NSMutableArray arrayWithCapacity:5];
  runInMainThread(@selector(fillTrayRect:), (id)box);
  if ([box count] < 5) {
    *ok = false;
    return;
  }
  *x = (int)[[box objectAtIndex:0] intValue];
  *y = (int)[[box objectAtIndex:1] intValue];
  *w = (int)[[box objectAtIndex:2] intValue];
  *h = (int)[[box objectAtIndex:3] intValue];
  *ok = [[box objectAtIndex:4] boolValue];
  }
}
