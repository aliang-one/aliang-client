// app/website/src/composables/useNavigation.test.js
import { beforeEach, describe, expect, it } from 'vitest';
import { AGENT_SUB_TABS, useNavigation } from './useNavigation';

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
