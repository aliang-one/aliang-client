// 文件预览(P3.5)轻组件测试:createApp + h 挂载(仓内手法,同 PinGuard.test.js)。
// mock services/agentApi 的 fsRead/fsDownload;highlight.js 用真实注册(组件自持
// 注册与 chatMarkdown 同一 core 单例)——文本用例断言真实 hljs 标记。jsdom 无
// URL.createObjectURL/revokeObjectURL:下载用例打桩 + spy 锚点 click(防 jsdom
// 导航噪声)。文案期望经 t() 动态取,断言两侧同源。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick, ref } from 'vue';
import FilePreview from './FilePreview.vue';
import { useI18n } from '../../i18n';

const mockFsRead = vi.fn();
const mockFsDownload = vi.fn();
vi.mock('../../services/agentApi', () => ({
  fsRead: (...a) => mockFsRead(...a),
  fsDownload: (...a) => mockFsDownload(...a),
}));

const { t } = useI18n();

async function flushMicrotasks() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
  await nextTick();
}

// entryRef 包成响应源:切换条目场景(race 用例)需运行中改 prop
async function mountEntry(entry) {
  const entryRef = ref(entry);
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(FilePreview, { entry: entryRef.value, token: 'tk' }),
  });
  app.mount(el);
  await flushMicrotasks();
  const q = (sel) => el.querySelector(sel);
  return { entryRef, el, app, q };
}

beforeEach(() => {
  vi.clearAllMocks();
  // jsdom 未实现 objectURL 两件套:常备打桩(下载用例显式断言调用)
  URL.createObjectURL = vi.fn(() => 'blob:test');
  URL.revokeObjectURL = vi.fn();
});

afterEach(() => {
  document.body.innerHTML = '';
  delete URL.createObjectURL;
  delete URL.revokeObjectURL;
});

describe('FilePreview', () => {
  it('entry=null→空态提示,不发 fsRead', async () => {
    const m = await mountEntry(null);
    expect(m.q('[data-testid="file-preview-hint"]').textContent).toBe(t('fs_empty_hint'));
    expect(mockFsRead).not.toHaveBeenCalled();
  });

  it('entry=dir→目录提示,不发 fsRead', async () => {
    const m = await mountEntry({ path: '/Users/mac/docs', type: 'dir', name: 'docs' });
    expect(m.q('[data-testid="file-preview-dir-hint"]').textContent).toBe(t('fs_dir_hint'));
    expect(mockFsRead).not.toHaveBeenCalled();
  });

  it('文本+已注册扩展(.js)→fsRead 后 highlight.js 高亮渲染', async () => {
    mockFsRead.mockResolvedValue({ path: '/Users/mac/a.js', content: 'const x = 1;', truncated: false, binary: false });
    const m = await mountEntry({ path: '/Users/mac/a.js', type: 'file', name: 'a.js' });
    expect(mockFsRead).toHaveBeenCalledWith('/Users/mac/a.js', 'tk');
    const code = m.q('.file-preview-code code');
    expect(code).toBeTruthy();
    // 真实 hljs:const 渲染为关键字 span(HTML 转义由 hljs 保证)
    expect(code.innerHTML).toContain('hljs-keyword');
    expect(code.textContent).toContain('const x = 1;');
    // 无二进制/截断徽章;下载钮在
    expect(m.q('[data-testid="file-preview-badge-binary"]')).toBe(null);
    expect(m.q('[data-testid="file-preview-badge-truncated"]')).toBe(null);
    expect(m.q('[data-testid="file-preview-download"]')).toBeTruthy();
  });

  it('未知扩展(.txt)→纯 <pre> 文本,不高亮', async () => {
    mockFsRead.mockResolvedValue({ path: '/Users/mac/notes.txt', content: 'plain hello', truncated: false, binary: false });
    const m = await mountEntry({ path: '/Users/mac/notes.txt', type: 'file', name: 'notes.txt' });
    expect(m.q('.file-preview-code')).toBe(null); // 无高亮码块
    const plain = m.q('[data-testid="file-preview-plain"]');
    expect(plain.textContent).toBe('plain hello');
  });

  it('二进制→仅徽章+下载,不渲染正文内容区', async () => {
    mockFsRead.mockResolvedValue({ path: '/Users/mac/logo.png', content: '', truncated: false, binary: true });
    const m = await mountEntry({ path: '/Users/mac/logo.png', type: 'file', name: 'logo.png' });
    expect(m.q('[data-testid="file-preview-badge-binary"]').textContent).toBe(t('fs_binary'));
    expect(m.q('[data-testid="file-preview-content"]')).toBe(null); // 正文区不渲染
    expect(m.q('[data-testid="file-preview-download"]')).toBeTruthy();
  });

  it('截断→截断徽章(文本仍渲染)', async () => {
    mockFsRead.mockResolvedValue({ path: '/Users/mac/big.log', content: 'x'.repeat(10), truncated: true, binary: false });
    const m = await mountEntry({ path: '/Users/mac/big.log', type: 'file', name: 'big.log' });
    expect(m.q('[data-testid="file-preview-badge-truncated"]').textContent).toBe(t('fs_truncated'));
    expect(m.q('[data-testid="file-preview-plain"]').textContent).toContain('xxxxxxxxxx');
  });

  it('下载:fsDownload→Blob 经 createObjectURL 消费→a[download] 点击→回收', async () => {
    const blob = new Blob(['abc']);
    mockFsRead.mockResolvedValue({ path: '/Users/mac/a.js', content: 'x', truncated: false, binary: false });
    mockFsDownload.mockResolvedValue(blob);
    const anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const m = await mountEntry({ path: '/Users/mac/a.js', type: 'file', name: 'a.js' });
    m.q('[data-testid="file-preview-download"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDownload).toHaveBeenCalledWith('/Users/mac/a.js', 'tk');
    expect(URL.createObjectURL).toHaveBeenCalledWith(blob); // Blob 被消费
    expect(anchorClick).toHaveBeenCalledTimes(1); // a[download] 确被点击
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:test'); // URL 回收
    anchorClick.mockRestore();
  });

  it('读取失败→错误行;重试再走 fsRead,成功渲染内容', async () => {
    mockFsRead.mockRejectedValueOnce(new Error('file too large'));
    const m = await mountEntry({ path: '/Users/mac/a.js', type: 'file', name: 'a.js' });
    const err = m.q('[data-testid="file-preview-error"]');
    expect(err.textContent).toContain(t('fs_load_error'));
    expect(err.textContent).toContain('file too large');
    expect(m.q('[data-testid="file-preview-download"]')).toBe(null); // 失败态无头部
    // 重试:成功回包渲染高亮码块,错误行消失
    mockFsRead.mockResolvedValue({ path: '/Users/mac/a.js', content: 'let y = 2;', truncated: false, binary: false });
    m.q('[data-testid="file-preview-retry"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsRead).toHaveBeenCalledTimes(2);
    expect(m.q('.file-preview-code code')).toBeTruthy();
    expect(m.q('[data-testid="file-preview-error"]')).toBe(null);
  });

  it('竞态防护:切换条目后,慢的旧回包不覆盖新选中内容', async () => {
    // 旧条目 fsRead 永不 resolve(挂起),新条目正常回包
    mockFsRead.mockImplementationOnce(() => new Promise(() => {}));
    mockFsRead.mockResolvedValueOnce({ path: '/Users/mac/b.go', content: 'package main', truncated: false, binary: false });
    const m = await mountEntry({ path: '/Users/mac/a.js', type: 'file', name: 'a.js' });
    await flushMicrotasks();
    m.entryRef.value = { path: '/Users/mac/b.go', type: 'file', name: 'b.go' };
    await flushMicrotasks();
    expect(mockFsRead).toHaveBeenCalledTimes(2);
    // 渲染的是新条目内容(且 loading 已随新回包落定)
    expect(m.q('.file-preview-code code')?.textContent).toContain('package main');
    expect(m.q('[data-testid="file-preview-loading"]')).toBe(null);
    expect(m.q('[data-testid="file-preview-error"]')).toBe(null);
  });
});
