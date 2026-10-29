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
  it('успех: возвращает данные сессии (transcript в payload)', async () => {
    const detail = { id: 'ai_1', transcript: [{ role: 'user', content: 'hi' }] };
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(detail));
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadAgentTranscript('ai_1', { limit: 50 })).resolves.toBe(detail);
    const [path, options] = fetchMock.mock.calls[0];
    expect(path).toBe('/api/agent/session?id=ai_1&limit=50');
    expect(options.method).toBe('GET');
  });

  it('холодный путь: 40s таймаут → ошибка-маркер transcript-timeout (кнопка «Повторить» = повторный вызов)', async () => {
    vi.stubGlobal('fetch', abortableFetch());
    vi.useFakeTimers();

    const pending = loadAgentTranscript('ai_slow', { timeoutMs: AGENT_TRANSCRIPT_TIMEOUT_MS });
    pending.catch(() => {}); // без «необработанного» отклонения на время прокрутки таймеров
    await vi.advanceTimersByTimeAsync(AGENT_TRANSCRIPT_TIMEOUT_MS - 1);
    await expect(Promise.race([pending.then(() => 'resolved'), Promise.resolve('pending')])).resolves.toBe('pending');
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending).rejects.toThrow('transcript-timeout');
  });

  it('серверная ошибка пробрасывается как есть (не маскируется под таймаут)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 500,
      json: vi.fn().mockResolvedValue({ code: 7, msg: 'session not found', data: null }),
    }));

    await expect(loadAgentTranscript('ai_x')).rejects.toThrow('session not found');
  });

  it('getAgentSessionDetail прокидывает before/signal и опускает пустые параметры', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse({}));
    vi.stubGlobal('fetch', fetchMock);
    const controller = new AbortController();

    await getAgentSessionDetail('ai_2', { before: 'm-10', signal: controller.signal });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agent/session?id=ai_2&before=m-10');
    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal);
  });
});
