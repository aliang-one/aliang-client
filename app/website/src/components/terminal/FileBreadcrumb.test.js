// 面包屑(导航补全批)纯组件测试:props 进(segments/currentPath)、jump/up 两
// 个 emit 出——窗层把 emit 接到 store.cd/cdUp,行为闭环在
// FileBrowserWindow.test.js 钉。createApp+h 挂载手法同 FileTree.test.js。
import { describe, it, expect, vi, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import FileBreadcrumb from './FileBreadcrumb.vue';

// 根+两级子目录的典型段序列(store.breadcrumbSegments 投影形态)
const SEGMENTS = [
  { name: '', path: '/Users/mac', isRoot: true },
  { name: 'Documents', path: '/Users/mac/Documents', isRoot: false },
  { name: 'project', path: '/Users/mac/Documents/project', isRoot: false },
];

let app = null;

async function mount(segments, onJump = () => {}, onUp = () => {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  app = createApp({
    render: () => h(FileBreadcrumb, {
      segments,
      currentPath: segments[segments.length - 1]?.path || '',
      onJump,
      onUp,
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

function click(el, testid) {
  el.querySelector(`[data-testid="${testid}"]`)?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

describe('FileBreadcrumb', () => {
  it('段渲染:根段=home 图标、子段=目录名;末段=当前(高亮非按钮,其余为按钮)', async () => {
    const el = await mount(SEGMENTS);
    expect(el.querySelector('[data-testid="file-breadcrumb"]')).toBeTruthy();
    // 段 0=根(home 图标,material 字面量)、可点;段 1=目录名、可点
    expect(el.querySelector('[data-testid="file-breadcrumb-seg-0"]').tagName).toBe('BUTTON');
    expect(el.querySelector('[data-testid="file-breadcrumb-seg-0"]').textContent).toContain('home');
    expect(el.querySelector('[data-testid="file-breadcrumb-seg-1"]').tagName).toBe('BUTTON');
    expect(el.querySelector('[data-testid="file-breadcrumb-seg-1"]').textContent).toContain('Documents');
    // 末段=当前目录:高亮非按钮(不可点)
    const last = el.querySelector('[data-testid="file-breadcrumb-seg-2"]');
    expect(last.tagName).toBe('SPAN');
    expect(last.textContent).toContain('project');
    expect(last.className).toContain('text-primary');
  });

  it('段点击→jump(该段完整 path);「↑」→up', async () => {
    const onJump = vi.fn();
    const onUp = vi.fn();
    const el = await mount(SEGMENTS, onJump, onUp);
    click(el, 'file-breadcrumb-seg-1');
    expect(onJump).toHaveBeenCalledWith('/Users/mac/Documents');
    click(el, 'file-breadcrumb-seg-0');
    expect(onJump).toHaveBeenCalledWith('/Users/mac');
    click(el, 'file-breadcrumb-up');
    expect(onUp).toHaveBeenCalledTimes(1);
  });

  it('根时(单段):↑ 禁用,唯一段=当前段(home 图标非按钮)', async () => {
    const el = await mount([SEGMENTS[0]]);
    expect(el.querySelector('[data-testid="file-breadcrumb-up"]').disabled).toBe(true);
    const seg = el.querySelector('[data-testid="file-breadcrumb-seg-0"]');
    expect(seg.tagName).toBe('SPAN');
    expect(seg.textContent).toContain('home');
  });
});
