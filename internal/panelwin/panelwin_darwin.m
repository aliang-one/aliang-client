// panelwin_darwin.m — 托盘面板纯原生窗口壳,macOS 侧(实现计划 M2 Task 13)。
//
// 形态:无边框 NSPanel(不抢宿主焦点,canBecomeKey 覆写为 YES)+ 系统
// 材质层(NSVisualEffectView behindWindow)铺底 + WKWebView 透明底
// (drawsBackground=false 经 KVC 私有面;公证探针随 Task 16 同批)——
// 毛玻璃归系统材质层,面板前端 hosted 态只画半透明底(Task 7 评审固化)。
//
// 生命周期:windowDidResignKey → exit(0) 失焦自退;页面 quit 消息 →
// exit(60);open 消息 → NSWorkspace openURL 开默认浏览器后驻留;resize
// 消息 → 跟随改窗高(高度自适应,2026-11-28 用户拍板)。
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
//
// ⚠️ 窗口创建时序(2026-11-28 实机走查坐实,见 applicationDidFinishLaunching):
// 窗口必须在 app 完成启动后创建, Spaces 归属/key 资格才有正确语义。

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
@property (copy) NSString* launchURL;      // --url,panel.html 完整地址
@property (assign) NSRect contentRect;     // AppKit 全局左下原点的窗口 frame
@end

@implementation PanelAppDelegate

// 失焦自退:面板点到外部/宿主切换即退出。退出码 0=正常关闭(Task 14
// launcher 判据:0 与 60 零惩罚,其余非零=崩溃)。
- (void)windowDidResignKey:(NSNotification *)notification {
  NSLog(@"panelwin: resignKey → exit(0)");
  exit(0);
}

- (void)windowDidBecomeKey:(NSNotification *)notification {
  NSLog(@"panelwin: becomeKey");
}

// 导航取证(Task 16 探针):页面加载成败一行日志,白名单误拦/网络失败不再哑。
- (void)webView:(WKWebView *)webView
    didCommitNavigation:(WKNavigation *)navigation {
  NSLog(@"panelwin: web didCommit %@", webView.URL ?: [NSURL URLWithString:@"(nil)"]);
}
- (void)webView:(WKWebView *)webView
        didFailNavigation:(WKNavigation *)navigation
                    withError:(NSError *)error {
  NSLog(@"panelwin: web didFail %@", error.localizedDescription);
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
// 逐字对齐,结构测试断言)。类型白名单为 quit/open/resize 三类型(resize=
// 高度自适应,2026-11-28 用户拍板;新增类型需双侧同批);页面 postMessage
// 一个 JS 对象时 WebKit 会转成 NSDictionary 送达,兼容 JSON 字符串形态。
- (void)userContentController:(WKUserContentController *)userContentController
          didReceiveScriptMessage:(WKScriptMessage *)message {
  id body = message.body;
  NSString* type = nil;
  NSString* kind = nil;
  NSNumber* heightNum = nil;
  if ([body isKindOfClass:[NSDictionary class]]) {
    // 畸形 payload 守卫:{"type":123}/{"type":"open","kind":[]} 这类形态
    // 会让 body[@"type"]/body[@"kind"] 拿到 NSNumber/NSArray,后续
    // isEqualToString: 直接 unrecognized selector 崩——协议注释是
    // 「未知形态忽略不崩」。取值先过 isKindOfClass:[NSString class] 闸,
    // 非 NSString 一律置 nil,落进下面的「未知忽略」分支;height 同款过
    // NSNumber 类闸(字符串 height 不得被 doubleValue 撑崩)。
    if ([body[@"type"] isKindOfClass:[NSString class]]) {
      type = body[@"type"];
    }
    if ([body[@"kind"] isKindOfClass:[NSString class]]) {
      kind = body[@"kind"];
    }
    if ([body[@"height"] isKindOfClass:[NSNumber class]]) {
      heightNum = body[@"height"];
    }
  } else if ([body isKindOfClass:[NSString class]]) {
    // 兜底:若前端改为发 JSON 字符串也能解(当前形态是对象→NSDictionary)。
    NSData* data = [(NSString *)body dataUsingEncoding:NSUTF8StringEncoding];
    NSDictionary* parsed = [NSJSONSerialization JSONObjectWithData:data
                                                           options:0
                                                             error:nil];
    if ([parsed isKindOfClass:[NSDictionary class]]) {
      // 同上的类守卫:JSON 里 type/kind 是数字/数组时不得崩。
      if ([parsed[@"type"] isKindOfClass:[NSString class]]) {
        type = parsed[@"type"];
      }
      if ([parsed[@"kind"] isKindOfClass:[NSString class]]) {
        kind = parsed[@"kind"];
      }
      if ([parsed[@"height"] isKindOfClass:[NSNumber class]]) {
        heightNum = parsed[@"height"];
      }
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
  if ([type isEqualToString:@"resize"]) {
    // 面板高度自适应(2026-11-28 用户拍板):height 过 NSNumber 类闸(畸形
    // payload 守卫同款),clamp [280, 顶边-8] 后跟随改窗高。macOS 托盘恒
    // 菜单栏,resize 恒顶边固定、向下生长;Windows 侧 side-aware 见
    // panelwin_windows.go(Options.Side)。height 缺失/非数值(heightNum
    // 为 nil)走「未知忽略」同款静默,不崩。
    if (heightNum && self.panel) {
      CGFloat newH = heightNum.doubleValue;
      if (newH < 280) {
        newH = 280; // 最低可用高:再矮内容就塞不下了
      }
      NSRect f = self.panel.frame;
      CGFloat topY = f.origin.y + f.size.height; // 顶边(全局左下原点)
      CGFloat maxH = topY - 8;                   // 超出屏幕下缘留 8 → 钳高
      if (newH > maxH) {
        newH = maxH;
      }
      // 顶边固定向下生长:origin.y = 顶边 - 新高,宽与 x 不动。
      [self.panel setFrame:NSMakeRect(f.origin.x, topY - newH, f.size.width, newH)
                   display:YES
                    animate:NO];
    }
    // resize 后驻留(面板不退出)。
    return;
  }
  // 未知类型默认忽略不崩(类型白名单:quit/open/resize,新增需双侧同批)。
}

// applicationDidFinishLaunching — 窗口/WebView 全部在 app 完成启动后创建。
// ⚠️ 2026-11-28 实机走查坐实:run loop 起来之前建窗口,Spaces 会把它钉在
// 错误位置(窗口服务器在册、CGWindowList onscreen 有名,但【不在当前 Space
// 合成】且永不成为 key,失焦自退链死)。AppKit 惯例:窗口创建不早于 app
// 完成启动。
- (void)applicationDidFinishLaunching:(NSNotification *)notification {
  PanelWindow* panel = [[PanelWindow alloc]
      initWithContentRect:self.contentRect
                styleMask:NSWindowStyleMaskBorderless |
                          NSWindowStyleMaskNonactivatingPanel
                  backing:NSBackingStoreBuffered
                    defer:NO];
  panel.opaque = NO;                 // 透明底:让系统材质层透出来
  panel.backgroundColor = [NSColor clearColor];
  panel.level = NSPopUpMenuWindowLevel; // 悬浮于普通窗口之上
  panel.releasedWhenClosed = NO;
  // Space 语义(2026-11-28 实机走查坐实):托盘弹层要与系统托盘菜单同
  // 语义——任何 Space(含全屏应用)上都可见。
  panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                             NSWindowCollectionBehaviorFullScreenAuxiliary;
  self.panel = panel;

  CGFloat w = NSWidth(self.contentRect);
  CGFloat h = NSHeight(self.contentRect);

  // 系统材质层铺底:毛玻璃归它,前端只画半透明底色(spec §1/§7)。
  // ⚠️ frame 必须用 contentView 本地坐标——2026-11-28 实机走查坐实:此前
  // 用 contentRectForFrameRect:panel.frame 拿到的是屏幕全局坐标,addSubview
  // 后材质层整个落到 contentView 可视区外,窗口成为全透明空壳(透过它看到
  // 底下浏览器,「面板没弹出」的真相)。borderless 窗口的 contentView
  // bounds 就是 {0,0,w,h}。
  NSVisualEffectView* effect = [[NSVisualEffectView alloc]
      initWithFrame:NSMakeRect(0, 0, w, h)];
  effect.material = NSVisualEffectMaterialPopover;
  effect.blendingMode = NSVisualEffectBlendingModeBehindWindow;
  effect.state = NSVisualEffectStateActive;
  effect.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  [panel.contentView addSubview:effect];

  // WKWebView 透明底:drawsBackground 是私有 KVC 面(不在公开头文件,
  // 公证探针随 Task 16 同批);不关它 WKWebView 白底会盖住材质。
  WKWebViewConfiguration* config = [[WKWebViewConfiguration alloc] init];
  [config.userContentController addScriptMessageHandler:self name:@"panel"];
  // 前端错误取证(atDocumentStart,先于页面脚本):模块加载/运行错误收进
  // window.__perr——WKWebView 的 JS 错误默认无处可看,留 Task 16 探针取证口。
  [config.userContentController addUserScript:[[WKUserScript alloc]
      initWithSource:@"window.__perr=[];window.addEventListener('error',function(e){window.__perr.push(String(e.message||e.filename))});window.addEventListener('unhandledrejection',function(e){window.__perr.push('rej:'+String(e.reason))});"
       injectionTime:WKUserScriptInjectionTimeAtDocumentStart
     forMainFrameOnly:YES]];
  WKWebView* web = [[WKWebView alloc] initWithFrame:effect.bounds
                                      configuration:config];
  web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
  [web setValue:@NO forKey:@"drawsBackground"];
  web.navigationDelegate = self;
  [effect addSubview:web];

  // NSURL URLWithString 对畸形串返回 nil:NSLog 留痕(Task 16 取证用)且
  // 不 loadRequest——面板空白但失焦链(NSApp run)仍活,宿主可正常关闭;
  // 不做这层防御,requestWithURL:nil 属未定义形态。
  NSURL* pageURL = [NSURL URLWithString:self.launchURL];
  if (pageURL) {
    [web loadRequest:[NSURLRequest requestWithURL:pageURL]];
  } else {
    NSLog(@"panelwin: --url 解析为 nil,面板不加载内容(失焦自退链仍生效)");
  }

  // 激活双保险:不取焦点则失焦即退永不触发。
  [panel makeKeyAndOrderFront:nil];
  [NSApp activateIgnoringOtherApps:YES];

  // 上屏取证(Task 16 探针):0.5s 后回报 occlusion/key 状态——Space 事故
  // (窗口在册但不在屏)在这里一行日志就能归因。
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.5 * NSEC_PER_SEC)),
                 dispatch_get_main_queue(), ^{
    NSLog(@"panelwin: frame=%@ visible=%d occlusion=%lu key=%d",
          NSStringFromRect(panel.frame),
          panel.isVisible ? 1 : 0,
          (unsigned long)panel.occlusionState,
          panel.isKeyWindow ? 1 : 0);
  });
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

    PanelAppDelegate* delegate = [[PanelAppDelegate alloc] init];
    delegate.allowOrigin = originOfURL(urlStr);
    delegate.dashboardURL = dashStr;
    delegate.peerURL = peerStr;
    delegate.launchURL = urlStr;
    delegate.contentRect = NSMakeRect((CGFloat)x, bottomLeftY, (CGFloat)w, (CGFloat)h);
    [NSApp setDelegate:delegate];

    // ⚠️ 窗口创建必须等 applicationDidFinishLaunching(见上方委托方法)——
    // 2026-11-28 实机走查坐实:run loop 起来之前建窗口,Spaces 会把它钉在
    // 错误位置(窗口服务器在册、CGWindowList onscreen 有名,但【不在当前
    // Space 合成】且永不成为 key,失焦自退链死)。AppKit 惯例:窗口创建
    // 不早于 app 完成启动。
    [NSApp activateIgnoringOtherApps:YES];

    // 不返回——退出只发生在 exit(0)/exit(60) 两个出口上。
    [NSApp run];
  }
}
