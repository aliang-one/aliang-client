// ChatComposer 附件交互（2026-11-20 审计首批 ④⑥）轻量组件测试：
//   ④ 回形针按钮 click 必须触发隐藏 file input（旧实现模板调函数 ref 注册
//      函数的 .click() → TypeError，发图入口死键，自 c8afcde1 起无测试覆盖）；
//   ⑥ chips 同 ref 不同 uid：key 用 uid（Vue 重复 key 不再撞）、移除按 uid
//      精确命中（同 ref 双删已根治）。
// 挂载与仓内既有组件测试同款裸 createApp（仓未装 @vue/test-utils）。
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import ChatComposer from './ChatComposer.vue';

const baseProps = {
  modelValue: '',
  setInputEl: () => {},
  addAttachmentFiles: () => {},
};

const mounted = [];

async function mountComposer(pendingAttachments = [], removeSpy = null) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const emitted = [];
  const app = createApp({
    render: () =>
      h(ChatComposer, {
        ...baseProps,
        pendingAttachments,
        onRemoveAttachment: (v) => {
          emitted.push(v);
          if (removeSpy) removeSpy(v);
        },
      }),
  });
  app.mount(el);
  mounted.push({ app, el });
  await nextTick();
  return { el, emitted };
}

afterEach(() => {
  while (mounted.length) {
    const { app, el } = mounted.pop();
    app.unmount();
    el.remove();
  }
});

const qs = (el, sel) => el.querySelector(sel);

describe('④ 附件按钮触发隐藏 file input', () => {
  it('点击回形针：file input.click 被调、事件 handler 不抛 TypeError', async () => {
    const { el } = await mountComposer();
    const input = qs(el, 'input[type="file"]');
    expect(input).toBeTruthy();
    const clickSpy = vi.spyOn(input, 'click').mockImplementation(() => {});
    const button = qs(el, '[data-testid="chat-attach-button"]');
    expect(button).toBeTruthy();
    // 旧实现此处抛 bindFileInputEl.click is not a function（模板把函数 ref
    // 注册函数当 DOM 元素调）；修复后走 ref 解包的 fileInputEl。
    button.click();
    await nextTick();
    expect(clickSpy).toHaveBeenCalledTimes(1);
  });
});

describe('⑥ 附件 chips：uid key + 按 uid 移除', () => {
  const sameRefPair = [
    { ref: 'sess/shot.png', name: 'shot.png', mime: 'image/png', bytes: 1, uid: 'att-1' },
    { ref: 'sess/shot.png', name: 'shot.png', mime: 'image/png', bytes: 1, uid: 'att-2' },
  ];

  it('同 ref 两条 chips 都渲染（uid key 不再撞 Vue 重复 key）', async () => {
    const { el } = await mountComposer(sameRefPair);
    const chips = qs(el, '[data-testid="chat-attach-chips"]');
    expect(chips).toBeTruthy();
    // 直接子 span = 两条 chip（末尾的 uploading 计数 span 在条件外：空上传
    // 时不渲染，这里 pendingAttachments 非空且无上传 → 恰两条）。
    const chipNodes = [...chips.children].filter((n) => n.tagName === 'SPAN' && n.querySelector('button'));
    expect(chipNodes).toHaveLength(2);
  });

  it('点移除：出口的是 uid（非 ref），同 ref 双删不再发生', async () => {
    const { el, emitted } = await mountComposer(sameRefPair);
    const buttons = [...el.querySelectorAll('[data-testid="chat-attach-chips"] button')];
    expect(buttons).toHaveLength(2);
    buttons[0].click();
    await nextTick();
    expect(emitted).toEqual(['att-1']);
    buttons[1].click();
    await nextTick();
    expect(emitted).toEqual(['att-1', 'att-2']);
  });

  it('无 uid 的旧数据兼容：出口退化回 ref', async () => {
    const legacy = [{ ref: 'sess/old.png', name: 'old.png', mime: 'image/png', bytes: 1 }];
    const { el, emitted } = await mountComposer(legacy);
    el.querySelector('[data-testid="chat-attach-chips"] button').click();
    await nextTick();
    expect(emitted).toEqual(['sess/old.png']);
  });
});
