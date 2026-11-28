// 远程协助观看页测试公共夹具:import 失败恢复族(rrImportFailure /
// rrImportRetry / rrImportCapEdge 三文件)共享的假体与挂载助手。
//
// 为什么三个文件、每文件一条「失败→恢复」弧:vi.mock('rrweb') 的模块在
// 首次成功求值后按测试文件缓存——一条弧里 import 从失败翻成功后,同文件的
// 后续用例再也拿不到失败形态。三个场景各自需要独立的 fail→success 求值序,
// 各占一个文件;vi.mock 本体(xterm/agentApi/rrweb)留在各测试文件内
// (vi.mock 文件级提升,不能共享),本模块只共享无 mock 依赖的助手。
//
// 各测试文件的 rrweb mock 工厂以 mocks.rrwebImportShouldFail=true 开局
// (抛错的工厂不进缓存,下一次 import 重新求值——正是「失败后可重试」的
// 现实形态:Vite 重部署把旧 chunk hash 下架 → import 404)。
import { createApp, h, nextTick } from 'vue';
import AssistWatchPage from '../AssistWatchPage.vue';
import { useAssist } from '../../../composables/useAssist';
import { useNavigation } from '../../../composables/useNavigation';

export class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    FakeWebSocket.instances.push(this);
  }
  send() {}
  close() { this.readyState = FakeWebSocket.CLOSED; }
}

const { currentPage } = useNavigation();
export { currentPage };

export function snap(overrides = {}) {
  return { state: '', session_id: '', message: '', ...overrides };
}

let apps = [];

// mountActive 以 active 态挂页并打开 watch WS(动作与主测试文件的
// mountActive 同款;apps 登记供 afterEach 统一卸载)。
export async function mountActive() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const a = useAssist();
  a.state.value = 'active';
  a.sessionId.value = 's1';
  const app = createApp({ render: () => h(AssistWatchPage) });
  app.mount(el);
  await nextTick();
  await nextTick();
  apps.push(app);
  const sock = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  return { a, m: { el }, sock };
}

export function unmountAll() {
  for (const app of apps) { try { app.unmount(); } catch (_) { /* noop */ } }
  apps = [];
}

export function send(sock, frame) {
  sock.onmessage({ data: JSON.stringify(frame) });
}

let rrTs = 1700000000000;
export function rrMeta() {
  rrTs += 1;
  return { type: 4, timestamp: rrTs, data: { href: 'https://x.test/', width: 1000, height: 700 } };
}
export function rrFullSnap() {
  rrTs += 1;
  return { type: 2, timestamp: rrTs, data: { node: { type: 0, childNodes: [] }, initialOffset: { top: 0, left: 0 } } };
}
export function rrIncremental(source = 0) {
  rrTs += 1;
  return { type: 3, timestamp: rrTs, data: { source, texts: [] } };
}
