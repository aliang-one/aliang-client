// 文件树(P3.4)轻组件测试:createApp + h 挂载(仓内手法,同 PinGuard.test.js)。
// 拍板(任务契约):FileTree 以 store 为 prop——不 mock useFileBrowser,直接
// 构造真实 createFileBrowserStore + mock services/agentApi = 行为测法:断言落在
// 「树渲染了 store 里什么」与「点击走了 store 哪条公开路」(fsList 调用序列)。
// 文案期望经 t() 动态取,断言两侧同源(fs_* 已入词表,先后皆成立)。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick, ref } from 'vue';
import FileTree from './FileTree.vue';
import { createFileBrowserStore } from '../../composables/useFileBrowser';
import { useI18n } from '../../i18n';

const mockFsList = vi.fn();
vi.mock('../../services/agentApi', () => ({
  fsList: (...a) => mockFsList(...a),
}));

const { t } = useI18n();

const ROOT = '/Users/mac';
const rootEntries = [
  { name: 'Documents', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' },
  { name: 'notes.txt', type: 'file', size: 12, mtime: 2, mode: '-rw-r--r--' },
  { name: 'link', type: 'symlink', size: 5, mtime: 3, mode: 'lrwxr-xr-x' },
];
const childEntries = [
  { name: 'a.js', type: 'file', size: 40, mtime: 4, mode: '-rw-r--r--' },
  { name: 'project', type: 'dir', size: 0, mtime: 5, mode: 'drwxr-xr-x' },
];
const CHILD = `${ROOT}/Documents`;

// 缺省实现:''/ROOT → 根列表(首拉收养规范根);CHILD → 子目录列表
function defaultList(path) {
  if (path === '' || path === ROOT) return { path: ROOT, entries: rootEntries };
  if (path === CHILD) return { path: CHILD, entries: childEntries };
  return { path, entries: [] };
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
  await nextTick();
}

async function mount() {
  const store = createFileBrowserStore(ref('tk'));
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(FileTree, { store }) });
  app.mount(el);
  await flushMicrotasks();
  const q = (sel) => el.querySelector(sel);
  return { store, el, app, q };
}

function click(el, testid) {
  el.querySelector(`[data-testid="${testid}"]`)?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mockFsList.mockImplementation((path) => defaultList(path));
});

afterEach(() => {
  document.body.innerHTML = '';
});

describe('FileTree', () => {
  it('挂载即根引导:setRoot(空 path)→根级条目按 children[root] 渲染', async () => {
    const m = await mount();
    // 引导契约:空 path 首拉,回包 path=规范根
    expect(mockFsList).toHaveBeenCalledTimes(1);
    expect(mockFsList).toHaveBeenCalledWith('', 'tk');
    expect(m.store.root.value).toBe(ROOT);
    // 根级三条目各成行(data-path=规范根拼名)
    expect(m.q(`[data-testid="file-tree-node"][data-path="${ROOT}/Documents"]`)).toBeTruthy();
    expect(m.q(`[data-testid="file-tree-node"][data-path="${ROOT}/notes.txt"]`)).toBeTruthy();
    expect(m.q(`[data-path="${ROOT}/link"]`)?.getAttribute('data-type')).toBe('symlink');
    expect(m.q('[data-testid="file-tree-error"]')).toBe(null); // 无错误行
  });

  it('展开目录:行点击→toggleNode 懒拉,子级递归渲染(depth+1 缩进)', async () => {
    const m = await mount();
    // 行本体在节点包裹层内:按节点 data-path + 行 testid 定位
    const row = m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`);
    expect(row).toBeTruthy();
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 懒加载:按完整子路径拉取
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk');
    expect(m.store.expanded.value.has(CHILD)).toBe(true);
    // 子级递归渲染(文件+目录都有),且嵌套在子容器里
    const children = m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-children"]`);
    expect(children).toBeTruthy();
    expect(children.querySelector(`[data-path="${CHILD}/a.js"]`)).toBeTruthy();
    expect(children.querySelector(`[data-path="${CHILD}/project"]`)).toBeTruthy();
  });

  it('懒拉失败→错误行;重试走公开 listDir(而非 toggleNode)且成功即展开', async () => {
    const m = await mount();
    mockFsList.mockRejectedValueOnce(new Error('Path escapes the file browser root'));
    const row = m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`);
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 失败:errors 有文案、不进 expanded、无子级
    expect(m.store.errors.value[CHILD]).toBe('Path escapes the file browser root');
    expect(m.store.expanded.value.has(CHILD)).toBe(false);
    const node = m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"]`);
    expect(node.querySelector('[data-testid="file-tree-error"]')?.textContent).toContain(t('fs_load_error'));
    // 重试:点击错误行按钮(实现=store.listDir,观测为 fsList 按同 path 再拉)
    expect(mockFsList).toHaveBeenCalledTimes(2); // 引导 1 + 失败 1
    node.querySelector('[data-testid="file-tree-retry"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenCalledTimes(3);
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk');
    // 成功:错误行消失、目录进 expanded、子级渲染
    expect(m.store.errors.value[CHILD]).toBeUndefined();
    expect(m.store.expanded.value.has(CHILD)).toBe(true);
    expect(node.querySelector('[data-testid="file-tree-children"]')).toBeTruthy();
    expect(node.querySelector(`[data-path="${CHILD}/a.js"]`)).toBeTruthy();
  });

  it('文件行点击→selectNode({path,type,name})', async () => {
    const m = await mount();
    const row = m.q(`[data-testid="file-tree-node"][data-path="${ROOT}/notes.txt"] [data-testid="file-tree-row-file"]`);
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.store.selected.value).toEqual({
      path: `${ROOT}/notes.txt`,
      type: 'file',
      name: 'notes.txt',
    });
    // 选中行高亮类在行上(bg-primary/10)
    expect(row.className).toContain('bg-primary/10');
  });

  it('根引导失败→根级错误行;重试走 setRoot 收养规范根并铺条目', async () => {
    mockFsList.mockReset();
    mockFsList.mockRejectedValueOnce(new Error('bad token'));
    mockFsList.mockImplementation((path) => defaultList(path));
    const m = await mount();
    expect(mockFsList).toHaveBeenCalledTimes(1);
    const err = m.q('[data-testid="file-tree-error"]');
    expect(err.textContent).toContain(t('fs_load_error'));
    expect(err.textContent).toContain('bad token');
    expect(m.q('[data-path^="/Users"]')).toBe(null); // 未收养,无条目
    // 重试:setRoot('') 全语义(重拉+收养+根进 expanded)
    click(m.el, 'file-tree-retry');
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(mockFsList).toHaveBeenLastCalledWith('', 'tk');
    expect(m.store.root.value).toBe(ROOT);
    expect(m.q(`[data-path="${ROOT}/notes.txt"]`)).toBeTruthy();
    expect(m.q('[data-testid="file-tree-error"]')).toBe(null);
  });
});
