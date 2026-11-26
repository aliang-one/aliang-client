// api.js — 托盘面板唯一数据通道。
//
// 路由已按 2026-11-27 读码核实(app/http/routes/routes.go:184-193):
//   GET  /api/tray/summary   聚合读(app/http/services/tray_summary.go)
//   POST /api/run/start      启动代理
//   POST /api/run/stop       停止代理
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

// getSummary 的超时预算:面板 2s 轮询一次,超时 1.5s 保证不堆叠
// (实现计划 Task 7 指定值)。
export const SUMMARY_TIMEOUT_MS = 1500;

// 启停的预算放宽:StartService/StopService 内部有生命周期锁与 TUN 重建,
// 实测可超过秒级;1.5s 太紧会误报。
export const PROXY_ACTION_TIMEOUT_MS = 10000;

// restart 中 stop 与 start 之间的间隔,与托盘 companion 的 400ms 同值——给
// 端口释放/监听器拆除留时间,紧贴着 start 会撞 not_running 或端口占用。
export const RESTART_GAP_MS = 400;

// 底层请求:同源 fetch + AbortController 超时,HTTP 非 2xx 抛错。
async function fetchJson(path, { method = 'GET', timeoutMs } = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(path, { method, signal: controller.signal });
    if (!res.ok) {
      throw new Error(`${method} ${path} 失败: HTTP ${res.status}`);
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
async function proxyAction(path, { tolerateErrors = [] } = {}) {
  const body = await fetchJson(path, { method: 'POST', timeoutMs: PROXY_ACTION_TIMEOUT_MS });
  unwrapEnvelopeOrThrow(body, path);
  const data = body && typeof body === 'object' ? body.data : undefined;
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

// 重启代理:stop → 间隔 RESTART_GAP_MS → start(服务端无 restart 路由,
// 与托盘 companion.restartProxy 同语义)。stop 的 not_running 不拦流程。
export async function restartProxy() {
  await stopProxy();
  await new Promise((resolve) => setTimeout(resolve, RESTART_GAP_MS));
  return startProxy();
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
