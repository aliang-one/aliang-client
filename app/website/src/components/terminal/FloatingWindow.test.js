// 浮窗壳(P2.2)轻组件测试:createApp + h 挂载(仓内手法,同 TerminalPane.test.js)。
// jsdom 无 PointerEvent 构造器/setPointerCapture:用 MouseEvent 换 type 冒充
// pointer 事件(clientX/Y 语义同源,Vue 模板监听只看事件名);组件侧
// setPointerCapture 已做存在性守卫,捕获缺失时事件直接派发在头栏元素上,
// 拖拽三段(down/move/up)逻辑等价可测。视口取 jsdom 默认 1024×768。
import { describe, it, expect, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import FloatingWindow from './FloatingWindow.vue';

function pointerEvent(type, opts = {}) {
  return new MouseEvent(type, { bubbles: true, cancelable: true, ...opts });
}

async function mount(overrides = {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const events = { move: [], minimize: 0, maximize: 0, close: 0, focus: 0 };
  const base = {
    title: '终端 1',
    x: 100,
    y: 100,
    width: 600,
    height: 400,
    zIndex: 5,
    active: true,
    minimized: false,
  };
  const app = createApp({
    render: () =>
      h(
        FloatingWindow,
        {
          ...base,
          ...overrides,
          onMove: (x, y) => events.move.push({ x, y }),
          onMinimize: () => { events.minimize += 1; },
          onMaximizeToggle: () => { events.maximize += 1; },
          onClose: () => { events.close += 1; },
          onFocus: () => { events.focus += 1; },
        },
        () => h('div', { 'data-testid': 'slot-body' }, 'SLOT')
      ),
  });
  app.mount(el);
  await nextTick();
  const q = (sel) => el.querySelector(sel);
  return { el, events, app, q, header: () => q('[data-testid="floating-window-header"]') };
}

afterEach(() => {
  document.body.innerHTML = '';
});

describe('FloatingWindow', () => {
  it('渲染:title/槽内容/几何样式(zIndex=5)齐备', async () => {
    const m = await mount();
    expect(m.header().textContent).toContain('终端 1');
    expect(m.q('[data-testid="slot-body"]').textContent).toBe('SLOT');
    const shell = m.q('[data-testid="floating-window"]');
    expect(shell.style.left).toBe('100px');
    expect(shell.style.top).toBe('100px');
    expect(shell.style.width).toBe('600px');
    expect(shell.style.height).toBe('400px');
    expect(shell.style.zIndex).toBe('5');
    // active 高亮类在壳上
    expect(shell.className).toContain('border-primary');
  });

  it('右上三钮:各 emit 一次且 pointerdown 不透传 focus/拖拽', async () => {
    const m = await mount();
    m.q('[data-testid="floating-minimize"]').dispatchEvent(pointerEvent('pointerdown', { clientX: 900, clientY: 110 }));
    m.q('[data-testid="floating-minimize"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    m.q('[data-testid="floating-maximize"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    m.q('[data-testid="floating-close"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await nextTick();
    expect(m.events.minimize).toBe(1);
    expect(m.events.maximize).toBe(1);
    expect(m.events.close).toBe(1);
    // 钮上 pointerdown 被 stop:不 focus;按钮位置远超窗界却未启动拖拽(无 move)
    expect(m.events.focus).toBe(0);
    expect(m.events.move.length).toBe(0);
  });

  it('拖拽:down→focus,move 实时上抛钳制坐标,up 结束后 move 静默', async () => {
    const m = await mount();
    // jsdom 视口 1024×768,width=600/height=400 → x∈[-520,944]、y∈[0,728]
    m.header().dispatchEvent(pointerEvent('pointerdown', { clientX: 150, clientY: 130 }));
    expect(m.events.focus).toBe(1);
    // 甩到右下角外:偏移(50,30) → 原始(2950,1970) → 钳到(944,728)
    m.header().dispatchEvent(pointerEvent('pointermove', { clientX: 3000, clientY: 2000 }));
    expect(m.events.move[0]).toEqual({ x: 944, y: 728 });
    // 拖回界内:原样通过
    m.header().dispatchEvent(pointerEvent('pointermove', { clientX: 200, clientY: 200 }));
    expect(m.events.move[1]).toEqual({ x: 150, y: 170 });
    // 松手后再动指针不再上抛
    m.header().dispatchEvent(pointerEvent('pointerup', { clientX: 200, clientY: 200 }));
    m.header().dispatchEvent(pointerEvent('pointermove', { clientX: 500, clientY: 500 }));
    expect(m.events.move.length).toBe(2);
  });

  it('头栏双击 → maximize-toggle(按钮区双击豁免)', async () => {
    const m = await mount();
    m.header().dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
    expect(m.events.maximize).toBe(1);
    m.q('[data-testid="floating-close"]').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
    expect(m.events.maximize).toBe(1); // 钮上双击不触发
  });

  it('minimized=紧凑条:内容区 v-show 藏(槽仍挂载,终端保活)且高度 auto', async () => {
    const m = await mount({ minimized: true });
    const body = m.q('[data-testid="floating-window-body"]');
    expect(body).not.toBe(null); // v-show 非 v-if:TerminalPane/WS 不销毁
    expect(body.style.display).toBe('none');
    const shell = m.q('[data-testid="floating-window"]');
    expect(shell.style.height).toBe('auto');
    // 紧凑条仍可拖/仍可抓:头栏在
    expect(m.header()).not.toBe(null);
  });

  it('卸载不炸(拖拽中卸载的指针捕获清理路径)', async () => {
    const m = await mount();
    m.header().dispatchEvent(pointerEvent('pointerdown', { clientX: 150, clientY: 130 }));
    m.app.unmount();
    expect(m.el.textContent).toBe('');
  });
});
