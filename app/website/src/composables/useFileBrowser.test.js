import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ref } from 'vue';

const mockFsList = vi.fn();
vi.mock('../services/agentApi', () => ({
  fsList: (...a) => mockFsList(...a),
}));

import { createFileBrowserStore } from './useFileBrowser';

const entries = [
  { name: 'Documents', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' },
  { name: 'notes.txt', type: 'file', size: 12, mtime: 2, mode: '-rw-r--r--' },
];

function okList(path) {
  return { path, entries };
}

describe('createFileBrowserStore', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('toggleNode 懒加载:首次展开才拉 fsList 并填充 children', async () => {
    mockFsList.mockResolvedValue(okList('/Users/mac'));
    const store = createFileBrowserStore(ref('tk'));
    await store.toggleNode('/Users/mac');
    expect(mockFsList).toHaveBeenCalledTimes(1);
    expect(mockFsList).toHaveBeenCalledWith('/Users/mac', 'tk');
    expect(store.children.value['/Users/mac']).toEqual(entries);
    expect(store.expanded.value.has('/Users/mac')).toBe(true);
    expect(store.errors.value['/Users/mac']).toBeUndefined();
  });

  it('收起保留缓存:再展开命中缓存零请求', async () => {
    mockFsList.mockResolvedValue(okList('/Users/mac'));
    const store = createFileBrowserStore(ref('tk'));
    await store.toggleNode('/Users/mac');
    expect(mockFsList).toHaveBeenCalledTimes(1);

    await store.toggleNode('/Users/mac'); // 收起
    expect(store.expanded.value.has('/Users/mac')).toBe(false);
    expect(store.children.value['/Users/mac']).toEqual(entries); // 缓存保留

    await store.toggleNode('/Users/mac'); // 再展开=缓存命中
    expect(mockFsList).toHaveBeenCalledTimes(1); // 未重复拉取
    expect(store.expanded.value.has('/Users/mac')).toBe(true);
  });

  it('失败:errors 记录+不进 expanded+不落缓存,再 toggle 重试并清错', async () => {
    mockFsList.mockRejectedValueOnce(new Error('Path escapes the file browser root'));
    mockFsList.mockResolvedValueOnce(okList('/Users/mac'));
    const store = createFileBrowserStore(ref('tk'));

    await store.toggleNode('/Users/mac');
    expect(store.errors.value['/Users/mac']).toBe('Path escapes the file browser root');
    expect(store.expanded.value.has('/Users/mac')).toBe(false);
    expect(store.children.value['/Users/mac']).toBeUndefined();

    await store.toggleNode('/Users/mac'); // 失败无缓存 → 重拉
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(store.expanded.value.has('/Users/mac')).toBe(true);
    expect(store.children.value['/Users/mac']).toEqual(entries);
    expect(store.errors.value['/Users/mac']).toBeUndefined(); // 成功清错
  });

  it('refreshNode:展开中删缓存+重拉;收起态只清缓存不请求', async () => {
    mockFsList.mockResolvedValue(okList('/Users/mac'));
    const store = createFileBrowserStore(ref('tk'));
    await store.toggleNode('/Users/mac');

    const fresh = [{ name: 'newdir', type: 'dir', size: 0, mtime: 3, mode: 'drwxr-xr-x' }];
    mockFsList.mockResolvedValue({ path: '/Users/mac', entries: fresh });
    await store.refreshNode('/Users/mac');
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(store.children.value['/Users/mac']).toEqual(fresh);
    expect(store.expanded.value.has('/Users/mac')).toBe(true); // 仍展开

    await store.toggleNode('/Users/mac'); // 收起
    mockFsList.mockClear();
    await store.refreshNode('/Users/mac'); // 收起态强刷=只清缓存
    expect(mockFsList).not.toHaveBeenCalled();
    expect(store.children.value['/Users/mac']).toBeUndefined();
  });

  it('工厂独立性:两个 store 的 children/expanded/selected 互不串扰', async () => {
    mockFsList.mockResolvedValue(okList('/Users/mac'));
    const a = createFileBrowserStore(ref('t1'));
    const b = createFileBrowserStore(ref('t2'));

    await a.toggleNode('/Users/mac');
    expect(b.children.value['/Users/mac']).toBeUndefined();
    expect(b.expanded.value.has('/Users/mac')).toBe(false);

    await b.toggleNode('/Users/mac'); // b 独立拉自己的
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(mockFsList).toHaveBeenNthCalledWith(1, '/Users/mac', 't1');
    expect(mockFsList).toHaveBeenNthCalledWith(2, '/Users/mac', 't2');

    a.selectNode({ path: '/Users/mac/a.txt', type: 'file', name: 'a.txt' });
    expect(b.selected.value).toBe(null);
  });

  it('selectNode 设置 selected,null 清除', () => {
    const store = createFileBrowserStore(ref('tk'));
    const entry = { path: '/Users/mac/notes.txt', type: 'file', name: 'notes.txt' };
    store.selectNode(entry);
    expect(store.selected.value).toEqual(entry);
    store.selectNode(null);
    expect(store.selected.value).toBe(null);
  });

  it('令牌逐调用读 tokenRef(滑动续期后自动带新值)', async () => {
    mockFsList.mockResolvedValue(okList('/Users/mac'));
    const token = ref('t1');
    const store = createFileBrowserStore(token);
    await store.toggleNode('/Users/mac');
    expect(mockFsList).toHaveBeenLastCalledWith('/Users/mac', 't1');

    token.value = 't2'; // 15 分钟滑动续期换新令牌
    await store.refreshNode('/Users/mac');
    expect(mockFsList).toHaveBeenLastCalledWith('/Users/mac', 't2');
  });

  it('setRoot 空路径引导:回包 path=规范根,缓存重挂其键,根进 expanded', async () => {
    mockFsList.mockResolvedValue({ path: '/Users/mac', entries });
    const store = createFileBrowserStore(ref('tk'));
    const data = await store.setRoot('');
    expect(mockFsList).toHaveBeenCalledTimes(1);
    expect(mockFsList).toHaveBeenCalledWith('', 'tk');
    expect(data.path).toBe('/Users/mac');
    expect(store.root.value).toBe('/Users/mac');
    // 缓存挂在规范根键下(空串键不留),后续 toggleNode(root) 直接命中
    expect(store.children.value['/Users/mac']).toEqual(entries);
    expect(store.children.value['']).toBeUndefined();
    expect(store.expanded.value.has('/Users/mac')).toBe(true);

    await store.toggleNode('/Users/mac'); // 收起
    await store.toggleNode('/Users/mac'); // 再展开命中缓存
    expect(mockFsList).toHaveBeenCalledTimes(1);
  });
});
