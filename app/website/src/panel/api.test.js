// api.test.js — api 层三面专测:
// A. postMessageHost 双宿主/无宿主(Task 7 计划加粗项:两后端皆缺 → 返回
//    false=无宿主模式判据,必须有专测);
// B. isHosted() 宿主探测(I-1:theme.css hosted 门控的数据源,M2 原生壳
//    drawsBackground=false 时页面底色必须让位给系统材质);
// C. 动作链路(I-2:getSummary 信封/启停业务失败容忍白名单/restart 顺序与
//    400ms 间隔/超时转译——评审收口批补的回归网)。
// 宿主对象用 vi.stubGlobal 打桩(jsdom 下 globalThis 即 window),fetch 用
// stubGlobal + fake timers,测完恢复。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  activateEndpoint,
  ENDPOINT_ACTIVATE_TIMEOUT_MS,
  getSummary,
  isHosted,
  postMessageHost,
  PROXY_ACTION_TIMEOUT_MS,
  restartProxy,
  setMode,
  startProxy,
  stopProxy,
  SUMMARY_TIMEOUT_MS,
} from './api';

afterEach(() => {
  vi.unstubAllGlobals();
});

// ---- A. postMessageHost ----------------------------------------------------

describe('postMessageHost', () => {
  it('① 无宿主环境:两后端皆缺 → 返回 false 且不抛错', () => {
    // jsdom 默认就没有 chrome/webkit,不 stub 即无宿主;双保险再显式删一遍。
    vi.stubGlobal('chrome', undefined);
    vi.stubGlobal('webkit', undefined);
    expect(() => postMessageHost({ type: 'panel_ready' })).not.toThrow();
    expect(postMessageHost({ type: 'panel_ready' })).toBe(false);
  });

  it('② WebView2 宿主:消息送达 chrome.webview.postMessage 且返回 true', () => {
    const spy = vi.fn();
    vi.stubGlobal('chrome', { webview: { postMessage: spy } });
    const msg = { type: 'panel_ready' };
    const out = postMessageHost(msg);
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy).toHaveBeenCalledWith(msg);
    expect(out).toBe(true);
  });

  it('③ WKWebView 宿主:消息送达 webkit.messageHandlers.panel.postMessage 且返回 true', () => {
    const spy = vi.fn();
    vi.stubGlobal('webkit', { messageHandlers: { panel: { postMessage: spy } } });
    const msg = { type: 'open_dashboard' };
    const out = postMessageHost(msg);
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy).toHaveBeenCalledWith(msg);
    expect(out).toBe(true);
  });

  it('WebView2 优先:两宿主同存时只走 chrome 分支,不重复投递', () => {
    const chromeSpy = vi.fn();
    const webkitSpy = vi.fn();
    vi.stubGlobal('chrome', { webview: { postMessage: chromeSpy } });
    vi.stubGlobal('webkit', { messageHandlers: { panel: { postMessage: webkitSpy } } });
    expect(postMessageHost({ type: 'x' })).toBe(true);
    expect(chromeSpy).toHaveBeenCalledTimes(1);
    expect(webkitSpy).not.toHaveBeenCalled();
  });

  it('chrome 存在但 webview 缺失,且无 WKWebView → false', () => {
    vi.stubGlobal('chrome', {});
    vi.stubGlobal('webkit', undefined);
    expect(postMessageHost({ type: 'x' })).toBe(false);
  });

  it('postMessage 非函数(如被宿主改成数字)→ 视同该后端缺失 → false', () => {
    vi.stubGlobal('chrome', { webview: { postMessage: 42 } });
    vi.stubGlobal('webkit', undefined);
    expect(postMessageHost({ type: 'x' })).toBe(false);
  });
});

// ---- B. isHosted -----------------------------------------------------------

describe('isHosted', () => {
  it('无宿主(chrome/webkit 皆缺)→ false', () => {
    vi.stubGlobal('chrome', undefined);
    vi.stubGlobal('webkit', undefined);
    expect(isHosted()).toBe(false);
  });

  it('WebView2(chrome.webview)→ true', () => {
    vi.stubGlobal('chrome', { webview: {} });
    vi.stubGlobal('webkit', undefined);
    expect(isHosted()).toBe(true);
  });

  it('WKWebView(webkit.messageHandlers.panel)→ true', () => {
    vi.stubGlobal('chrome', undefined);
    vi.stubGlobal('webkit', { messageHandlers: { panel: {} } });
    expect(isHosted()).toBe(true);
  });
});

// ---- C. 动作链路 ------------------------------------------------------------

// stubFetch 打桩 global fetch 并返回 mock 便于断言调用序列。
function stubFetch(impl) {
  const fetchMock = vi.fn(impl);
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

// flushQueue:把 mock fetch 的 promise 链冲到落定(fake timers 下用
// advanceTimersByTimeAsync 冲微任务,不混真实定时器)。
async function flushQueue() {
  await vi.advanceTimersByTimeAsync(0);
}

describe('动作链路(信封/启停/超时)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('① getSummary 信封 code≠0 → 抛错且带服务端 msg', async () => {
    stubFetch(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ code: 401001, msg: 'unauthorized' }) })
    );
    await expect(getSummary()).rejects.toThrow('unauthorized');
  });

  it('② start 业务失败(status=failed)→ 抛错且错误信息带服务端 msg', async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            code: 0,
            data: { status: 'failed', error: 'activation_required', msg: '请先完成登录或配置恢复' },
          }),
      })
    );
    await expect(startProxy()).rejects.toThrow('请先完成登录或配置恢复');
  });

  it('③ stop 撞 not_running → 容忍不抛(白名单,与托盘同口径)', async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            code: 0,
            data: { status: 'failed', error: 'not_running', msg: 'No service is currently running' },
          }),
      })
    );
    await expect(stopProxy()).resolves.toBeDefined();
    expect(fetchMock.mock.calls[0][0]).toBe('/api/run/stop');
  });

  it('④ stop 返回白名单外的 failed error → 照抛', async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({ code: 0, data: { status: 'failed', error: 'boom', msg: '服务端炸了' } }),
      })
    );
    await expect(stopProxy()).rejects.toThrow('服务端炸了');
  });

  it('⑤ restartProxy 顺序与间隔:先 POST stop,400ms 内不 start,满 400ms 才 start', async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ code: 0, data: { status: 'success' } }) })
    );
    const done = restartProxy();
    await flushQueue();
    // stop 已发出,start 还没有
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(['/api/run/stop']);
    vi.advanceTimersByTime(399);
    await flushQueue();
    // 399ms:间隔未满,start 仍未发出
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(['/api/run/stop']);
    vi.advanceTimersByTime(1); // 满 400ms
    await flushQueue();
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(['/api/run/stop', '/api/run/start']);
    await done;
  });

  it('⑥ fetch 超时 → 转译为带毫秒数的超时错误', async () => {
    // mock fetch 不自动响应 abort,手动监听 signal 模拟真实 fetch 行为。
    stubFetch(
      (_path, opts) =>
        new Promise((_resolve, reject) => {
          opts.signal.addEventListener('abort', () => {
            const err = new Error('The operation was aborted');
            err.name = 'AbortError';
            reject(err);
          });
        })
    );
    const pending = getSummary();
    // 拒绝断言先于推进时钟挂上:abort 在 advanceTimersByTimeAsync 期间落地,
    // 若此刻 pending 还没有任何 handler,Node 会记 unhandledRejection
    // (vitest 汇总报 Unhandled Errors)——先挂断言再触发即可消除。
    const assertion = expect(pending).rejects.toThrow(`超时(${SUMMARY_TIMEOUT_MS}ms)`);
    await vi.advanceTimersByTimeAsync(SUMMARY_TIMEOUT_MS);
    await assertion;
  });

  it('restart 中途 stop 失败(非白名单)→ 整条 restart 抛错且不发 start', async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ code: 0, data: { status: 'failed', error: 'boom', msg: 'stop 炸了' } }),
      })
    );
    const done = restartProxy();
    // 同⑥:拒绝断言先于冲队列挂上,消除 rejection 落地时无 handler 的窗口。
    const assertion = expect(done).rejects.toThrow('stop 炸了');
    await flushQueue();
    await assertion;
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(['/api/run/stop']);
  });

  it('⑦ setMode POST /api/run/swift,body {"mode":...},成功透传 data', async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ code: 0, data: { status: 'switched', current_mode: 'tun' } }),
      })
    );
    const out = await setMode('tun');
    expect(out).toEqual({ status: 'switched', current_mode: 'tun' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/run/swift');
    expect(fetchMock.mock.calls[0][1].method).toBe('POST');
    expect(fetchMock.mock.calls[0][1].body).toBe(JSON.stringify({ mode: 'tun' }));
    // 超时走启停同一条 proxyAction(10s 档,fetchJson 内部消费 timeoutMs,
    // 不出现在 fetch 入参;超时转译机制本身由⑥用例覆盖)——这里断言
    // abort 信号已挂,证明确有超时闸。
    expect(fetchMock.mock.calls[0][1].signal).toBeInstanceOf(AbortSignal);
    expect(PROXY_ACTION_TIMEOUT_MS).toBe(10000); // 与启停同档的静态锚
  });

  it('⑧ setMode 非法 mode(status=failed)→ 抛错且带服务端 msg', async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            code: 0,
            data: { status: 'failed', error: 'invalid_mode', msg: "Invalid target mode: bad. Must be 'http' or 'tun'" },
          }),
      })
    );
    await expect(setMode('bad')).rejects.toThrow('Invalid target mode');
  });

  it('⑨ setMode 同值幂等(status=unchanged)→ 视为成功不抛', async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ code: 0, data: { status: 'unchanged', current_mode: 'http' } }),
      })
    );
    await expect(setMode('http')).resolves.toEqual({ status: 'unchanged', current_mode: 'http' });
  });

  it('⑩ 非 2xx 默认不透传信封 msg(errorBodyMsg 默认关不变式):getSummary/startProxy/setMode 恒报 HTTP 状态码', async () => {
    // 信封 body 有人话 msg,但默认关路径必须连 res.json() 都不碰,统一回落
    // 「HTTP xxx」——防止 errorBodyMsg 默认值被误翻成 true 改变既有三路文案。
    const body = () => Promise.resolve({ code: 401001, msg: '登录已过期，请重新登录' });
    const json1 = vi.fn(body);
    stubFetch(() => Promise.resolve({ ok: false, status: 401, json: json1 }));
    await expect(getSummary()).rejects.toThrow('GET /api/tray/summary 失败: HTTP 401');
    expect(json1).not.toHaveBeenCalled();

    const json2 = vi.fn(body);
    stubFetch(() => Promise.resolve({ ok: false, status: 403, json: json2 }));
    await expect(startProxy()).rejects.toThrow('POST /api/run/start 失败: HTTP 403');
    expect(json2).not.toHaveBeenCalled();

    const json3 = vi.fn(body);
    stubFetch(() => Promise.resolve({ ok: false, status: 503, json: json3 }));
    await expect(setMode('tun')).rejects.toThrow('POST /api/run/swift 失败: HTTP 503');
    expect(json3).not.toHaveBeenCalled();
  });
});

// ---- D. activateEndpoint(面板 radio 真激活,plan 2026-11-27 Task 3)--------
// 本段直测【真实 api.js】(文件顶部无 mock):EndpointZone.test.js 的 vi.mock
// 工厂只拦组件行为链路,对未来「改坏真实请求体/错误透传」零保护力——真
// 契约(路径/body/三层判失败/超时档)必须打在真模块上。

describe('activateEndpoint(真实模块:路径/body/三层判失败/超时档)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('① POST /api/endpoints/activate,body {protocol,id},成功透传 data', async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ code: 0, msg: 'success', data: { protocol: 'anthropic', id: 'ep-1' } }),
      })
    );
    const out = await activateEndpoint('anthropic', 'ep-1');
    expect(out).toEqual({ protocol: 'anthropic', id: 'ep-1' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/endpoints/activate');
    expect(fetchMock.mock.calls[0][1].method).toBe('POST');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ protocol: 'anthropic', id: 'ep-1' });
    expect(fetchMock.mock.calls[0][1].headers['Content-Type']).toBe('application/json');
    // 超时闸确有挂上(时长由⑤用例钉死)
    expect(fetchMock.mock.calls[0][1].signal).toBeInstanceOf(AbortSignal);
  });

  it('② HTTP 错误层:非 2xx 的信封 body msg 透传(common.Error* 恒带人话,如摘除行 400)', async () => {
    stubFetch(() =>
      Promise.resolve({
        ok: false,
        status: 400,
        json: () => Promise.resolve({ code: 400, msg: '已摘除的行请先在仪表盘恢复' }),
      })
    );
    await expect(activateEndpoint('anthropic', 'ep-1')).rejects.toThrow('已摘除的行请先在仪表盘恢复');
  });

  it('③ HTTP 错误但 body 非信封(网关 HTML 错误页)→ 回落 HTTP 状态码文案', async () => {
    stubFetch(() => Promise.resolve({ ok: false, status: 502, json: () => Promise.resolve('Bad Gateway') }));
    await expect(activateEndpoint('anthropic', 'ep-1')).rejects.toThrow('HTTP 502');
  });

  it('④ 业务码层:200 信封 code≠0 → 抛错带服务端 msg(防御性,惯例对齐 getSummary)', async () => {
    stubFetch(() =>
      Promise.resolve({ ok: true, json: () => Promise.resolve({ code: 201, msg: 'endpoint registry 不可用' }) })
    );
    await expect(activateEndpoint('anthropic', 'ep-1')).rejects.toThrow('endpoint registry 不可用');
  });

  it('⑤ 传输层超时档 = 启停同款 10s(静态锚 + 超时转译)', async () => {
    expect(ENDPOINT_ACTIVATE_TIMEOUT_MS).toBe(10000);
    expect(ENDPOINT_ACTIVATE_TIMEOUT_MS).toBe(PROXY_ACTION_TIMEOUT_MS);
    stubFetch(
      (_path, opts) =>
        new Promise((_resolve, reject) => {
          opts.signal.addEventListener('abort', () => {
            const err = new Error('The operation was aborted');
            err.name = 'AbortError';
            reject(err);
          });
        })
    );
    const pending = activateEndpoint('anthropic', 'ep-1');
    // 拒绝断言先于推进时钟挂上(同⑥:消除 rejection 无 handler 窗口)
    const assertion = expect(pending).rejects.toThrow(`超时(${ENDPOINT_ACTIVATE_TIMEOUT_MS}ms)`);
    await vi.advanceTimersByTimeAsync(ENDPOINT_ACTIVATE_TIMEOUT_MS);
    await assertion;
  });

  it('⑥ 信封解析自身失败(res.json() reject)→ 回落 HTTP 状态码文案(readErrorEnvelopeMsg catch 分支)', async () => {
    // 非 JSON 错误页(如网关 502 HTML)会让 res.json() 直接 reject;错误文案
    // 必须回落状态码,而不是把解析异常漏给用户。
    stubFetch(() =>
      Promise.resolve({
        ok: false,
        status: 500,
        json: () => Promise.reject(new SyntaxError('Unexpected token < in JSON')),
      })
    );
    await expect(activateEndpoint('anthropic', 'ep-1')).rejects.toThrow('HTTP 500');
  });
});
