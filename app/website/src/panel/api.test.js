// api.test.js — postMessageHost 双宿主/无宿主专测(Task 7 计划加粗项:
// 「两后端皆缺 → 返回 false(无宿主模式判据)」必须有专测)。
//
// 三个分支:
//   ① 无宿主(jsdom 默认 window.chrome/window.webkit 皆缺)→ false 且不抛错
//      ——M1 浏览器直开 panel.html 的无宿主模式靠这个返回值判定;
//   ② WebView2(Windows):window.chrome.webview.postMessage;
//   ③ WKWebView(macOS):window.webkit.messageHandlers.panel.postMessage。
// 宿主对象用 vi.stubGlobal 打桩(jsdom 下 globalThis 即 window),测完恢复。
import { afterEach, describe, expect, it, vi } from 'vitest';
import { postMessageHost } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
});

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
