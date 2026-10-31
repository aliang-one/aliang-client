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
        @apply-settings="onApplySettings"
      />

      <ChatMessageList
        :conversation="activeConversation"
        :active-id="activeId"
        :active-messages="activeMessages"
        :steer-items="activeSteerItems"
        :approvals="activeApprovals"
        :now-tick="nowTick"
        :set-container="setMessagesContainer"
        :near-bottom="messagesNearBottom"
        :set-near-bottom="setMessagesNearBottom"
        :scroll-to-bottom="scrollToBottom"
        @approval-respond="onApprovalRespond"
        @recall="startRecall"
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
      />
    </div>
  </div>
</template>

<script setup>
// 聊天页编排壳：状态与全部业务逻辑收在 useAliangChat（WS 生命周期/看门狗/
// 帧路由/审批/steer/三源清单/持久化），渲染拆在 chat/ 组件树（侧栏、顶栏、
// 设置条、消息列表、气泡、审批卡、输入区）。本文件只做组件接线。
import { onMounted, onUnmounted } from 'vue';
import { useAliangChat } from '../composables/useAliangChat';
import ChatSidebar from './chat/ChatSidebar.vue';
import ChatHeader from './chat/ChatHeader.vue';
import ChatSettingsBar from './chat/ChatSettingsBar.vue';
import ChatMessageList from './chat/ChatMessageList.vue';
import ChatComposer from './chat/ChatComposer.vue';

const {
  activeApprovals,
  activeConversation,
  activeId,
  activeIsDevice,
  activeMessages,
  activeRunning,
  activeSessionRunning,
  activeSteerItems,
  applySessionSettings,
  applySlash,
  cancelRecall,
  cancelRename,
  close,
  commitRename,
  conversationGroups,
  createConversation,
  createError,
  deleteConversation,
  deviceAvailable,
  inputPlaceholder,
  inputText,
  loadRecentProjects,
  mergedConversations,
  messagesNearBottom,
  nowTick,
  onSidebarItemClick,
  onSlashInput,
  recallActive,
  renameDraft,
  renamingId,
  renameSurface,
  respondApproval,
  scrollToBottom,
  sendMessage,
  sendRecall,
  sendButtonLabel,
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
  startRecall,
  startRename,
  steerBlocked,
  stopRun,
} = useAliangChat();

// 设置条补丁落点：conv 取当前活跃会话（与拆分前各调用点同源），
// running 守卫/持久化/清错/服务端同步都在 applySessionSettings 内。
function onApplySettings(patch) {
  applySessionSettings(activeConversation.value, patch);
}

// 审批卡应答落点：conv 取当前活跃会话（列表只渲染活跃会话的审批）。
function onApprovalRespond(approval, decision) {
  respondApproval(activeConversation.value, approval, decision);
}

// 撤回确认重发落点（Task 13）：conv 取当前活跃会话；状态机/发送链全在
// useAliangChat（recalling 在途守卫、stop→终态等待、truncate create 确认门）。
function onRecallConfirm() {
  sendRecall(activeConversation.value);
}

function handleKeydown(e) {
  if (e.key === 'Escape') {
    close();
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown);
});

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown);
});
</script>
