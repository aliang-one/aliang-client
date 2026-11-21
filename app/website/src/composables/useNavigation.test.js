// app/website/src/composables/useNavigation.test.js
import { beforeEach, describe, expect, it } from 'vitest';
import { AGENT_SUB_TABS, useNavigation } from './useNavigation';
import { useEscStack } from './useEscStack';

// Esc 层级栈是模块级单例:每条用例前清空,并把模块级导航状态复位到基线,
// 防跨用例残留(层串扰/子 tab 残留导致压层层数变化)
beforeEach(() => {
  const nav = useNavigation();
  const { stack } = useEscStack();
  stack.value.length = 0;
  nav.showDashboard();
  nav.showPage('agent', { sub: 'records' });
  nav.showDashboard();
});

function pressEscape() {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }));
}

describe('useNavigation chat provider preset', () => {
  beforeEach(() => {
    const nav = useNavigation();
    nav.showChat();      // 清空预置
    nav.showDashboard();
  });

  it('showChat({ provider }) 写入预置并切到 chat 页', () => {
    const nav = useNavigation();
    nav.showChat({ provider: 'agentcore' });
    expect(nav.chatProviderPreset.value).toBe('agentcore');
    expect(nav.currentPage.value).toBe('chat');
  });

  it('无参调用清空预置（回到 ChatPage 自身的 claudecode 默认）', () => {
    const nav = useNavigation();
    nav.showChat({ provider: 'agentcore' });
    nav.showChat();
    expect(nav.chatProviderPreset.value).toBe('');
    expect(nav.currentPage.value).toBe('chat');
  });

  it('空白/非法 options 归一为空串而不是留下脏值', () => {
    const nav = useNavigation();
    nav.showChat({ provider: '  AgentCore  ' });
    expect(nav.chatProviderPreset.value).toBe('agentcore');
    nav.showChat(null);
    expect(nav.chatProviderPreset.value).toBe('');
  });

  it('非字符串 provider 归一为空串（不写出 [object Object] 脏预置）', () => {
    const nav = useNavigation();
    nav.showChat({ provider: { name: 'agentcore' } });
    expect(nav.chatProviderPreset.value).toBe('');
    nav.showChat({ provider: 123 });
    expect(nav.chatProviderPreset.value).toBe('');
  });

  it('预置对多个 consumer 是同一份模块级状态', () => {
    const a = useNavigation();
    const b = useNavigation();
    a.showChat({ provider: 'agentcore' });
    expect(b.chatProviderPreset.value).toBe('agentcore');
  });
});

describe('useNavigation agentSubTab', () => {
  beforeEach(() => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'records' });
    nav.showDashboard();
  });

  it('showPage("agent", { sub }) 写入合法子 tab', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'aliangcode' });
    expect(nav.currentPage.value).toBe('agent');
    expect(nav.agentSubTab.value).toBe('aliangcode');
  });

  it('未知 sub 归一为 records（深链 fallback）', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'bogus' });
    expect(nav.agentSubTab.value).toBe('records');
  });

  it('无 sub 参数保留现状（仪表盘跳转不打断用户所在子 tab）', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'aliangcode' });
    nav.showDashboard();
    nav.showPage('agent');
    expect(nav.agentSubTab.value).toBe('aliangcode');
  });

  it('非 agent 页不改写子 tab', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'user-agent' });
    nav.showPage('settings', { sub: 'records' });
    expect(nav.agentSubTab.value).toBe('user-agent');
  });

  it('AGENT_SUB_TABS 词表与 AgentPage 子 tab 一一对应', () => {
    expect(AGENT_SUB_TABS).toEqual(['records', 'user-agent', 'aliangcode', 'peer-access']);
  });

  it('peer-access 是合法子 tab（深链写入不被归一）', () => {
    const nav = useNavigation();
    nav.showPage('agent', { sub: 'peer-access' });
    expect(nav.agentSubTab.value).toBe('peer-access');
  });
});

describe('useNavigation ESC 页面返回层', () => {
  it.each(['settings', 'agent', 'user', 'log'])('进入 %s 页压返回层,Esc 回 dashboard 且层移除', (page) => {
    const nav = useNavigation();
    const { stack } = useEscStack();

    nav.showPage(page);
    expect(stack.value.some((l) => l.label === 'page-return')).toBe(true);

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
    expect(stack.value.some((l) => l.label === 'page-return')).toBe(false);
  });

  it('dashboard 不压返回层,Esc 无操作', () => {
    const nav = useNavigation();
    const { stack } = useEscStack();

    nav.showDashboard();
    expect(stack.value.length).toBe(0);

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
  });

  it('chat 页返回层由 ChatPage 组件自管,导航模块不代压', () => {
    const nav = useNavigation();
    const { stack } = useEscStack();

    nav.showChat({ provider: 'agentcore' });
    expect(stack.value.some((l) => l.label === 'page-return')).toBe(false);
  });
});

describe('useNavigation terminal 页', () => {
  it('showTerminal 切到 terminal 页', () => {
    const nav = useNavigation();
    nav.showTerminal();
    expect(nav.currentPage.value).toBe('terminal');
  });

  it('terminal 压 page-return 层,Esc 回 dashboard 且层移除（工作台组件层 terminal-workspace 另行自管,双层同归 dashboard）', () => {
    const nav = useNavigation();
    const { stack } = useEscStack();

    nav.showTerminal();
    expect(stack.value.some((l) => l.label === 'page-return')).toBe(true);

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
    expect(stack.value.some((l) => l.label === 'page-return')).toBe(false);
  });
});

describe('useNavigation ESC 子 tab 收回层', () => {
  it('agent 非默认子 tab:第一次 Esc 收回 records,第二次 Esc 回 dashboard', () => {
    const nav = useNavigation();

    nav.showPage('agent', { sub: 'aliangcode' });

    pressEscape();
    expect(nav.agentSubTab.value).toBe('records');
    expect(nav.currentPage.value).toBe('agent');

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
  });

  it('agent 默认子 tab 一步回 dashboard(无收回层)', () => {
    const nav = useNavigation();
    const { stack } = useEscStack();

    nav.showPage('agent', { sub: 'records' });
    expect(stack.value.some((l) => l.label === 'subtab-collapse')).toBe(false);

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
  });

  it('跨页陈旧层:agent 非默认子 tab 切到 settings,Esc 直接回 dashboard 且不收回隐藏子 tab', () => {
    const nav = useNavigation();

    nav.showPage('agent', { sub: 'user-agent' });
    nav.showPage('settings');

    pressEscape();
    expect(nav.currentPage.value).toBe('dashboard');
    expect(nav.agentSubTab.value).toBe('user-agent'); // 导航语义不重置,只是收回层不跨页生效
  });

  it('反向跨页:settings 停留后回 agent,恢复压收回层', () => {
    const nav = useNavigation();

    nav.showPage('agent', { sub: 'aliangcode' });
    nav.showPage('settings');
    pressEscape(); // settings 页一步回 dashboard
    expect(nav.currentPage.value).toBe('dashboard');

    nav.showPage('agent'); // 无参跳转保留子 tab(现有语义)
    pressEscape();
    expect(nav.agentSubTab.value).toBe('records');
    expect(nav.currentPage.value).toBe('agent');
  });
});
