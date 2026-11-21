// 网页聊天审计四批（拍板批 2026-11-21）：S3 图片白名单限同源。
// 拍板口径：img src 仅同源（相对路径/同 host 绝对 URL）+ data:image 放行；
// 远程图降级为可点击链接文本（零自动外联，追踪信标/URL 外传通道关闭），
// 本机附件图（同源端点 /api/agent/ai/attachment）照常显示。
import { describe, it, expect } from 'vitest';
import { renderMarkdownSafe } from './chatMarkdown';

describe('S3 图片白名单限同源（拍板 2026-11-21）', () => {
  it('同源相对路径附件图照常渲染 <img>', () => {
    const html = renderMarkdownSafe('![本机图](/api/agent/ai/attachment?ref=s%2Fm.png)');
    expect(html).toContain('<img');
    expect(html).toContain('/api/agent/ai/attachment');
  });

  it('data:image 放行', () => {
    const html = renderMarkdownSafe('![内联](data:image/png;base64,iVBORw0KGgo=)');
    expect(html).toContain('<img');
  });

  it('同 origin 绝对 URL 放行（按 jsdom location 动态构造）', () => {
    const abs = `${window.location.origin}/api/agent/ai/attachment?ref=a.png`;
    const html = renderMarkdownSafe(`![同源](${abs})`);
    expect(html).toContain('<img');
    expect(html).toContain(abs);
  });

  it('不同 port 的绝对 URL（跨 origin）降级', () => {
    const html = renderMarkdownSafe('![他端口](http://localhost:56433/api/agent/ai/attachment?ref=a.png)');
    expect(html).not.toContain('<img');
    expect(html).toContain('noopener');
  });

  it('远程图降级为链接文本：无 <img>、无自动外联、保留可点击 href 与 alt 文本', () => {
    const html = renderMarkdownSafe('![追踪像素](https://evil.example/pixel?u=victim)');
    expect(html).not.toContain('<img');
    expect(html).toContain('href="https://evil.example/pixel?u=victim"');
    expect(html).toContain('追踪像素');
    // 降级后的链接同样过 target/rel 白名单
    expect(html).toContain('noopener');
  });

  it('javascript: src 被 markdown-it validateLink 拦为纯文本（不产 img/注入 href）', () => {
    const html = renderMarkdownSafe('![x](javascript:alert(1))');
    expect(html).not.toContain('<img');
    expect(html).not.toContain('href="javascript'); // 注入面在 href，字面文本无害
  });

  it('纯文本不受影响（回归）', () => {
    expect(renderMarkdownSafe('**bold**')).toContain('<strong>');
  });
});
