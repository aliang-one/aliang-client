// 远程协助 P3 评审 Fix 1(1/3):import 失败·缓冲废弃路径(路径 b)。
// 封顶裁剪裁掉底座 → 整段清空回占位 → 新快照走新底座路径绕过 5s 冷却强制
// 重试 import → 恢复。
// 「每文件一条失败→恢复弧」的约束与共享夹具见 assistWatchHarness.js 头注。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { nextTick } from 'vue';

const mocks = vi.hoisted(() => ({
  termInstances: [],
  ro: { instances: [] },
  replayers: [],
  rrwebImportShouldFail: true, // 失败开局(弧约束见 harness 头注)
  rrwebImportCalls: 0,
}));

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    constructor(opts) {
      this.opts = opts;
      this.rows = 24;
      this.cols = 80;
      this.written = '';
      this.clearCalls = 0;
      this.disposed = false;
      mocks.termInstances.push(this);
    }
    loadAddon(addon) { this.addon = addon; }
    open(el) { this.openedAt = el; }
    write(data) { this.written += data; }
    clear() { this.clearCalls += 1; this.written = ''; }
    onData(cb) { this.dataCb = cb; return { dispose: () => { this.dataCb = null; } }; }
    dispose() { this.disposed = true; }
  },
}));

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    constructor() { this.fitCalls = 0; }
    fit() { this.fitCalls += 1; }
  },
}));

// 抛错的 mock factory 不进缓存,下一次 import 重新求值——先失败后成功即靠它。
vi.mock('rrweb', () => {
  mocks.rrwebImportCalls += 1;
  if (mocks.rrwebImportShouldFail) {
    throw new Error('chunk 404 (simulated redeploy)');
  }
  return {
    Replayer: class {
      constructor(initialEvents, config) {
        this.initialEvents = initialEvents;
        this.config = config;
        this.added = [];
        this.liveStarted = false;
        this.destroyed = false;
        this.iframe = {
          attrs: {},
          style: {},
          setAttribute(k, v) { this.attrs[k] = v; },
        };
        this.wrapper = { style: {} };
        mocks.replayers.push(this);
      }
      startLive() { this.liveStarted = true; }
      addEvent(ev) { this.added.push(ev); }
      destroy() { this.destroyed = true; }
    },
  };
});

const api = vi.hoisted(() => ({
  getAssistGates: vi.fn(),
  getAssistSession: vi.fn(),
  requestAssist: vi.fn(),
  cancelAssist: vi.fn(),
  endAssist: vi.fn(),
}));
vi.mock('../../../services/agentApi', () => api);

import { resetForTests } from '../../../composables/useAssist';
import {
  FakeWebSocket, currentPage, snap, mountActive, unmountAll, send,
  rrMeta, rrFullSnap, rrIncremental,
} from './assistWatchHarness';

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset();
  api.getAssistSession.mockResolvedValue(snap());
  resetForTests();
  FakeWebSocket.instances = [];
  globalThis.WebSocket = FakeWebSocket;
  mocks.termInstances.length = 0;
  mocks.ro.instances.length = 0;
  mocks.replayers.length = 0;
  currentPage.value = 'dashboard';
  global.ResizeObserver = class {
    constructor(cb) { this.cb = cb; mocks.ro.instances.push(this); }
    observe(el) { this.el = el; }
    disconnect() { this.disconnected = true; }
  };
});

afterEach(() => {
  vi.useRealTimers();
  unmountAll();
  resetForTests();
  document.body.innerHTML = '';
  delete global.ResizeObserver;
});

describe('AssistWatchPage rrweb import 失败·路径 b(缓冲废弃+新底座强制重试)', () => {
  it('封顶裁掉底座 → 清空回占位;残帧不触碰 import;新快照绕冷却强制重试恢复', async () => {
    const { m, sock } = await mountActive();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify(rrFullSnap()) });
    // 1999 条增量把 [合成Meta, FullSnapshot] 底座挤出 2000 帧帽 → trim 后
    // 无 Meta → 整段废弃(半截缓冲无渲染价值,不吃内存)。
    for (let i = 0; i < 1999; i += 1) {
      send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    }
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();
    expect(mocks.rrwebImportCalls).toBe(1); // 失败过一次
    expect(mocks.replayers.length).toBe(0);
    expect(m.el.querySelector('[data-testid="assist-watch-page-syncing"]')).not.toBeNull();

    // 废弃后的残帧落「无底座丢弃」路径,不触碰 import(无缓冲即无重试锚)
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await new Promise((r) => setTimeout(r, 0));
    expect(mocks.rrwebImportCalls).toBe(1);
    expect(mocks.replayers.length).toBe(0);

    // 新快照 → 新底座路径绕过冷却强制重试,import 成功 → 恢复
    mocks.rrwebImportShouldFail = false;
    const meta = rrMeta();
    const full2 = rrFullSnap();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify([meta, full2]) });
    await new Promise((r) => setTimeout(r, 0));
    await nextTick();
    expect(mocks.rrwebImportCalls).toBe(2); // 新底座绕冷却
    expect(mocks.replayers.length).toBe(1);
    expect(mocks.replayers[0].added).toEqual([meta, full2]);
    expect(m.el.querySelector('[data-testid="assist-watch-page-syncing"]')).toBeNull();
  });
});
