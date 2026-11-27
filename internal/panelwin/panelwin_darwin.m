// panelwin_darwin.m — 托盘面板纯原生窗口壳,macOS 侧(实现计划 M2 Task 13)。
//
// 形态:无边框 NSPanel(不抢宿主焦点,canBecomeKey 覆写为 YES)+ 系统
// 材质层(NSVisualEffectView behindWindow)铺底 + WKWebView 透明底
// (drawsBackground=false 经 KVC 私有面;公证探针随 Task 16 同批)——
// 毛玻璃归系统材质层,面板前端 hosted 态只画半透明底(Task 7 评审固化)。
//
// 生命周期:windowDidResignKey → exit(0) 失焦自退;页面 quit 消息 →
// exit(60);open 消息 → NSWorkspace openURL 开默认浏览器后驻留。
// 进程是叶子进程,窗口/对象生命周期=进程存活,不做显式释放。
//
// 内存纪律(同 third_party/systray/systray_darwin.m 的教训,上游 #135):
// 本文件唯一的 C 入口 panelwin_run 会被 Go goroutine 所在线程直接调到,
// Go 运行时线程没有 autorelease pool——函数体整体包一层 @autoreleasepool,
// 入口内创建的任何 autoreleased 中间对象就地排空。ARC 负责所有权,全文件
// 不手写 autorelease 消息;delegate/窗口对象存活到进程结束(叶子进程,
// 不释放)。delegate 的 ObjC 方法(WKScriptMessageHandler / NSWindowDelegate /
// WKNavigationDelegate)全部由 AppKit 主 run loop 调用,run loop 自带
// 逐事件 pool,无需自管。
//
// 激活面:makeKeyAndOrderFront + [NSApp activateIgnoringOtherApps:YES]
// 双保险——不取到焦点,windowDidResignKey 永不触发、失焦即退整条失效。
// NSWindowStyleMaskNonactivatingPanel 让面板不抢走宿主(托盘)的 key 状态;
// canBecomeKey 覆写为 YES 让面板内部可交互(按钮、hover)。

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface PanelWindow : NSPanel
@end

@implementation PanelWindow
// 无边框 NSPanel 默认 canBecomeKey=NO,面板内部无法交互;必须覆写。
- (BOOL)canBecomeKey {
  return YES;
}
@end

@interface PanelAppDelegate : NSObject
    <NSApplicationDelegate, NSWindowDelegate, WKScriptMessageHandler, WKNavigationDelegate>
@property (strong) NSPanel* panel;
@property (copy) NSString* allowOrigin;    // 导航白名单唯一放行 origin
@property (copy) NSString* dashboardURL;   // open kind=dashboard → 此 URL
@property (copy) NSString* peerURL;        // open kind=peer → 此 URL
@end

@implementation PanelAppDelegate

// 失焦自退:面板点到外部/宿主切换即退出。退出码 0=正常关闭(Task 14
// launcher 判据:0 与 60 零惩罚,其余非零=崩溃)。
- (void)windowDidResignKey:(NSNotification *)notification {
  exit(0);
}

// 导航白名单:仅放行白名单 origin 的主文档导航(调研 §6.3 不变量——面板
// 访问 core 回环源免 cookie 即完整控制权,不得加载任何第三方内容)。
// 其余(外域、file:、about:)一律 Cancel。
- (void)webView:(WKWebView *)webView
    decidePolicyForNavigationAction:(WKNavigationAction *)navigationAction
                  decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
  NSURL* url = navigationAction.request.URL;
  NSString* origin = nil;
  if (url.scheme && url.host) {
    origin = [NSString stringWithFormat:@"%@://%@%@",
              url.scheme.lowercaseString,
              url.host.lowercaseString,
              url.port ? [NSString stringWithFormat:@":%@", url.port] : @""];
  }
  if (origin && [origin isEqualToString:self.allowOrigin.lowercaseString]) {
    decisionHandler(WKNavigationActionPolicyAllow);
    return;
  }
  decisionHandler(WKNavigationActionPolicyCancel);
}

// WebMessage 通道:handler 名固定 "panel"(与前端 messageHandlers.panel
// 逐字对齐,结构测试断言)。协议冻结为 quit/open 两类型;页面 postMessage
// 一个 JS 对象时 WebKit 会转成 NSDictionary 送达,兼容 JSON 字符串形态。
- (void)userContentController:(WKUserContentController *)userContentController
          didReceiveScriptMessage:(WKScriptMessage *)message {
  id body = message.body;
  NSString* type = nil;
  NSString* kind = nil;
  if ([body isKindOfClass:[NSDictionary class]]) {
    type = body[@"type"];
    kind = body[@"kind"];
  } else if ([body isKindOfClass:[NSString class]]) {
    // 兜底:若前端改为发 JSON 字符串也能解(当前形态是对象→NSDictionary)。
    NSData* data = [(NSString *)body dataUsingEncoding:NSUTF8StringEncoding];
    NSDictionary* parsed = [NSJSONSerialization JSONObjectWithData:data
                                                           options:0
                                                             error:nil];
    if ([parsed isKindOfClass:[NSDictionary class]]) {
      type = parsed[@"type"];
      kind = parsed[@"kind"];
    }
  }

  if ([type isEqualToString:@"quit"]) {
    exit(60);
    return;
  }
  if ([type isEqualToString:@"open"]) {
    NSString* target = nil;
    if ([kind isEqualToString:@"dashboard"]) {
      target = self.dashboardURL;
    } else if ([kind isEqualToString:@"peer"]) {
      target = self.peerURL;
    }
    if (target) {
      NSURL* url = [NSURL URLWithString:target];
      if (url) {
        [[NSWorkspace sharedWorkspace] openURL:url];
      }
    }
    // open 后驻留(面板不退出);未知 kind 落到这里同样静默驻留。
    return;
  }
  // 未知类型默认忽略不崩(协议冻结:只有 quit/open 两类型)。
}

@end

// originOfURL — 白名单锚点 origin(scheme://host[:port],小写;IPv6 字面量
// 由 NSURL host 自带方括号,与 Go 侧 LoopbackOrigin 的序列化形态一致)。
static NSString* originOfURL(NSString* urlStr) {
  NSURL* url = [NSURL URLWithString:urlStr];
  if (!url.scheme || !url.host) {
    return @"";
  }
  NSString* origin = [NSString stringWithFormat:@"%@://%@%@",
                      url.scheme.lowercaseString,
                      url.host.lowercaseString,
                      url.port ? [NSString stringWithFormat:@":%@", url.port] : @""];
  return origin;
}

// panelwin_run — 唯一 C 入口(Go 侧 panelwin_darwin.go 经 cgo 调用)。
// 入参坐标是全局左上原点(与两侧 TrayRect 语义一致);NSWindow 定位用
// 全局左下原点,这里做一次换算(主屏高 - yTop - h,即 fillTrayRect 的逆)。
void panelwin_run(const char* url, int x, int yTop, int w, int h,
                  const char* dashboardURL, const char* peerURL) {
  @autoreleasepool {
    [NSApplication sharedApplication];
    // 无 Dock 图标(验收项):面板是托盘的附属弹出物,不是独立应用。
    [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

    NSString* urlStr = [NSString stringWithUTF8String:url];
    NSString* dashStr = [NSString stringWithUTF8String:dashboardURL];
    NSString* peerStr = [NSString stringWithUTF8String:peerURL];

    // 全局左上原点 → NSWindow 的全局左下原点:主屏(带菜单栏那块)的
    // frame 高即全局坐标系的高度基准。
    NSArray* screens = [NSScreen screens];
    CGFloat screenH = screens.count > 0
        ? [[screens objectAtIndex:0] frame].size.height
        : (CGFloat)yTop + (CGFloat)h;
    CGFloat bottomLeftY = screenH - (CGFloat)yTop - (CGFloat)h;

    PanelWindow* panel = [[PanelWindow alloc]
        initWithContentRect:NSMakeRect((CGFloat)x, bottomLeftY, (CGFloat)w, (CGFloat)h)
                  styleMask:NSWindowStyleMaskBorderless |
                            NSWindowStyleMaskNonactivatingPanel
                    backing:NSBackingStoreBuffered
                      defer:NO];
    panel.opaque = NO;                 // 透明底:让系统材质层透出来
    panel.backgroundColor = [NSColor clearColor];
    panel.level = NSPopUpMenuWindowLevel; // 悬浮于普通窗口之上
    panel.releasedWhenClosed = NO;

    PanelAppDelegate* delegate = [[PanelAppDelegate alloc] init];
    delegate.allowOrigin = originOfURL(urlStr);
    delegate.dashboardURL = dashStr;
    delegate.peerURL = peerStr;
    delegate.panel = panel;
    [NSApp setDelegate:delegate];

    // 系统材质层铺底:毛玻璃归它,前端只画半透明底色(spec §1/§7)。
    NSVisualEffectView* effect = [[NSVisualEffectView alloc]
        initWithFrame:[panel contentRectForFrameRect:panel.frame]];
    effect.material = NSVisualEffectMaterialPopover;
    effect.blendingMode = NSVisualEffectBlendingModeBehindWindow;
    effect.state = NSVisualEffectStateActive;
    effect.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    [panel.contentView addSubview:effect];

    // WKWebView 透明底:drawsBackground 是私有 KVC 面(不在公开头文件,
    // 公证探针随 Task 16 同批);不关它 WKWebView 白底会盖住材质。
    WKWebViewConfiguration* config = [[WKWebViewConfiguration alloc] init];
    [config.userContentController addScriptMessageHandler:delegate name:@"panel"];
    WKWebView* web = [[WKWebView alloc] initWithFrame:effect.bounds
                                        configuration:config];
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    [web setValue:@NO forKey:@"drawsBackground"];
    web.navigationDelegate = delegate;
    [effect addSubview:web];

    [web loadRequest:[NSURLRequest requestWithURL:[NSURL URLWithString:urlStr]]];

    // 激活双保险:不取焦点则失焦即退永不触发。
    [panel makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];

    // 不返回——退出只发生在 exit(0)/exit(60) 两个出口上。
    [NSApp run];
  }
}
