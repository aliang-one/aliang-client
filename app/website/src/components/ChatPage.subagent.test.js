// ChatPage 子agent面板接线（Task 8）轻量集成测试：composable 与重组件全部
// mock 成最小桩，只保留真实链路三件套 SubagentStatusBar → SubagentPanel →
// SubagentDetail。断言：入口条点击→面板开+详情拉取；面板选中→
// subagentWatchingTaskId 置值（M1 重拉钩子激活口径）；面板关闭→watching 清空。
import { beforeEach, describe, it, expect, vi } from 'vitest';

vi.mock('../composables/useAliangChat', async () => {
  const { ref } = await import('vue');
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
  const refetchSpy = vi.fn();
  const noopFn = () => {};
  return {
    isComposingEvent: () => false,
    useAliangChat: () => ({
      activeApprovals: ref([]),
      activeConversation,
      activeId: ref('dev1'),
      activeIsDevice: ref(true),
      activeMessages: ref([]),
      activeOptionCards: ref([]),
      activeRunning: ref(false),
      activeSessionRunning: ref(false),
      activeSteerItems: ref([]),
      activeSubagents: ref([
        { taskId: 'ta_1', name: 'Explore', description: '搜索', phase: 'progress', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null },
        { taskId: 'ta_2', name: 'Reviewer', description: '审查', phase: 'completed', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: 4200, summary: null },
      ]),
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
      renameDraft: ref(''),
      renamingId: ref(null),
      renameSurface: ref('list'),
      respondApproval: noopFn,
      respondOption: noopFn,
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
      refetchSubagentsForSession: refetchSpy,
    }),
    __test: { subagentWatchingTaskId, refetchSpy, activeConversation },
  };
});
// 重组件桩化：本测试只关心子agent链路接线，其余聊天子树不装配。
vi.mock('./chat/ChatSidebar.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatHeader.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatSettingsBar.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatMessageList.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatComposer.vue', () => ({ default: { render: () => null } }));
vi.mock('./chat/ChatComposerTools.vue', () => ({ default: { render: () => null } }));

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

describe('ChatPage 子agent面板接线（Task 8 / M1）', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    __test.subagentWatchingTaskId.value = null;
  });

  it('入口条点击 → 面板打开 + 以当前会话 aiSessionId 拉详情（只消费 subagents[] 的入口）', async () => {
    const { el } = await mountPage();
    const entry = el.querySelector('button[data-testid="chat-subagent-entry"]');
    expect(entry).toBeTruthy();
    expect(el.querySelector('[data-testid="chat-subagent-panel"]')).toBeNull();
    expect(__test.refetchSpy).not.toHaveBeenCalled();
    entry.click();
    await nextTick();
    expect(el.querySelector('[data-testid="chat-subagent-panel"]')).toBeTruthy();
    expect(__test.refetchSpy).toHaveBeenCalledTimes(1);
    expect(__test.refetchSpy).toHaveBeenCalledWith('sess-1');
  });

  it('面板打开即自动选中首条 → watchingTaskId 置首条 taskId（M1 watching 激活）', async () => {
    const { el } = await mountPage();
    el.querySelector('button[data-testid="chat-subagent-entry"]').click();
    await nextTick(); // v-if 挂载面板（onMounted 自动选中经一次微任务渲染）
    expect(__test.subagentWatchingTaskId.value).toBe('ta_1');
    // 点击第二行 → watching 跟随选中切换
    const rows = el.querySelectorAll('[data-testid="chat-subagent-panel-item"]');
    rows[1].click();
    await nextTick();
    expect(__test.subagentWatchingTaskId.value).toBe('ta_2');
  });

  it('面板关闭 → watchingTaskId 清空（终态帧重拉钩子随之失活）', async () => {
    const { el } = await mountPage();
    el.querySelector('button[data-testid="chat-subagent-entry"]').click();
    await nextTick(); // v-if 挂载面板（onMounted 自动选中经一次微任务渲染）
    expect(__test.subagentWatchingTaskId.value).toBe('ta_1');
    el.querySelector('[data-testid="chat-subagent-panel-close"]').click();
    await nextTick();
    expect(el.querySelector('[data-testid="chat-subagent-panel"]')).toBeNull();
    expect(__test.subagentWatchingTaskId.value).toBeNull();
  });
});
