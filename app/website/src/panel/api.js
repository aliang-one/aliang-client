// api.js — 托盘面板唯一数据通道。
//
// 路由已按 2026-11-27 读码核实(app/http/routes/routes.go:184-193):
//   GET  /api/tray/summary   聚合读(app/http/services/tray_summary.go)
//   POST /api/run/start      启动代理
//   POST /api/run/stop       停止代理
//   POST /api/endpoints/activate  endpoint 组内互斥激活(面板 radio 钉选)
//   ——服务端【没有】/api/run/restart;托盘 companion 的重启语义就是
//   stop → 400ms → start(app/tray/companion.go restartProxy),面板照抄同语义。
//
// 信封契约(app/http/common/response.go):所有成功响应恒 HTTP 200 包
// {code:0, msg, data, timestamp};启停的"业务失败"(登录闸/not_running 等)
// 也走 200 信封,藏在 data 里:{error:"...", status:"failed", msg:"..."}
// (app/http/services/run.go StartService/StopService)。所以判断失败要看两层:
//   1) HTTP 非 2xx / 超时 / 网络错 → fetch 抛错
//   2) 信封 code!=0,或启停 data.status==='failed' → 主动抛错
//
// 依赖纪律:本文件绝不 import 仪表盘的 stores/composables,面板入口零重依赖。

const SUMMARY_PATH = '/api/tray/summary';
const RUN_START_PATH = '/api/run/start';
const RUN_STOP_PATH = '/api/run/stop';
// 模式切换:POST /api/run/swift(body {"mode":"http"|"tun"}),契约 2026-11-27
// 读码核实(routes.go:193 → HandleRunSwift → RunService.SwitchMode):
//   失败恒藏 200 信封 data:{status:'failed', error, msg}(invalid_mode /
//   wintun_required|wintun_installing / switch_failed);成功 data.status=
//   'switched',同值幂等 'unchanged'。⚠️ 运行中调用会同步停掉当前代理且
//   不拉起(SwitchMode 既有语义)——调用方必须只在停止态调用(面板拍板 2:
//   segmented 仅 stopped 可点)。
const RUN_MODE_PATH = '/api/run/swift';

// 组内互斥激活:POST /api/endpoints/activate(body {"protocol","id"}),契约
// 2026-11-27 读码核实(routes.go:462 → endpoint_handler.go HandleEndpointActivate):
//   成功 200 信封 data 回显 {protocol,id};业务失败走【真 HTTP 错误码】——
//   目标不存在/protocol 不匹配→404(ErrEndpointNotAvailable)、builtin 兜底行/
//   auto_group=0/已摘除→400(ErrEndpointNotActivatable)、registry 不可用→
//   HTTP 500+业务码 201。这些 common.Error* 路径的信封 body 恒带服务端人话
//   msg(app/http/common/response.go Error)。来源闸(终审修复批):回环 ∥
//   dashboard 会话双闸——面板与 core 同进程同源(回环)免 cookie 可用,与
//   /api/tray/summary、聊天 WS 握手同档;非回环来源无会话 → 401 信封英文
//   msg「Authenticated dashboard session is required」(LAN/远端防御态,面板
//   正常形态不会命中;组件 humanizeActionError 有对应转译兜底)。例外:
//   非 POST→405 是 handler 直接 http.Error("Method not allowed") 的纯文本、
//   【不是信封】;客户端恒发 POST,该分支实际不可达,万一读不到 msg 会回落
//   HTTP 文案。
const ENDPOINT_ACTIVATE_PATH = '/api/endpoints/activate';

// getSummary 的超时预算:面板 2s 轮询一次,超时 1.5s 保证不堆叠
// (实现计划 Task 7 指定值)。
export const SUMMARY_TIMEOUT_MS = 1500;

// 启停的预算放宽:StartService/StopService 内部有生命周期锁与 TUN 重建,
// 实测可超过秒级;1.5s 太紧会误报。
export const PROXY_ACTION_TIMEOUT_MS = 10000;

// 激活的预算对齐启停 10s 档(ActivateInGroup 是一次 List + 组内逐行整行写
// 穿,sqlite 上毫秒级;与启停同档纯粹是留足余量,plan Task 3 指定对齐)。
export const ENDPOINT_ACTIVATE_TIMEOUT_MS = PROXY_ACTION_TIMEOUT_MS;

// restart 中 stop 与 start 之间的间隔,与托盘 companion 的 400ms 同值——给
// 端口释放/监听器拆除留时间,紧贴着 start 会撞 not_running 或端口占用。
export const RESTART_GAP_MS = 400;

// readErrorEnvelopeMsg — 非 2xx 响应的信封 body 里也带服务端人话 msg
// (common.Error* 对错误码恒写完整 CommonResponse)。解析失败/无 msg 返回
// 空串,由调用方回落 HTTP 状态码文案。仅错误路径专用(res.body 只能消费
// 一次,成功路径不受影响)。
async function readErrorEnvelopeMsg(res) {
  try {
    const body = await res.json();
    if (body && typeof body === 'object' && typeof body.msg === 'string' && body.msg) {
      return body.msg;
    }
  } catch {
    // 非 JSON body(网关 HTML 错误页等)——回落状态码文案
  }
  return '';
}

// 底层请求:同源 fetch + AbortController 超时,HTTP 非 2xx 抛错。
// body(JSON 字符串)可选:仅 POST 动作使用,带上标准 Content-Type
// (服务端 DecodeRequest 只认 body 本身,头是规范习惯非硬依赖)。
// errorBodyMsg(默认关):非 2xx 时尽力捞信封 body 的服务端 msg 作为错误
// 信息——启用方是 activateEndpoint(它的业务失败走真 HTTP 错误码,msg 是
// 面板 toast 要直吐人话);不启用时保持既有「HTTP 状态码」文案,启停/读
// 路径行为零变化。
async function fetchJson(path, { method = 'GET', timeoutMs, body, errorBodyMsg = false } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(path, {
      method,
      signal: controller.signal,
      ...(body !== undefined ? { body, headers: { 'Content-Type': 'application/json' } } : {}),
    });
    if (!res.ok) {
      const serverMsg = errorBodyMsg ? await readErrorEnvelopeMsg(res) : '';
      throw new Error(serverMsg || `${method} ${path} 失败: HTTP ${res.status}`);
    }
    return await res.json();
  } catch (err) {
    if (err && err.name === 'AbortError') {
      throw new Error(`${method} ${path} 超时(${timeoutMs}ms)`);
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

// 信封校验:code 存在且非 0 视为业务失败(如 401 信封),取 msg 抛给人看。
function unwrapEnvelopeOrThrow(body, what) {
  if (body && typeof body === 'object' && body.code !== undefined && body.code !== 0) {
    throw new Error(`${what}失败: ${body.msg || `code=${body.code}`}`);
  }
  return body;
}

// 聚合读:返回原始信封 JSON(解包归一归 parseSummary,保持 api 层薄)。
// 超时/网络失败抛错——由 useSummary 的失败计数承接,本层不做重试。
export function getSummary() {
  return fetchJson(SUMMARY_PATH, { timeoutMs: SUMMARY_TIMEOUT_MS }).then((body) =>
    unwrapEnvelopeOrThrow(body, '读取面板状态')
  );
}

// 启停动作:除信封外,还要看 data.status==='failed'(业务失败恒 200 信封,
// 见文件头契约注释)。失败抛 Error(带服务端 msg),成功返回 data。
async function proxyAction(path, { tolerateErrors = [], body } = {}) {
  const payload = await fetchJson(path, { method: 'POST', timeoutMs: PROXY_ACTION_TIMEOUT_MS, body });
  unwrapEnvelopeOrThrow(payload, path);
  const data = payload && typeof payload === 'object' ? payload.data : undefined;
  if (data && typeof data === 'object' && data.status === 'failed') {
    // tolerateErrors:重启流程里 stop 撞 not_running 属正常(companion 同款
    // 容忍,app/tray/companion.go stopProxy),不算失败。
    if (data.error && tolerateErrors.includes(data.error)) {
      return data;
    }
    throw new Error(data.msg || data.error || '代理操作失败');
  }
  return data;
}

// 启动代理:POST /api/run/start。失败(登录闸/强制更新等)抛错。
export function startProxy() {
  return proxyAction(RUN_START_PATH);
}

// 停止代理:POST /api/run/stop。not_running 容忍(幂等,与托盘同口径)。
export function stopProxy() {
  return proxyAction(RUN_STOP_PATH, { tolerateErrors: ['not_running'] });
}

// 切换运行模式:POST /api/run/swift(契约见 RUN_MODE_PATH 注释)。失败
// (非法 mode/wintun 依赖/落盘失败)抛错带服务端 msg;'unchanged' 幂等属
// 成功。超时沿用启停 10s 档(SwitchMode 内含 wintun 刷新与配置落盘)。
export function setMode(mode) {
  return proxyAction(RUN_MODE_PATH, { body: JSON.stringify({ mode }) });
}

// 重启代理:stop → 间隔 RESTART_GAP_MS → start(服务端无 restart 路由,
// 与托盘 companion.restartProxy 同语义)。stop 的 not_running 不拦流程。
export async function restartProxy() {
  await stopProxy();
  await new Promise((resolve) => setTimeout(resolve, RESTART_GAP_MS));
  return startProxy();
}

// activateEndpoint — endpoint 组内互斥激活(面板 radio 钉选,POST
// /api/endpoints/activate,契约见 ENDPOINT_ACTIVATE_PATH 注释)。判失败与
// proxyAction 同「三层」但错误形态不同:本路由业务失败走真 HTTP 错误码
// (404/400/500,信封 body 仍带 code/msg),所以 HTTP 层经 errorBodyMsg 把
// 信封 msg 捞出来透传(前端 toast 直吐人话,如「已摘除的行请先在仪表盘
// 恢复」),不能只抛状态码;200 信封 code≠0 层防御性保留(惯例对齐
// getSummary)。成功返回 data(回显 {protocol,id})。
export async function activateEndpoint(protocol, id) {
  const payload = await fetchJson(ENDPOINT_ACTIVATE_PATH, {
    method: 'POST',
    timeoutMs: ENDPOINT_ACTIVATE_TIMEOUT_MS,
    body: JSON.stringify({ protocol, id }),
    errorBodyMsg: true,
  });
  unwrapEnvelopeOrThrow(payload, '激活 endpoint');
  return payload && typeof payload === 'object' ? payload.data : undefined;
}

// isHosted() — 宿主探测:WebView2 / WKWebView 任一在即为原生壳宿主态。
// 消费方:main.js 启动时据此给 <html> 加 hosted 类(theme.css 据此关掉页面
// 渐变底色,把底让给系统材质层——M2 壳 drawsBackground=false 时页面不透明
// 底色会盖住材质);与 postMessageHost 同文件同族(都在认宿主通道)。
export function isHosted() {
  if (typeof window === 'undefined') return false;
  if (window.chrome && window.chrome.webview) return true;
  if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.panel) {
    return true;
  }
  return false;
}

// postMessageHost(msg) — 把消息投给原生宿主:
//   Windows WebView2: window.chrome.webview.postMessage
//   macOS WKWebView:  window.webkit.messageHandlers.panel.postMessage
// 两个后端皆缺(如 M1 浏览器直开 panel.html 的无宿主模式)→ 返回 false,
// 不抛错——调用方以返回值判定「无宿主模式」。
export function postMessageHost(msg) {
  if (typeof window === 'undefined') return false;
  const chromeWebview = window.chrome && window.chrome.webview;
  if (chromeWebview && typeof chromeWebview.postMessage === 'function') {
    chromeWebview.postMessage(msg);
    return true;
  }
  const panelHandler =
    window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.panel;
  if (panelHandler && typeof panelHandler.postMessage === 'function') {
    panelHandler.postMessage(msg);
    return true;
  }
  return false;
}
