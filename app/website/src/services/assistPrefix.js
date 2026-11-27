// 远程协助 P2(Task 3):仪表盘 SPA 相对化改造的 API 前缀自检。
//
// 仪表盘现在有两个挂载点:
//   ①根挂载(core embed,/)——原语义,前缀为空串;
//   ②管理端镜像挂载(/assist/service/,Task 2 同构代理反代本机 dashboard)
//     ——静态资产靠 index.html 相对引用 + vite base './' 自解析,而
//     运行时的 fetch/XHR/EventSource 调用必须显式带上挂载前缀,否则代理
//     剥不掉前缀、请求打不回本机 API。
//
// 约定:返回值不带尾斜杠,调用方统一 `assistPrefix() + '/api/...'` 拼接
// (根挂载时即 '/api/...',与历史行为逐字节一致)。
//
// 前置条件:两端注册均为标准 ServeMux(裸路径 301 归一补尾斜杠);换非重定
// 向路由则本检测失效(检测锚在尾斜杠形态上,镜像页必落在 /assist/service/)。
//
// 已知不覆盖面(评审预裁决,勿当 bug 追):WebSocket 三处 builder
// (useAssist/assistStreamUrl、useTerminalSocket/terminalUrl、
// useAliangChat/aiSocketUrl)刻意不走本前缀——镜像 iframe 内 WS 功能降级
// 为已知限制,观看页宿主直连的 assist 流不经镜像、不受影响。
export function assistPrefix() {
  if (typeof location === 'undefined') return '';
  return location.pathname.startsWith('/assist/service/') ? '/assist/service' : '';
}
