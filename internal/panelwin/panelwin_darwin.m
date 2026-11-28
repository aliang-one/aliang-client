// panelwin_darwin.m — 托盘面板纯原生窗口壳,macOS 侧(实现计划 M2 Task 13)。
//
// 形态:无边框 NSPanel(不抢宿主焦点,canBecomeKey 覆写为 YES)+ 系统
// 材质层(NSVisualEffectView behindWindow)铺底 + WKWebView 透明底
// (drawsBackground=false 经 KVC 私有面;公证探针随 Task 16 同批)——
// 毛玻璃归系统材质层,面板前端 hosted 态只画半透明底(Task 7 评审固化)。
//
// 生命周期(2026-11-28 撤销方案 B「首次后常驻」,回归「点击才拉起+关闭
// 即退」):companion 点图标 spawn 本进程,窗口随进程生、随进程死——
// 点面板外(global mouseDown)/失焦(resignKey)→ exit(0) 正常关闭;托盘
// 图标的 close 请求(SIGUSR1,handler 在 panelwin_darwin.go)→ exit(0);
// 页面 quit 消息 → exit(60);open 消息 → NSWorkspace openURL 开默认浏览器
// 后驻留;resize 消息 → 跟随改窗高(高度自适应),且【首次】resize 到位
// 才真正上屏(首帧尺寸修正,见 applicationDidFinishLaunching/showIfAwaitingFirstResize)。
// 进程退出即窗口消失,
// 不存在任何驻留形态的残留窗口。
//
// ⚠️ 撤销方案 B 的实证依据(2026-11-28 实机走查三连败,同根=常驻的隐藏
// WKWebView 窗口在该 macOS 上不可靠):
//  ① orderOut 后 makeKeyAndOrderFront/orderFrontRegardless 均被防抢焦点
//     策略静默忽略(再也打不开);
//  ② 重显后 WebKit 内容不回复绘制(空白框);
//  ③ 显示路径 makeKey 抢用户焦点→被夺回→0.12s 内 resignKey 自动再隐藏。
//
// 点面板外即收(全局鼠标监听实现):global monitor 对鼠标事件【无需】TCC
// 授权(仅键盘事件需要 Input Monitoring)——面板未成为 key 也能收到
// 「面板外」的点击。菜单栏带 y<40 排除:托盘图标自身的点击是 companion 的
// 手势(close 请求由它发),不能被本监听抢先关闭。
//
// 退出码契约:0=正常关闭(点面板外/失焦/图标请求关闭/孤儿自检)、60=quit、
// 其余=崩溃。
//
// 进程是叶子进程,窗口/对象生命周期=进程存活,不做显式释放。
//
// 内存纪律(同 third_party/systray/systray_darwin.m 的教训,上游 #135):
// 会被 Go goroutine 所在线程直接调到的 C 入口(panelwin_run)函数体整体
// 包一层 @autoreleasepool——Go 运行时线程没有 autorelease pool,入口内
// 创建的任何 autoreleased 中间对象就地排空。ARC 负责所有权,全文件不手写
// autorelease 消息;delegate/窗口对象存活到进程结束(叶子进程,不释放)。
// delegate 的 ObjC 方法(WKScriptMessageHandler / NSWindowDelegate /
// WKNavigationDelegate)全部由 AppKit 主 run loop 调用,run loop 自带
// 逐事件 pool,无需自管;dispatch_async 到主队列的块也由主 run loop 的
// 逐事件 pool 排空。
//
// 激活面:初始上屏【刻意不 makeKey】(撤销方案 B 定谳的失败模式③:makeKey
// 抢用户焦点→被夺回→resignKey 自动再隐藏)——用户点面板内部一次后
// becomeKey,失焦即退链随即激活;此前点面板外由全局鼠标监听兜底关闭。
//
// ⚠️ 窗口创建时序(2026-11-28 实机走查坐实,见 applicationDidFinishLaunching):
// 窗口必须在 app 完成启动后创建, Spaces 归属/key 资格才有正确语义。

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// panelwinGoHeightChanged — Go 侧高度落盘回调(cgo 导出,panelwin_darwin.go
// //export 实现):resize 应用新高度后把稳态逻辑高度写 ~/.aliang/panel/last-height,
// 下次启动 cmd/panel.go 读回作出生尺寸,窗口出生即正确尺寸(首帧尺寸修正
// Part 2)。声明与 Go 导出签名逐字对齐,漂移=链接失败(fail loud)。
extern void panelwinGoHeightChanged(double height);

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
@property (assign) BOOL everKeyed;         // 曾成为 key(见 resignKey 守卫)
// awaitingFirstResize —— 首帧尺寸修正 Part 1(2026-11-28 用户反馈「点击
// 瞬间出现大空白区域再缩到目标尺寸」):窗口创建后【不上屏】,置位等待
// 页面首次 resize(量出的实际高度)应用后再上屏——首帧即正确尺寸,不再
// 闪缺省 560 高的空白。1.2s 兜底(见 applicationDidFinishLaunching)保证
// 页面加载失败时面板仍会出现。
@property (assign) BOOL awaitingFirstResize;
- (void)verifyOnscreenRetry:(NSInteger)attempt forPanel:(NSPanel *)panel;
- (void)showIfAwaitingFirstResize;
@end

@implementation PanelAppDelegate

// 失焦 = 正常关闭(exit 0):面板点到外部/宿主切换即退出——窗口随进程
// 消失,残留不可能存在(「关闭即退」)。退出码 0=正常关闭契约,launcher
// 零惩罚零重拉。
// ⚠️ everKeyed 守卫:从未成为过 key 的面板不响应失焦——spawn 发生在托盘
// 点击的 key 处理中途,理论上一出生就被夺 key 的 resignKey 会误伤(面板
// 一出生就退,用户视角「点击没反应」);初始上屏已不 makeKey(见文件头
// 激活面),本守卫是 belt-and-suspenders。首次真实点击面板(becomeKey)后
// 守卫放行,失焦即退链即活。
- (void)windowDidResignKey:(NSNotification *)notification {
  if (!self.everKeyed) {
    NSLog(@"panelwin: resignKey 忽略(从未成为 key,spawn 期 key 抖动防误伤)");
    return;
  }
  NSLog(@"panelwin: resignKey → 正常关闭(失焦即退,exit 0)");
  exit(0);
}

- (void)windowDidBecomeKey:(NSNotification *)notification {
  self.everKeyed = YES;
  NSLog(@"panelwin: becomeKey(失焦即退链武装)");
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
      if (maxH <= 0) {
        // newH 钳后 ≤0 的守卫(Minor-5):topY-8 ≤ 0——托盘在负坐标屏
        // (全局坐标系下移)形态时钳不出正高度,硬钳会把窗压没;保持原
        // 矩形不动,NSLog 留痕(高度自适应在该形态下让位;不落盘——未
        // 应用的高度没有回放意义)。
        NSLog(@"panelwin: resize 钳高上限非正 (topY=%f), 保持原矩形", topY);
      } else {
        if (newH > maxH) {
          newH = maxH;
        }
        // 顶边固定向下生长:origin.y = 顶边 - 新高,宽与 x 不动。
        [self.panel setFrame:NSMakeRect(f.origin.x, topY - newH, f.size.width, newH)
                     display:YES
                      animate:NO];
        // 高度持久化(Part 2):应用后的稳态逻辑高度落盘,下次启动读回作
        // 出生尺寸(经 cgo 导出回调,Go 侧写文件;失败静默降级)。此前的
        // maxH≤0 保持原矩形形态不落盘。
        panelwinGoHeightChanged(newH);
      }
    }
    // 首帧尺寸修正 Part 1:检查等待位——收到 resize 即页面已活,首次时按
    // 当前尺寸上屏(高度已应用则首帧即正确尺寸;height 畸形 heightNum=nil
    // 时先按出生尺寸上屏,真实高度随后续 resize 到位再跟随——页面活着,
    // 多等 1.2s 兜底不如先出现);后续 resize 时位已清,零操作。
    [self showIfAwaitingFirstResize];
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
  // 不 loadRequest——面板空白但失焦即退链(NSApp run)仍活,宿主可正常关闭;
  // 不做这层防御,requestWithURL:nil 属未定义形态。
  NSURL* pageURL = [NSURL URLWithString:self.launchURL];
  if (pageURL) {
    [web loadRequest:[NSURLRequest requestWithURL:pageURL]];
  } else {
    NSLog(@"panelwin: --url 解析为 nil,面板不加载内容(失焦即退链仍生效)");
  }

  // 初始上屏(首帧尺寸修正 Part 1,2026-11-28 三版):【延迟到首次 resize
  // 到位】。背景:窗口出生=缺省高 560(--ph 缺省),页面渲染后量实际高度
  // → resize → 窗口才缩放——此前 150ms 早屏会把 560 的空窗先闪出来再缩到
  // 实际高度,用户反馈「点击瞬间出现大空白区域再缩到目标尺寸」。现改为
  // 窗口创建后不上屏,置 awaitingFirstResize 等待;首次 resize 应用新高度
  // (保持顶边固定)后经 showIfAwaitingFirstResize 真正上屏,首帧即正确
  // 尺寸。上屏合成自检重试(verifyOnscreenRetry,二版机制见彼处注释:
  // companion 托盘点击 spawn 撞系统 key/space 切换震荡期,orderFront 可能
  // 落进「已注册但永不合成」的悬挂态)挂到真正上屏动作之后。⚠️ 刻意不
  // makeKey(撤销方案 B 失败模式③:makeKey 抢用户焦点→被夺回→resignKey
  // 自动再隐藏)——用户点面板内部一次后 becomeKey,失焦即退链随即激活。
  // 页面加载在等待期照常。
  self.awaitingFirstResize = YES;
  // 兜底超时:1.2s 后若仍在等待(1.2s 内未收到 resize:rAF 挂起或页面
  // 加载慢),按当前尺寸直接上屏——面板不能永远不出现(正常路径 resize
  // 先到,本块进来看到位已清,零操作)。文案如实(P3):首报链路是
  // nextTick→同步量高,不依赖 rAF;但旧版经 rAF 合帧时未上屏窗口 rAF 被
  // WebKit 挂起、resize 永远发不出是该兜底的已知诱因之一,「页面加载失败?」
  // 的单因猜测不实。
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.2 * NSEC_PER_SEC)),
                 dispatch_get_main_queue(), ^{
    if (self.awaitingFirstResize) {
      NSLog(@"panelwin: 1.2s 兜底上屏(1.2s 内未收到 resize:rAF 挂起或页面加载慢,按当前尺寸上屏)");
      [self showIfAwaitingFirstResize];
    }
  });

  // 原生圆角(2026-11-28 用户反馈:CSS 的 --panel-radius 只作用内层 .panel
  // div,它贴满窗口后被窗口边界裁成方角——「外边框始终方方正正」的真相)。
  // contentView 打 layer 圆角+裁边,裁的是【整个窗口】的四个角;圆角外
  // 露出的是透明区(opaque=NO+clearColor 不变),悬浮在内容上方无违和。
  // 半径 16 与前端 --panel-radius 同步(app/website/src/panel/theme.css
  // :root 几何常量),改动需两处同批。
  panel.contentView.wantsLayer = YES;
  panel.contentView.layer.cornerRadius = 16;
  panel.contentView.layer.masksToBounds = YES;

  // 点面板外即收:global mouseDown(本窗口内的点击走本地路径不进此监听,
  // 故凡到达此处的点击必然在面板外)=正常关闭 exit(0);菜单栏带(y<40)
  // 排除——托盘图标自身的点击是 companion 的手势(close 请求由它发),
  // 不能被本监听抢先关闭(否则形成「点一次没反应」竞态)。handler 在注册
  // 线程(主线程)回调,仍走 dispatch_async 保险。
  [NSEvent addGlobalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown
                                         handler:^(NSEvent *event) {
    NSPoint loc = event.locationInWindow; // global monitor:屏幕全局坐标(左上原点)
    if (loc.y < 40) {
      return;
    }
    dispatch_async(dispatch_get_main_queue(), ^{
      NSLog(@"panelwin: 点面板外 → 正常关闭(exit 0)");
      exit(0);
    });
  }];

  // 上屏取证(Task 16 探针):1s 后回报 occlusion/key 状态——初始上屏延迟
  // 600ms,探针随之挪到 1s 以观测上屏之后的状态;Space 事故(窗口在册但
  // 不在屏/悬挂窗口)在这里一行日志就能归因。
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC)),
                 dispatch_get_main_queue(), ^{
    NSLog(@"panelwin: frame=%@ visible=%d occlusion=%lu key=%d",
          NSStringFromRect(panel.frame),
          panel.isVisible ? 1 : 0,
          (unsigned long)panel.occlusionState,
          panel.isKeyWindow ? 1 : 0);
  });
}

// showIfAwaitingFirstResize — 首次上屏的唯一入口(首帧尺寸修正 Part 1):
// awaitingFirstResize 置位时 orderFrontRegardless 上屏+清位+合成自检重试
// (verifyOnscreenRetry 挂到真正上屏动作之后);位已清(已上屏)零操作。
// 两处调用:首次 resize 到位(resize 分派内,高度应用之后)、1.2s 兜底
// 超时。⚠️ 刻意不 makeKey(见文件头激活面与初始上屏注释:抢用户焦点会被
// 夺回,resignKey 自动再隐藏)。主线程专用(两处调用都在主 run loop),
// 无并发面。
- (void)showIfAwaitingFirstResize {
  if (!self.awaitingFirstResize || !self.panel) {
    return;
  }
  self.awaitingFirstResize = NO;
  NSLog(@"panelwin: 首次上屏(等待位清,面板按当前高度出现)");
  [self.panel orderFrontRegardless];
  [self verifyOnscreenRetry:1 forPanel:self.panel];
}

// verifyOnscreenRetry:上屏合成自检+重试(「早屏+探测重试」二版;悬挂窗口
// 机制见初始上屏处注释)。每次:查本窗口号是否在合成列表(CGWindowList
// onscreen),已合成→确认收工;未合成→再补一次 orderFrontRegardless 并按
// 150/350/600/950ms 递增间隔续检(累计 ~1.2s,越过托盘点击震荡期);5 次
// 耗尽仍未合成→NSLog 留 Task 16 探针。用委托方法而非递归 block,避免
// block 自捕获的 retain cycle。self=panelDelegate(仅主线程读,见文件头)。
- (void)verifyOnscreenRetry:(NSInteger)attempt forPanel:(NSPanel *)panel {
  if (!panel) {
    return;
  }
  dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)),
                 dispatch_get_main_queue(), ^{
    NSArray *onscreen = CFBridgingRelease(CGWindowListCopyWindowInfo(
        kCGWindowListOptionOnScreenOnly, kCGNullWindowID));
    NSNumber *winNum = @(panel.windowNumber);
    BOOL comp = NO;
    for (NSDictionary *w in onscreen) {
      if ([w[(NSString *)kCGWindowNumber] isEqualToNumber:winNum]) { comp = YES; break; }
    }
    if (comp) {
      NSLog(@"panelwin: 上屏合成确认(attempt %ld)", (long)attempt);
      return;
    }
    if (attempt >= 5) {
      NSLog(@"panelwin: 上屏重试耗尽仍未合成(留 Task 16 探针)");
      return;
    }
    NSLog(@"panelwin: 未合成(attempt %ld),重试 orderFront", (long)attempt);
    [panel orderFrontRegardless];
    [self verifyOnscreenRetry:attempt + 1 forPanel:panel];
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
    // 不早于 app 完成启动。⚠️ 不调 activateIgnoringOtherApps——初始上屏
    // 不 makeKey(见文件头激活面),程序化激活只会抢用户焦点。

    // 不返回——退出只发生在 exit(0)(点面板外/失焦/close 请求/孤儿自检)
    // 与 exit(60)(quit)两个出口上。
    [NSApp run];
  }
}
