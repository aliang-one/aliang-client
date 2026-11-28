// summary.test.js — parseSummary 容错解析 + useSummary 轮询(失败计数/恢复/卸载停轮询)。
// 用 fake timers + mock global fetch,不打真实网络。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadSummarySnapshot, parseSummary, SUMMARY_SNAPSHOT_KEY, useSummary } from './summary';

// ---- parseSummary ---------------------------------------------------------

describe('parseSummary 容错解析', () => {
  it('信封形态({code,data}) 解包出 data', () => {
    const envelope = {
      code: 0,
      msg: 'success',
      timestamp: 1700000000,
      data: {
        version: 'v1.1.124',
        run: { is_running: true, mode: 'http', status_desc: '', listen_addr: '127.0.0.1:56432', blocked: { code: '', msg: '' } },
        ai: { enabled: false, providers: [] },
        agents: { kernels: [] },
        endpoints: { available: false, groups: [] },
      },
    };
    const out = parseSummary(envelope);
    expect(out).not.toBeNull();
    expect(out.version).toBe('v1.1.124');
    expect(out.run.is_running).toBe(true);
  });

  it('裸形态(无信封,直接是聚合对象) 原样归一', () => {
    const out = parseSummary({ version: 'x', run: { is_running: false } });
    expect(out).not.toBeNull();
    expect(out.version).toBe('x');
    expect(out.run.is_running).toBe(false);
  });

  it('字段缺失不炸:全部缺 → null 子源兜底', () => {
    const out = parseSummary({});
    expect(out).not.toBeNull();
    expect(out.version).toBe('');
    expect(out.run).toBeNull();
    expect(out.ai).toBeNull();
    expect(out.agents).toBeNull();
    expect(out.endpoints).toBeNull();
  });

  it('run 子源缺 blocked 字段 → 兜底 {code:"",msg:""}(不炸下游读取)', () => {
    const out = parseSummary({ run: { is_running: false } });
    expect(out.run.blocked).toEqual({ code: '', msg: '' });
  });

  it('未知字段忽略', () => {
    const out = parseSummary({ version: 'x', future_field: { nested: 1 }, run: { is_running: true, extra: 'y' } });
    expect(out.version).toBe('x');
    expect(out.run.is_running).toBe(true);
    expect(out.future_field).toBeUndefined();
    expect(out.run.extra).toBeUndefined();
  });

  it('类型错误值不炸:非对象输入 → null', () => {
    expect(parseSummary(null)).toBeNull();
    expect(parseSummary(undefined)).toBeNull();
    expect(parseSummary('oops')).toBeNull();
    expect(parseSummary(42)).toBeNull();
    expect(parseSummary([])).toBeNull();
  });

  it('run 是数组/字符串等坏类型 → 该子源降级 null,其余照常', () => {
    const out = parseSummary({ version: 'x', run: 'broken', ai: { enabled: true, providers: [] } });
    expect(out.run).toBeNull();
    expect(out.ai).not.toBeNull();
  });

  it('非零 code 信封(如 401) → null(无有效数据)', () => {
    expect(parseSummary({ code: 401001, msg: 'unauthorized', data: null })).toBeNull();
  });
});

// ---- useSummary -----------------------------------------------------------

// flushQueue:把 mock fetch 的 promise 链冲到落定(fake timers 下不能用真实 await 间隔)。
async function flushQueue() {
  await vi.advanceTimersByTimeAsync(0);
}

describe('useSummary 轮询', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function stubFetchOnce(impl) {
    const fetchMock = vi.fn(impl);
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
  }

  it('启动即拉取一次,成功后 summary 有值且 connLost=false', async () => {
    const payload = { code: 0, data: { version: 'v1', run: { is_running: true } } };
    stubFetchOnce(() => Promise.resolve({ ok: true, json: () => Promise.resolve(payload) }));
    const { summary, connLost } = useSummary();
    await flushQueue();
    expect(summary.value).not.toBeNull();
    expect(summary.value.version).toBe('v1');
    expect(summary.value.run.is_running).toBe(true);
    expect(connLost.value).toBe(false);
  });

  it('2s 周期轮询:推 4s 触发两次新拉取', async () => {
    const payload = { code: 0, data: { version: 'v1' } };
    const fetchMock = stubFetchOnce(() => Promise.resolve({ ok: true, json: () => Promise.resolve(payload) }));
    useSummary();
    await flushQueue();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(2000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(2000);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('连续 2 次失败 → connLost=true(一次失败不触发)', async () => {
    let calls = 0;
    stubFetchOnce(() => {
      calls += 1;
      return Promise.reject(new Error(`net down #${calls}`));
    });
    const { connLost } = useSummary();
    await flushQueue();
    expect(connLost.value).toBe(false); // 第 1 次失败:还没到阈值
    await vi.advanceTimersByTimeAsync(2000);
    expect(connLost.value).toBe(true); // 第 2 次失败:达到阈值
  });

  it('失败后成功即恢复:connLost 归 false 且失败计数清零', async () => {
    let calls = 0;
    const payload = { code: 0, data: { version: 'v2' } };
    stubFetchOnce(() => {
      calls += 1;
      if (calls <= 2) return Promise.reject(new Error('down'));
      return Promise.resolve({ ok: true, json: () => Promise.resolve(payload) });
    });
    const { connLost, summary } = useSummary();
    await flushQueue();
    await vi.advanceTimersByTimeAsync(2000);
    expect(connLost.value).toBe(true);
    await vi.advanceTimersByTimeAsync(2000);
    expect(connLost.value).toBe(false);
    expect(summary.value.version).toBe('v2');
  });

  it('恢复后再失败仍需重新累计 2 次(计数已清零)', async () => {
    let calls = 0;
    const payload = { code: 0, data: { version: 'v3' } };
    stubFetchOnce(() => {
      calls += 1;
      if (calls === 1 || calls === 2) return Promise.reject(new Error('down'));
      if (calls === 3) return Promise.resolve({ ok: true, json: () => Promise.resolve(payload) });
      return Promise.reject(new Error('down again'));
    });
    const { connLost } = useSummary();
    await flushQueue(); // #1 失败
    await vi.advanceTimersByTimeAsync(2000); // #2 失败 → lost
    await vi.advanceTimersByTimeAsync(2000); // #3 成功 → 恢复
    expect(connLost.value).toBe(false);
    await vi.advanceTimersByTimeAsync(2000); // #4 失败(第 1 次)
    expect(connLost.value).toBe(false);
    await vi.advanceTimersByTimeAsync(2000); // #5 失败(第 2 次) → lost
    expect(connLost.value).toBe(true);
  });

  it('HTTP 非 2xx 也计失败', async () => {
    let calls = 0;
    stubFetchOnce(() => {
      calls += 1;
      return Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({}) });
    });
    const { connLost } = useSummary();
    await flushQueue();
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls).toBe(2);
    expect(connLost.value).toBe(true);
  });

  it('组件卸载停轮询(unmount 后不再发请求)', async () => {
    const payload = { code: 0, data: { version: 'v1' } };
    const fetchMock = stubFetchOnce(() => Promise.resolve({ ok: true, json: () => Promise.resolve(payload) }));
    const { stop } = useSummary();
    await flushQueue();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    stop();
    await vi.advanceTimersByTimeAsync(6000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe('summary 快照预热(localStorage)', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('空缓存 → loadSummarySnapshot 返回 null', () => {
    expect(loadSummarySnapshot()).toBe(null);
  });

  it('写入快照键 → 往返解析出归一 summary(run 子源归一形态保持)', () => {
    const snapshot = { version: '1.1.139-local', run: { is_running: true, mode: 'http', status_desc: 'x', listen_addr: '127.0.0.1:56432' }, ai: { enabled: true }, agents: null, endpoints: null };
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY, JSON.stringify(snapshot));
    const parsed = loadSummarySnapshot();
    expect(parsed).not.toBe(null);
    expect(parsed.version).toBe('1.1.139-local');
    expect(parsed.run).toEqual({ is_running: true, mode: 'http', status_desc: 'x', listen_addr: '127.0.0.1:56432', blocked: { code: '', msg: '' } });
    expect(parsed.ai).toEqual({ enabled: true });
    expect(parsed.agents).toBe(null);
  });

  it('缓存垃圾(JSON 解析炸)→ null 不炸', () => {
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY, '{not-json');
    expect(loadSummarySnapshot()).toBe(null);
  });

  it('缓存非对象(数组/字符串)→ null 不炸', () => {
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY, '[1,2]');
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY + 'b', '"str"');
    expect(loadSummarySnapshot()).toBe(null);
    localStorage.setItem(SUMMARY_SNAPSHOT_KEY, '"str"');
    expect(loadSummarySnapshot()).toBe(null);
  });

  it('useSummary 轮询成功 → 快照键被写入(下次 spawn 预热)', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 0, data: { version: 'v' } }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const { stop } = useSummary();
    await vi.waitFor(() => {
      expect(localStorage.getItem(SUMMARY_SNAPSHOT_KEY)).not.toBe(null);
    });
    const cached = JSON.parse(localStorage.getItem(SUMMARY_SNAPSHOT_KEY));
    expect(cached.version).toBe('v');
    stop();
    vi.unstubAllGlobals();
  });
});
