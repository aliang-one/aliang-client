// ChatPage 会话信息抽屉接线测试（spec 2026-11-26-chat-info-drawer-design）：
// 复用 ChatPage.subagent.test.js 的「composable 最小桩+重组件桩化」手法，但
// ChatHeader 与 ChatInfoDrawer 保持真实（本批被测对象）。断言：顶栏入口开抽屉
// +以当前会话 aiSessionId 拉诊断详情；抽屉「查看详情」开子agent面板（z 序在其
// 之上）；Esc 层序（后激活在顶：先面板后抽屉最后页面）；切会话收抽屉。
import { beforeEach, describe, it, expect, vi } from 'vitest';

vi.mock('../composables/useAliangChat', async () => {
  const { ref } = await import('vue');
  const activeId = ref('dev1');
  const activeConversation = ref({
    id: 'dev1',
    name: '测试会话',
    provider: 'agentcore',
    aiSessionId: 'sess-1',
    messages: [],
    approvals: [],
    optionCards: [],
  });
  const subagentWatchingTaskId = ref(null);
  const refetchDiagSpy = vi.fn();
  const refetchSubSpy = vi.fn();
  const noopFn = () => {};
  const RUNNING_DIAG = {
    turns: 4, contextTokens: 51234, contextWindow: 200000,
    dupStreak: 1, errStreak: 0, truncStreak: 2, elapsedMs: 61320,
    stopReason: 'end_turn', terminal: false, reason: null, status: null, error: null,
    filesTouchedCount: 3, gitChangedCount: 1,
  };
  return {
    isComposingEvent: () => false,
    useAliangChat: () => ({
      activeApprovals: ref([]),
      activeConversation,
      activeId,
      activeIsDevice: ref(false),
      activeMessages: ref([]),
      activeOptionCards: ref([]),
      activeRunning: ref(false),
      activeSessionRunning: ref(false),
      activeSteerItems: ref([]),
      activeSubagents: ref([
        { taskId: 'ta_1', name: 'Explore', description: '搜索', phase: 'progress', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null },
      ]),
      activeDiagnostics: ref(RUNNING_DIAG),
      applySessionSettings: noopFn,
      applySlash: noopFn,
      cancelRecall: noopFn,
      cancelRename: noopFn,
      close: noopFn,
      commitContextWindow: noopFn,
      commitMaxTokens: noopFn,
      commitRename: noopFn,
      conversationGroups: ref([]),
      createConversation: noopFn,
      createError: ref(''),
      contextWindowDraft: ref(''),
      deleteConversation: noopFn,
      deviceAvailable: ref(false),
      ensureMcpServersLoaded: () => Promise.resolve(),
      inputPlaceholder: ref(''),
      inputText: ref(''),
      loadRecentProjects: () => Promise.resolve([]),
      maxTokensDraft: ref(''),
      mcpLoadError: ref(''),
      mcpLoaded: ref(false),
      mcpServers: ref([]),
      mergedConversations: ref([]),
      messagesNearBottom: ref(true),
      nowTick: ref(0),
      onSidebarItemClick: noopFn,
      onSlashInput: noopFn,
      recallActive: ref(false),
      refetchDiagnosticsForSession: refetchDiagSpy,
      refetchSubagentsForSession: refetchSubSpy,
      renameDraft: ref(''),
      renamingId: ref(null),
      renameSurface: ref('list'),
      respondApproval: noopFn,
      respondOption: noopFn,
      respondToolAsk: noopFn,
      scrollToBottom: noopFn,
      sendMessage: noopFn,
      sendRecall: noopFn,
      sendButtonLabel: ref(''),
      pendingAttachments: ref([]),
      attachmentUploading: ref(false),
      addPendingAttachments: noopFn,
      removePendingAttachment: noopFn,
      sendApprovalBlocked: ref(false),
      sendDisabled: ref(false),
      setInputEl: noopFn,
      setMessagesContainer: noopFn,
      setMessagesNearBottom: noopFn,
      setRenameInput: noopFn,
      showMobileSidebar: ref(false),
      slashIndex: ref(-1),
      slashMatches: ref([]),
      slashOpen: ref(false),
      skills: ref([]),
      startRecall: noopFn,
      startRename: noopFn,
      steerBlocked: ref(false),
      stopRun: noopFn,
      toggleMcpServer: noopFn,
      toggleSkill: noopFn,
      subagentsBySession: {},
      activeHasRunningSubagents: ref(true),
      subagentWatchingTaskId,
    }),
    __test: { activeId, refetchDiagSpy },
  };
});
// 重组件桩化：只装配被测链路（真实 ChatHeader → 真实 ChatInfoDrawer → 桩
// SubagentPanel），其余聊天子树不装配。
vi.mock('./chat/ChatSidebar.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatSettingsBar.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatMessageList.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatComposer.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatComposerTools.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/SubagentStatusBar.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/SubagentPanel.vue', async () => {
  const { h } = await import('vue');
  return { default: { render: () => h('div', { 'data-testid': 'stub-subagent-panel' }) } };
});

import { createApp, h, nextTick } from 'vue';
import ChatPage from './ChatPage.vue';
import { __test } from '../composables/useAliangChat';

async function mountPage() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(ChatPage) });
  app.mount(el);
  await nextTick();
  return { el, app };
}

async function openDrawer(el) {
  el.querySelector('button[data-testid="chat-info-entry"]').click();
  await nextTick();
}

describe('ChatPage 会话信息抽屉接线', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    __test.activeId.value = 'dev1';
  });

  it('抽屉常驻挂载但初始关闭（滑出动画前提）；顶栏入口点击 → 打开 + 以 aiSessionId 拉诊断详情', async () => {
    const { el } = await mountPage();
    const drawer = el.querySelector('[data-testid="chat-info-drawer"]');
    expect(drawer).toBeTruthy(); // 常驻挂载
    expect(drawer.className).toContain('translate-x-full'); // 初始关闭
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeNull();
    expect(__test.refetchDiagSpy).not.toHaveBeenCalled();
    await openDrawer(el);
    expect(drawer.className).toContain('translate-x-0');
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeTruthy();
    expect(__test.refetchDiagSpy).toHaveBeenCalledTimes(1);
    expect(__test.refetchDiagSpy).toHaveBeenCalledWith('sess-1');
  });

  it('抽屉「查看详情」→ 子agent面板盖在抽屉之上（两者同开）', async () => {
    const { el } = await mountPage();
    await openDrawer(el);
    expect(el.querySelector('[data-testid="stub-subagent-panel"]')).toBeNull();
    el.querySelector('[data-testid="chat-info-drawer-subagent-detail"]').click();
    await nextTick();
    expect(el.querySelector('[data-testid="stub-subagent-panel"]')).toBeTruthy();
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeTruthy(); // 抽屉仍开
  });

  it('Esc 层序（后激活在顶）：抽屉+面板同开先收面板；只开抽屉先收抽屉、页面不退', async () => {
    const { el } = await mountPage();
    await openDrawer(el);
    el.querySelector('[data-testid="chat-info-drawer-subagent-detail"]').click();
    await nextTick();
    // 第一刀：收面板，抽屉保持
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await nextTick();
    expect(el.querySelector('[data-testid="stub-subagent-panel"]')).toBeNull();
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeTruthy();
    // 第二刀：收抽屉，页面本体还在
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await nextTick();
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeNull();
    expect(el.querySelector('[data-testid="chat-info-drawer"]').className).toContain('translate-x-full');
    expect(el.querySelector('button[data-testid="chat-info-entry"]')).toBeTruthy();
  });

  it('抽屉开着切会话：抽屉收起（防旧会话快照错位呈现，D2 同口径）', async () => {
    const { el } = await mountPage();
    await openDrawer(el);
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeTruthy();
    __test.activeId.value = 'dev2';
    await nextTick();
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeNull();
    expect(el.querySelector('[data-testid="chat-info-drawer"]').className).toContain('translate-x-full');
  });
});
