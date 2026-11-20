<template>
  <div class="fixed inset-0 z-[900] flex bg-white dark:bg-slate-950">
    <ChatSidebar
      :groups="conversationGroups"
      :merged-count="mergedConversations.length"
      :active-id="activeId"
      :show-mobile-sidebar="showMobileSidebar"
      :rename-draft="renameDraft"
      :renaming-id="renamingId"
      :rename-surface="renameSurface"
      :device-available="deviceAvailable"
      :set-rename-input="setRenameInput"
      @mobile-close="showMobileSidebar = false"
      @item-click="onSidebarItemClick"
      @create="createConversation"
      @rename-start="startRename"
      @rename-commit="commitRename"
      @rename-cancel="cancelRename"
      @rename-draft="renameDraft = $event"
    />

    <!-- Main chat area -->
    <div class="flex flex-1 flex-col min-w-0">
      <ChatHeader
        :conversation="activeConversation"
        :active-id="activeId"
        :active-is-device="activeIsDevice"
        :active-running="activeRunning"
        :renaming-id="renamingId"
        :rename-surface="renameSurface"
        :rename-draft="renameDraft"
        :set-rename-input="setRenameInput"
        @open-sidebar="showMobileSidebar = true"
        @rename-start="startRename"
        @rename-commit="commitRename"
        @rename-cancel="cancelRename"
        @rename-draft="renameDraft = $event"
        @delete="deleteConversation"
        @close="close"
      />

      <ChatSettingsBar
        :conversation="activeConversation"
        :active-running="activeRunning"
        :create-error="createError"
        :load-recent-projects="loadRecentProjects"
        :max-tokens-draft="maxTokensDraft"
        :context-window-draft="contextWindowDraft"
        :mcp-servers="mcpServers"
        :skills="skills"
        :mcp-loaded="mcpLoaded"
        :mcp-load-error="mcpLoadError"
        :ensure-mcp-loaded="ensureMcpServersLoaded"
        @apply-settings="onApplySettings"
        @update:max-tokens-draft="maxTokensDraft = $event"
        @update:context-window-draft="contextWindowDraft = $event"
        @commit-max-tokens="commitMaxTokens"
        @commit-context-window="commitContextWindow"
        @toggle-mcp="toggleMcpServer"
        @toggle-skill="toggleSkill"
      />

      <ChatMessageList
        :conversation="activeConversation"
        :active-id="activeId"
        :active-messages="activeMessages"
        :steer-items="activeSteerItems"
        :approvals="activeApprovals"
        :option-cards="activeOptionCards"
        :tool-asks="activeToolAsks"
        :hidden-option-ids="hiddenOptionIds"
        :now-tick="nowTick"
        :set-container="setMessagesContainer"
        :near-bottom="messagesNearBottom"
        :set-near-bottom="setMessagesNearBottom"
        :scroll-to-bottom="scrollToBottom"
        @approval-respond="onApprovalRespond"
        @option-respond="onOptionRespond"
        @tool-ask-respond="onToolAskRespond"
        @recall="startRecall"
      />

      <!-- 子agent状态入口条（Task 7）：消息区底部常驻，点击置面板打开标志
           （面板本体 Task 8 接线）。 -->
      <SubagentStatusBar
        v-if="activeIsAgentcore && activeSubagents.length > 0"
        :subagents="activeSubagents"
        @open="openSubagentPanel"
      />

      <!-- 子agent面板（Task 8, spec 2026-11-07-subagent-panel §三）：入口条
           open 后挂载；空数组由面板自身守卫不渲染。选中经 @select 写
           subagentWatchingTaskId（M1 终态静默重拉钩子），关闭回 null。 -->
      <SubagentPanel
        v-if="subagentPanelOpen"
        :open="subagentPanelOpen"
        :subagents="activeSubagents"
        @close="closeSubagentPanel"
        @select="onSubagentPanelSelect"
      />

      <ChatComposer
        v-if="activeId"
        v-model="inputText"
        :disabled="sendDisabled"
        :label="sendButtonLabel"
        :placeholder="inputPlaceholder"
        :running="activeSessionRunning"
        :steer-blocked="steerBlocked"
        :approval-blocked="sendApprovalBlocked"
        :recall-active="recallActive"
        :pending-attachments="pendingAttachments"
        :attachment-uploading="attachmentUploading"
        :add-attachment-files="addPendingAttachments"
        @remove-attachment="removePendingAttachment"
        :slash-open="slashOpen"
        :slash-matches="slashMatches"
        :slash-index="slashIndex"
        :set-input-el="setInputEl"
        @send="sendMessage"
        @stop="stopRun"
        @recall-confirm="onRecallConfirm"
        @recall-cancel="cancelRecall"
        @slash-apply="applySlash"
        @slash-index="slashIndex = $event"
        @slash-close="slashOpen = false"
        @slash-input="onSlashInput"
      >
        <template #tools>
          <ChatComposerTools
            :conversation="activeConversation"
            :settings-disabled="activeRunning"
            :pending-config="Boolean(activeConversation && activeConversation.pendingConfig)"
            @apply-settings="onApplySettings"
          />
        </template>
      </ChatComposer>
    </div>
  </div>
</template>

<script setup>
// 聊天页编排壳：状态与全部业务逻辑收在 useAliangChat（WS 生命周期/看门狗/
// 帧路由/审批/steer/三源清单/持久化），渲染拆在 chat/ 组件树（侧栏、顶栏、
// 设置条、消息列表、气泡、审批卡、输入区）。本文件只做组件接线。
import { computed, ref, watch } from 'vue';
import { useAliangChat } from '../composables/useAliangChat';
import { useNavigation } from '../composables/useNavigation';
import { useEscLayer } from '../composables/useEscStack';
import { normalizeDeviceProvider } from '../utils/chatSettings';
import ChatSidebar from './chat/ChatSidebar.vue';
import ChatHeader from './chat/ChatHeader.vue';
import ChatSettingsBar from './chat/ChatSettingsBar.vue';
import ChatMessageList from './chat/ChatMessageList.vue';
import ChatComposer from './chat/ChatComposer.vue';
import ChatComposerTools from './chat/ChatComposerTools.vue';
import SubagentStatusBar from './chat/SubagentStatusBar.vue';
import SubagentPanel from './chat/SubagentPanel.vue';

const {
  activeApprovals,
  activeConversation,
  activeId,
  activeIsDevice,
  activeMessages,
  activeOptionCards,
  activeToolAsks,
  activeRunning,
  activeSessionRunning,
  activeSteerItems,
  activeSubagents,
  applySessionSettings,
  applySlash,
  cancelRecall,
  cancelRename,
  close,
  commitContextWindow,
  commitMaxTokens,
  commitRename,
  conversationGroups,
  createConversation,
  createError,
  contextWindowDraft,
  deleteConversation,
  deviceAvailable,
  ensureMcpServersLoaded,
  inputPlaceholder,
  inputText,
  loadRecentProjects,
  maxTokensDraft,
  mcpLoadError,
  mcpLoaded,
  mcpServers,
  mergedConversations,
  messagesNearBottom,
  nowTick,
  onSidebarItemClick,
  onSlashInput,
  recallActive,
  refetchSubagentsForSession,
  renameDraft,
  renamingId,
  renameSurface,
  respondApproval,
  respondOption,
  respondToolAsk,
  scrollToBottom,
  sendMessage,
  sendRecall,
  sendButtonLabel,
  // 图片附件（spec 2026-11-05-chat-image-pipeline §3.6）：pending 态与上传
  // 动作下传 ChatComposer。
  pendingAttachments,
  attachmentUploading,
  addPendingAttachments,
  removePendingAttachment,
  sendApprovalBlocked,
  sendDisabled,
  setInputEl,
  setMessagesContainer,
  setMessagesNearBottom,
  setRenameInput,
  showMobileSidebar,
  slashIndex,
  slashMatches,
  slashOpen,
  skills,
  startRecall,
  startRename,
  steerBlocked,
  stopRun,
  subagentWatchingTaskId,
  toggleMcpServer,
  toggleSkill,
} = useAliangChat();

// ESC 页面返回层的闸门来源(组件本身由 App.vue v-if 控制,这里是双保险条件)
const { currentPage } = useNavigation();

// 子agent入口条挂载门（Task 7, spec 2026-11-07-subagent-panel §三）：仅
// AliangCode（agentcore 归一）会话且有子agent条目时挂载；条目数据经
// useAliangChat.activeSubagents（会话归属守卫在 getter 内）下传。
const activeIsAgentcore = computed(
  () => normalizeDeviceProvider(activeConversation.value?.provider) === 'agentcore'
);
// 面板打开标志（Task 8 接线落点）：open 后挂 SubagentPanel（Task 7 占位注释
// 的兑现）。打开瞬间以当前会话 aiSessionId 拉一次详情（只消费 subagents[]，
// 走 refreshSubagentsFromDetail 的 status→phase 归一），补齐终态条目的
// result/tools 与 WS 断连窗口丢失的条目；关闭/取消选中回写 watchingTaskId
// =null，M1 终态静默重拉钩子随之失活。
const subagentPanelOpen = ref(false);
function openSubagentPanel() {
  subagentPanelOpen.value = true;
}
function closeSubagentPanel() {
  subagentPanelOpen.value = false;
}
function onSubagentPanelSelect(taskId) {
  subagentWatchingTaskId.value = taskId || null;
}
watch(subagentPanelOpen, (open) => {
  if (open) {
    const sid = activeConversation.value?.aiSessionId || '';
    if (sid) refetchSubagentsForSession(sid);
  } else {
    subagentWatchingTaskId.value = null;
  }
});

// 设置条补丁落点：conv 取当前活跃会话（与拆分前各调用点同源），
// running 守卫/持久化/清错/服务端同步都在 applySessionSettings 内。
function onApplySettings(patch) {
  applySessionSettings(activeConversation.value, patch);
}

// 审批卡应答落点：conv 取当前活跃会话（列表只渲染活跃会话的审批）。
function onApprovalRespond(approval, decision) {
  respondApproval(activeConversation.value, approval, decision);
}

// 方案交互卡作答落点（Task 14）：两步作答的 respond 事件（selected/customText）
// 由 useAliangChat.respondOption 发 ai.option.response（成功置 answered 折叠态）。
function onOptionRespond(card, payload) {
  respondOption(activeConversation.value, card, payload?.selected, payload?.customText);
}

// tool_ask 提问卡作答落点（2026-11-08）：两步作答的 respond 事件
// （selected/customText）由 useAliangChat.respondToolAsk 发 tool_ask.response
// （成功置 answered 折叠态）。
function onToolAskRespond(card, payload) {
  respondToolAsk(activeConversation.value, card, payload?.selected, payload?.customText);
}

// 静态围栏卡防双份集合（Task 14）：活跃会话在挂交互卡的 option_id →
// MessageList → Bubble → ChatMarkdown 隐藏同 id 静态卡（无 option_id 的静态
// 块不参与比对，在渲染层自行容忍）。
const hiddenOptionIds = computed(() =>
  (activeOptionCards.value || []).map(c => c.optionId).filter(Boolean)
);

// 撤回确认重发落点（Task 13）：conv 取当前活跃会话；状态机/发送链全在
// useAliangChat（recalling 在途守卫、stop→终态等待、truncate create 确认门）。
function onRecallConfirm() {
  sendRecall(activeConversation.value);
}

// ── ESC 层级栈接入(替换文档级 handleKeydown 分流)──
// 页面返回层:当前页是 chat 时压层,handler 走 close()(在途重命名先落地再回
// dashboard,见 useAliangChat.close;组件由 v-if 挂载,computed 条件为双保险)。
// 撤回编辑态层后注册、后激活者压在页面层之上:banner 在场 Esc 只取消编辑态,
// 不关页(spec §A「退出:banner 取消按钮或 Esc」)。IME 组合期透传由 dispatcher
// 统一守(isComposing||keyCode===229),原 handleKeydown 内的 isComposingEvent
// 分支随之退役。
useEscLayer('page-return', close, () => currentPage.value === 'chat');
useEscLayer('chat-recall-edit', cancelRecall, () => recallActive.value);
// 子代理面板全屏抽屉（2026-11-20 审计二批 B9）：面板开着时 Esc 先关面板——
// 未注册层的漏网面板，Esc 直接命中 page-return 整页退出聊天，浏览上下文
// 全丢。层序=后激活在顶：面板层晚于 page-return 压栈，先收面板再回页面。
useEscLayer('chat-subagent-panel', closeSubagentPanel, () => subagentPanelOpen.value);
</script>
