import { expect } from '@playwright/test';

// 聊天页 e2e 共享桩（chat-settings / chat-device-sessions / chat-unify /
// chat-stop / chat-steer-stream 等六份 spec 消重的唯一真源）：
//   installChatStub(page, options) —— route mock（/api/auth/session 固定快照、
//     /api/agent/sessions 可覆写清单、可选 /api/agent/session 详情、其余空
//     答复）+ addInitScript 注入 FakeWebSocket 与 window.__aiFrames。
//   openChat(page, options) —— installChatStub + 进聊天页（默认再点侧栏「+」
//     新建会话，newSession:false 跳过）。
//   pushFrame(page, frame) —— 向最新 socket 实例注入一帧入站事件。
//   expectedSourceBadgeCount(sessions) —— 侧栏来源角标数的镜像规则。
//
// addInitScript 先于页面任何脚本执行——「app 代码运行前」的时序语义（如
// __CHAT_IDLE_TIMEOUT_MS__ 注入）由 Playwright 该保证兜住；需先于桩注入的
// 用例仍应先 addInitScript 再调 openChat，与既往写法一致。

// 出站帧全量入 window.__aiFrames；socket 实例入 FakeWebSocket.instances 供
// pushFrame 注入。subscribe 仅在 window.__snapshot 非空时回快照（null=不回
// 包，覆盖「未订阅静默」）；create 走 createResponder、ai.message 走
// aiMessageResponder（预设立于下方两张表，亦可传自备纯函数覆写）。
// mode：'live'（默认，全链路回包）| 'offline'（任何 send 立即由服务端语义
// close(1011)，不回任何回执——必须死在 create 回执之前，否则确认门放行
// ai.message、「不回退」断言必失败）| 'idle'（create 正常回执——否则确认门
// 5s 超时走早退分支、看门狗根本没机会武装——此后 subscribe/ai.message 起
// 的一切静默）。后两者即 chat-unify 的 WS_STUB(mode) 参数化语义。
export function buildChatWsStub({
  mode = 'live',
  createResponder = 'created',
  aiMessageResponder = 'ai.done',
} = {}) {
  return `
  window.__aiFrames = [];
  window.__snapshot = null;
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  const __mode = ${JSON.stringify(mode)};
  const __createResponder = ${serializeResponder(CREATE_RESPONDERS, 'createResponder', createResponder)};
  const __aiMessageResponder = ${serializeResponder(MESSAGE_RESPONDERS, 'aiMessageResponder', aiMessageResponder)};
  class FakeWebSocket {
    // 应用代码以 WebSocket.OPEN 等静态常量做就绪判定（readyState === WebSocket.OPEN），
    // stub 替换全局后若缺这些常量判定恒假、sendAI 永不发帧——必须补齐。
    static OPEN = 1;
    static CONNECTING = 0;
    static CLOSING = 2;
    static CLOSED = 3;
    constructor(url) {
      this.url = url;
      this.readyState = 0;
      FakeWebSocket.instances.push(this);
      setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
    }
    send(data) {
      const frame = JSON.parse(data);
      window.__aiFrames.push(frame);
      if (__mode === 'offline') {
        setTimeout(() => this.onclose?.({ code: 1011, reason: 'device agent offline' }), 0);
        return;
      }
      if (frame.type === 'ai.session.create') {
        setTimeout(() => {
          if (!this.onmessage) return;
          this.onmessage({ data: JSON.stringify(__createResponder(frame)) });
        }, 0);
        return;
      }
      if (__mode === 'idle') return;
      if (frame.type === 'ai.session.subscribe') {
        setTimeout(() => {
          if (!this.onmessage || !window.__snapshot) return;
          this.onmessage({ data: JSON.stringify({
            type: 'ai.session.snapshot',
            session_id: frame.session_id,
            ...window.__snapshot,
          }) });
        }, 0);
        return;
      }
      if (frame.type === 'ai.message') {
        setTimeout(() => {
          if (!this.onmessage) return;
          const payload = __aiMessageResponder(frame);
          if (payload) this.onmessage({ data: JSON.stringify(payload) });
        }, 0);
      }
    }
    close() {}
  }
  FakeWebSocket.instances = [];
  window.WebSocket = FakeWebSocket;
`;
}

// 回包器以函数源码注入页面：预设或自备函数都必须是纯函数——只允许引用
// frame 与 window，不得捕获闭包变量（toString 后闭包即断）。
const CREATE_RESPONDERS = {
  // 默认：create 正常回执（确认门放行的前提）。
  created: (frame) => ({ type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode, project_path: frame.project_path || '/home/e2e', provider: frame.provider, model: frame.model || '', effort: frame.effort || '', state: 'idle' }),
  // chat-settings 语义：window.__failCreate（用例可随时 page.evaluate 翻转）
  // 为真时回 ai.error（模拟目录不存在），否则照常回执。
  failCreate: (frame) => window.__failCreate
    ? { type: 'ai.error', session_id: frame.session_id, error: 'stat /no/such/dir: no such file or directory' }
    : { type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode, project_path: frame.project_path || '/home/e2e', provider: frame.provider, model: frame.model || '', effort: frame.effort || '', state: 'idle' },
};

const MESSAGE_RESPONDERS = {
  // 默认：ai.message 即回终态 ai.done（复位 sending，供连发用例）。
  'ai.done': (frame) => ({ type: 'ai.done', session_id: frame.session_id }),
  // chat-stop 语义：run 保持 running 不落终态，供停止键断言（终态由用例
  // pushFrame 注入 ai.done 收敛）。
  'ai.run.started': (frame) => ({ type: 'ai.run.started', session_id: frame.session_id, message_id: frame.message_id }),
  // 静默（挂死类场景自备）。
  silent: () => null,
};

function serializeResponder(presets, name, value) {
  if (typeof value === 'function') return value.toString();
  const preset = presets[value];
  if (!preset) {
    throw new Error(`chatStub: 未知 ${name} 「${String(value)}」（可用预设：${Object.keys(presets).join(' / ')}；或传自备纯函数）`);
  }
  return preset.toString();
}

const AUTH_SESSION_BODY = JSON.stringify({
  code: 0,
  data: { type: 'session_snapshot', instance_id: 'e2e', revision: 1, state: 'active', user: { id: 1, username: 'alice', email: 'a@b.c' } },
});

// detail：配置后启用 /api/agent/session 详情 mock——
//   transcript：默认转录行（transcript_page 按 limit:40 回）；
//   notFound：详情端点回 404（必须走本 route 表——Playwright 先注册者优先，
//     事后 addRoute 压不过这里的通配兜底）；
//   pages：按调用顺序弹出的分页 mock（rows + page，验证分页拉全；耗尽后
//     回空页），log：可选数组按序记录每次请求的 {limit, before}。
export async function installChatStub(page, {
  sessions = [],
  mode = 'live',
  createResponder = 'created',
  aiMessageResponder = 'ai.done',
  detail = null,
} = {}) {
  await page.addInitScript(buildChatWsStub({ mode, createResponder, aiMessageResponder }));
  await page.addInitScript(() => localStorage.setItem('alianggate.onboarding.v2', 'completed'));
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      await route.fulfill({ contentType: 'application/json', body: AUTH_SESSION_BODY });
      return;
    }
    if (url.pathname === '/api/agent/sessions') {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: { sessions } }) });
      return;
    }
    if (url.pathname === '/api/agent/session' && detail) {
      await fulfillDetail(route, url, detail);
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
}

async function fulfillDetail(route, url, { transcript = [], notFound = false, pages = null, log = null }) {
  if (notFound) {
    await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ code: 404, msg: 'not found' }) });
    return;
  }
  if (pages) {
    if (log) log.push({ limit: url.searchParams.get('limit'), before: url.searchParams.get('before') });
    const nextPage = pages.length ? pages.shift() : { rows: [], page: { has_more: false } };
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({ code: 0, data: { id: url.searchParams.get('id'), transcript: nextPage.rows, transcript_page: nextPage.page } }),
    });
    return;
  }
  await route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({ code: 0, data: { id: url.searchParams.get('id'), transcript, transcript_page: { limit: 40, count: transcript.length } } }),
  });
}

// 进聊天页（默认顺带点侧栏「+」新建本地会话并等设置条出现；device 清单类
// spec 传 newSession:false 只进入，由用例自行点开会话/新建）。
export async function openChat(page, options = {}) {
  const { newSession = true, ...stubOptions } = options;
  await installChatStub(page, stubOptions);
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
  if (newSession) {
    await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 侧栏「+」新建会话
    await expect(page.getByTestId('chat-settings-directory')).toBeVisible();
  }
}

// 向页面注入一帧 WS 事件（走最新一个 socket 实例的 onmessage）。socket 是
// 惰性建立的（发消息/订阅才 ensureAISocket），注入前必须确认实例存在。
export async function pushFrame(page, frame) {
  await page.waitForFunction(() => window.WebSocket.instances.length > 0);
  await page.evaluate(f => {
    const ws = window.WebSocket.instances[window.WebSocket.instances.length - 1];
    ws.onmessage?.({ data: JSON.stringify(f) });
  }, frame);
}

// 建会话 + 收敛正锚（Task 13 自 chat-settings.spec.js 迁入，并按 Task 12 质量
// 审查钉死结论加固为正锚）：发一句让 create 确认门 → ai.message 全链走完。
// 正锚两头：①outbound ai.message 帧存在（create-fail 路径没有它——弱阴性杀手）；
// ②run 以终态收敛（停止键退场 = 入站 ai.done/ai.error 已到达并走完收尾）。
// 注意 __aiFrames 只录出站帧，入站终态只能从 UI 键位读。返回该 ai.message 帧
// 的 session_id（本地会话 == aiSessionId）。
export async function establishCreatedSession(page, text = '第一句') {
  await page.getByPlaceholder(/输入消息|Type a message/).fill(text);
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() => {
    const msg = window.__aiFrames.filter((f) => f.type === 'ai.message').slice(-1)[0];
    return msg ? msg.session_id : '';
  })).not.toBe('');
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0); // 终态收敛锚
  return page.evaluate(() => window.__aiFrames.filter((f) => f.type === 'ai.message').slice(-1)[0].session_id);
}

// 侧栏来源角标数的镜像实现（与 sessionSourceBadge 同一条优先级规则：
// running > origin > id 前缀），供清单类 spec 在 openChat 后校验角标数。
export function expectedSourceBadgeCount(sessions) {
  return sessions.filter((s) => {
    if (!s || typeof s !== 'object') return false;
    if (s.status === 'running') return true;
    if (s.origin === 'phone' || s.origin === 'dashboard') return true;
    return typeof s.id === 'string' && (s.id.startsWith('claude_') || s.id.startsWith('codex_'));
  }).length;
}
