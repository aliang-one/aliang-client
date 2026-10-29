import { afterEach, describe, expect, it, vi } from 'vitest';

// agentApi → stores/auth → i18n（loadLocale 读 localStorage，node 测试环境无 DOM）：
// 与 RulesSettings.test.js 同套路，mock 掉 i18n 即可导入被测模块。
vi.mock('../i18n', () => ({ useI18n: () => ({ t: (k) => k, locale: { value: 'zh' }, setLocale: () => {} }) }));

import { AGENT_TRANSCRIPT_TIMEOUT_MS, getAgentSessionDetail, loadAgentTranscript } from './agentApi';

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function envelopeResponse(body) {
  return {
    ok: true,
    status: 200,
    json: vi.fn().mockResolvedValue({ code: 0, msg: '', data: body }),
  };
}

// 模拟 fetch 的中止语义：signal 触发 abort 时以 AbortError 拒绝，且 promise 永不成功。
function abortableFetch() {
  return vi.fn((_path, options) => new Promise((_resolve, reject) => {
    const signal = options && options.signal;
    if (signal) {
      signal.addEventListener('abort', () => {
        const err = new Error('The operation was aborted');
        err.name = 'AbortError';
        reject(err);
      });
    }
  }));
}

describe('loadAgentTranscript', () => {
  it('成功：返回会话数据（transcript 在 payload 里），请求带 id+limit', async () => {
    const detail = { id: 'ai_1', transcript: [{ role: 'user', content: 'hi' }] };
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(detail));
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadAgentTranscript('ai_1', { limit: 50 })).resolves.toBe(detail);
    const [path, options] = fetchMock.mock.calls[0];
    expect(path).toBe('/api/agent/session?id=ai_1&limit=50');
    expect(options.method).toBe('GET');
  });

  it('冷路径 40s 硬顶：超时抛 transcript-timeout 标记（重试按钮=重新调用）', async () => {
    vi.stubGlobal('fetch', abortableFetch());
    vi.useFakeTimers();

    const pending = loadAgentTranscript('ai_slow', { timeoutMs: AGENT_TRANSCRIPT_TIMEOUT_MS });
    pending.catch(() => {}); // 计时推进期不留「未处理拒绝」
    await vi.advanceTimersByTimeAsync(AGENT_TRANSCRIPT_TIMEOUT_MS - 1);
    await expect(Promise.race([pending.then(() => 'resolved'), Promise.resolve('pending')])).resolves.toBe('pending');
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending).rejects.toThrow('transcript-timeout');
  });

  it('服务端错误原样抛出（不伪装成超时）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: vi.fn().mockResolvedValue({ code: 7, msg: 'session not found', data: null }),
    }));

    await expect(loadAgentTranscript('ai_x')).rejects.toThrow('session not found');
  });

  it('getAgentSessionDetail 透传 before/signal，空参数不拼进 query', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse({}));
    vi.stubGlobal('fetch', fetchMock);
    const controller = new AbortController();

    await getAgentSessionDetail('ai_2', { before: 'm-10', signal: controller.signal });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agent/session?id=ai_2&before=m-10');
    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal);
  });
});
