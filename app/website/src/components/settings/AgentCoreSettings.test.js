import { afterEach, describe, expect, it, vi } from 'vitest';

// AgentCoreSettings.vue 求值连带 i18n loadLocale()(node 环境无 DOM),mock 掉
// 即可导入 SFC(t 原样回键,断言不硬编码文案;EgressTargetSettings.test.js 同
// 范式)。(endpoint 二期 Task 8:「内核端点(连到哪)」半卡退役——卡片只剩
// 守卫阈值,改名「内核调优」,旧 agentcore_llm 配置给灰字提示)
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import AgentCoreSettings from './AgentCoreSettings.vue';

const mountedApps = [];

// v-model 数据同步,但 DOM 渲染是异步的——行内报错断言前先冲微任务队列
// (EgressTargetSettings.test.js 同范式)。
const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

function mountCard(props = {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const saves = [];
  const app = createApp({
    render: () => h(AgentCoreSettings, {
      config: {},
      ...props,
      onSave: (payload) => saves.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saves };
}

afterEach(() => {
  while (mountedApps.length) {
    const { app, el } = mountedApps.pop();
    app.unmount();
    el.remove();
  }
});

describe('AgentCoreSettings 端点半卡退役(endpoint 二期 Task 8)', () => {
  it('卡片只剩守卫阈值:无 enable 勾选/baseUrl/apiKey 输入与显隐键按钮', () => {
    const { el } = mountCard();
    expect(el.querySelector('input[type="checkbox"]')).toBeNull();
    expect(el.querySelector('input[type="text"]')).toBeNull();
    expect(el.querySelector('input[type="password"]')).toBeNull();
    // 全卡只剩守卫阈值一个 number 输入
    const inputs = [...el.querySelectorAll('input')];
    expect(inputs).toHaveLength(1);
    expect(inputs[0].type).toBe('number');
    // 退役 i18n 键不再被模板引用(t 原样回键,残留引用会以键名出现)
    expect(el.textContent).not.toContain('agentCore_enable');
    expect(el.textContent).not.toContain('agentCore_baseUrl');
    expect(el.textContent).not.toContain('agentCore_apiKey');
    // 守卫阈值区块仍在
    expect(el.textContent).toContain('agentCore_guardTitle');
    expect(el.textContent).toContain('agentCore_guardHint');
  });

  it('save 载荷只含 agentcore 段,不再含 agentcore_llm 键', () => {
    const config = { agentcore: { max_identical_tool_turns: 5 } };
    const { el, saves } = mountCard({ config });
    const guard = el.querySelector('input[type="number"]');
    guard.value = '12';
    guard.dispatchEvent(new Event('input', { bubbles: true }));
    el.querySelector('[data-agentcore-save]').click();
    expect(saves).toHaveLength(1);
    expect(saves[0].agentcore).toEqual({ max_identical_tool_turns: 12 });
    expect(saves[0].agentcore_llm).toBeUndefined();
    expect('agentcore_llm' in saves[0]).toBe(false);
  });

  it('守卫阈值非法(51 越界)时行内报错且 save 不发射', async () => {
    const { el, saves } = mountCard();
    const guard = el.querySelector('input[type="number"]');
    guard.value = '51';
    guard.dispatchEvent(new Event('input', { bubbles: true }));
    await flush();
    expect(el.textContent).toContain('agentCore_error_guardInvalid');
    el.querySelector('[data-agentcore-save]').click();
    expect(saves).toHaveLength(0);
  });

  it('旧配置灰字提示:legacyAgentCoreLLM=true 渲染,false 不渲染', () => {
    const on = mountCard({ legacyAgentCoreLLM: true });
    const hint = on.el.querySelector('[data-agentcore-legacy-hint]');
    expect(hint).toBeTruthy();
    expect(hint.textContent).toContain('agentCore_legacyHint');
    const off = mountCard({ legacyAgentCoreLLM: false });
    expect(off.el.querySelector('[data-agentcore-legacy-hint]')).toBeNull();
  });

  it('卡片标题仍是 agentCore_title 键(值已改名「内核调优」,文案断言在 i18n 侧)', () => {
    const { el } = mountCard();
    expect(el.textContent).toContain('agentCore_title');
    expect(el.textContent).toContain('agentCore_description');
    expect(el.textContent).toContain('agentCore_hint');
  });
});
