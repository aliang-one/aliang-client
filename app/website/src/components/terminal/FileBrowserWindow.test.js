// 文件浏览窗(P3.6)轻组件测试:createApp + h 挂载(仓内手法,同 PinGuard.test.js)。
// mock services/agentApi 四端点(fsList 供树根引导/refreshNode,fsUpload 供上传,
// fsRead 供上传后选中新文件的预览触发);FloatingWindow/FileTree/FilePreview 用
// 真身(壳交互/树结构/预览分支各有独立测试,此处只钉上传闭环与目标目录语义)。
// 文案期望经 t() 动态取,断言两侧同源(fs_uploading/fs_upload_error 随 P3.6 入词表)。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import FileBrowserWindow from './FileBrowserWindow.vue';
import { useI18n } from '../../i18n';

const mockFsList = vi.fn();
const mockFsUpload = vi.fn();
const mockFsRead = vi.fn();
vi.mock('../../services/agentApi', () => ({
  fsList: (...a) => mockFsList(...a),
  fsUpload: (...a) => mockFsUpload(...a),
  fsRead: (...a) => mockFsRead(...a),
}));

const { t } = useI18n();

const ROOT = '/Users/mac';
const rootEntries = [
  { name: 'Documents', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' },
  { name: 'notes.txt', type: 'file', size: 12, mtime: 2, mode: '-rw-r--r--' },
];
const CHILD = `${ROOT}/Documents`;

function defaultList(path) {
  if (path === '' || path === ROOT) return { path: ROOT, entries: rootEntries };
  return { path, entries: [] };
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
  await nextTick();
}

let app = null;

async function mountWindow() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  app = createApp({
    render: () => h(FileBrowserWindow, { token: 'tk', x: 10, y: 20, zIndex: 5, active: true, minimized: false }),
  });
  app.mount(el);
  await flushMicrotasks();
  const q = (sel) => el.querySelector(sel);
  return { el, q };
}

// hidden file input 喂档:jsdom 的 input.files 只读,Object.defineProperty 注入
// 数组形态(组件只读 files[0],与 FileList 等价)
function feedFile(input, file) {
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new Event('change'));
}

function makeFile(name, content = 'data') {
  return new File([content], name, { type: 'text/plain' });
}

function click(el, testid) {
  el.querySelector(`[data-testid="${testid}"]`)?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mockFsList.mockImplementation((path) => defaultList(path));
  mockFsRead.mockResolvedValue({ path: '/x', content: 'x', truncated: false, binary: false });
  // 缺省上传成功:回包终名=dir/原名(同名冲突的时间戳后缀由 Go 侧落,前端只认
  // 回包 path——「终名优先」语义在首个用例以显式 Once 覆盖戳名验证)
  mockFsUpload.mockImplementation(async (dir, file) => ({ path: `${dir}/${file?.name || 'f'}`, bytes: 4 }));
});

afterEach(() => {
  if (app) {
    app.unmount();
    app = null;
  }
  document.body.innerHTML = '';
});

describe('FileBrowserWindow 上传(P3.6)', () => {
  it('上传成功:目标=守卫根(未选目录)→fsUpload(root,file,token);refreshNode 重拉根;新文件按回包终名选中并触发预览', async () => {
    // 回包终名带冲突时间戳(Go 侧语义),前端选中只认清包 path
    mockFsUpload.mockImplementationOnce(async () => ({ path: `${ROOT}/hello_153045.txt`, bytes: 4 }));
    const m = await mountWindow();
    expect(mockFsList).toHaveBeenCalledTimes(1); // 根引导
    const input = m.q('[data-testid="fs-upload-input"]');
    feedFile(input, makeFile('hello.txt'));
    await flushMicrotasks();
    // 上传目标=根(未选中目录)
    expect(mockFsUpload).toHaveBeenCalledWith(ROOT, expect.any(File), 'tk');
    // refreshNode(ROOT):根在 expanded → fsList 按规范根重拉(引导 1 + 强刷 1)
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
    expect(mockFsList).toHaveBeenCalledTimes(2);
    // 新文件按回包终名选中(同名冲突由 Go 加时间戳,前端只认清包 path)
    expect(mockFsRead).toHaveBeenLastCalledWith(`${ROOT}/hello_153045.txt`, 'tk');
    // 成功后无错误行、上传态退场
    expect(m.q('[data-testid="fs-upload-error"]')).toBe(null);
    expect(m.q('[data-testid="fs-upload-uploading"]')).toBe(null);
  });

  it('选中目录后上传:目标=当前选中目录(目录行点击即选中)→fsUpload(dir,...)+refreshNode(dir);连传粘滞同目录', async () => {
    const m = await mountWindow();
    // 目录行点击:选中(上传目标)+展开(懒拉子级)
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk'); // 展开懒拉
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('doc.pdf'));
    await flushMicrotasks();
    expect(mockFsUpload).toHaveBeenCalledWith(CHILD, expect.any(File), 'tk');
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk'); // refreshNode(CHILD)
    expect(mockFsList).toHaveBeenCalledTimes(3); // 引导 + 展开 + 强刷
    // 连传:首传后选中已移到新文件(非目录),目标粘滞在上传目录不退守卫根
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('doc2.pdf'));
    await flushMicrotasks();
    expect(mockFsUpload).toHaveBeenLastCalledWith(CHILD, expect.any(File), 'tk');
    expect(mockFsList).toHaveBeenCalledTimes(4); // 第二轮 refreshNode(CHILD)
  });

  it('上传失败→错误行呈现(fs_upload_error+服务端 msg);再传成功后错误退场', async () => {
    mockFsUpload.mockRejectedValueOnce(new Error('File exceeds 100 MiB'));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('huge.bin'));
    await flushMicrotasks();
    const err = m.q('[data-testid="fs-upload-error"]');
    expect(err).not.toBe(null);
    expect(err.textContent).toContain(t('fs_upload_error'));
    expect(err.textContent).toContain('File exceeds 100 MiB');
    expect(mockFsList).toHaveBeenCalledTimes(1); // 失败不刷新目录
    // 再传成功:错误行退场
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('ok.txt'));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-error"]')).toBe(null);
    expect(mockFsList).toHaveBeenCalledTimes(2);
  });

  it('uploading 态:进行中钮禁用+spinner 文案;回包后恢复', async () => {
    let resolveUpload;
    mockFsUpload.mockImplementationOnce(() => new Promise((resolve) => { resolveUpload = resolve; }));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('slow.txt'));
    await nextTick();
    await nextTick();
    expect(m.q('[data-testid="fs-upload-uploading"]').textContent).toContain(t('fs_uploading'));
    expect(m.q('[data-testid="fs-upload-button"]').disabled).toBe(true);
    resolveUpload({ path: `${ROOT}/slow.txt`, bytes: 4 });
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-uploading"]')).toBe(null);
    expect(m.q('[data-testid="fs-upload-button"]').disabled).toBe(false);
  });

  it('空令牌拒绝同走错误行(透传服务端 msg),不炸不刷新', async () => {
    mockFsUpload.mockRejectedValueOnce(new Error('Invalid web terminal token'));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('x.txt'));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-error"]').textContent).toContain('Invalid web terminal token');
  });
});

describe('FileBrowserWindow 壳几何透传(P3.6 props 化)', () => {
  it('width/height props 进壳(默认 860×540 契约保持:不传即缺省)', async () => {
    const m = await mountWindow();
    const shell = m.q('[data-testid="floating-window"]');
    expect(shell).toBeTruthy();
    expect(shell.style.width).toBe('860px');
    expect(shell.style.height).toBe('540px');
  });
});
