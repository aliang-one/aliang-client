// 当前目录列表(导航补全批)纯组件测试:目录行 open/文件行 select 两个 emit、
// 空目录提示、loading spinner——窗层把 open 接 store.cd、select 接 selectNode
// (行为闭环在 FileBrowserWindow.test.js 钉)。createApp+h 手法同 FileTree.test.js。
// mtime 用 RFC3339 串(Go 侧 agent_fs.go 契约),兼容数字夹具。
import { describe, it, expect, vi, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import FileListView from './FileListView.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

const ENTRIES = [
  { name: 'Documents', type: 'dir', size: 0, mtime: '2026-11-22T10:00:00+08:00', mode: 'drwxr-xr-x' },
  { name: 'notes.txt', type: 'file', size: 1234, mtime: '2026-11-22T10:00:01+08:00', mode: '-rw-r--r--' },
];

let app = null;

async function mount(entries, { loading = false, onOpen = () => {}, onSelect = () => {} } = {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  app = createApp({
    render: () => h(FileListView, {
      entries,
      currentPath: '/Users/mac',
      loading,
      onOpen,
      onSelect,
    }),
  });
  app.mount(el);
  await nextTick();
  return el;
}

afterEach(() => {
  if (app) {
    app.unmount();
    app = null;
  }
  document.body.innerHTML = '';
});

function fireRow(el, name, type = 'click') {
  el.querySelector(`[data-testid="file-list-row-${name}"]`)?.dispatchEvent(new MouseEvent(type, { bubbles: true }));
}

describe('FileListView', () => {
  it('目录行 click/dblclick→open(entry);文件行 click→select(entry)', async () => {
    const onOpen = vi.fn();
    const onSelect = vi.fn();
    const el = await mount(ENTRIES, { onOpen, onSelect });
    fireRow(el, 'Documents');
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith(ENTRIES[0]);
    fireRow(el, 'Documents', 'dblclick'); // 双击亦进
    expect(onOpen).toHaveBeenCalledTimes(2);
    fireRow(el, 'notes.txt');
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect).toHaveBeenCalledWith(ENTRIES[1]);
  });

  it('行渲染:file-list 容器+按名 testid 行,目录/文件带类型标识;size/mtime 右对齐小字', async () => {
    const el = await mount(ENTRIES);
    expect(el.querySelector('[data-testid="file-list"]')).toBeTruthy();
    const dirRow = el.querySelector('[data-testid="file-list-row-Documents"]');
    expect(dirRow.getAttribute('data-type')).toBe('dir');
    expect(dirRow.textContent).toContain('—'); // 目录 size=— 占位
    const fileRow = el.querySelector('[data-testid="file-list-row-notes.txt"]');
    expect(fileRow.getAttribute('data-type')).toBe('file');
    expect(fileRow.textContent).toContain('1.2 KB'); // 1234 B 人读化
    expect(fileRow.textContent).toContain('2026'); // mtime(RFC3339 串)本地化呈现
  });

  it('空目录→file-list-empty 提示;loading→spinner 且不渲染行', async () => {
    const el = await mount([]);
    expect(el.querySelector('[data-testid="file-list-empty"]')).toBeTruthy();
    expect(el.querySelector('[data-testid="file-list-empty"]').textContent).toContain(t('fs_empty_dir'));

    const el2 = await mount(ENTRIES, { loading: true });
    expect(el2.querySelector('[data-testid="file-list-loading"]')).toBeTruthy();
    expect(el2.querySelector('[data-testid="file-list-row-notes.txt"]')).toBe(null);
  });
});
