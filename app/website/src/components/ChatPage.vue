<template>
  <div class="fixed inset-0 z-[900] flex bg-white dark:bg-slate-950">
    <!-- Mobile: conversation list overlay -->
    <div
      v-if="showMobileSidebar"
      class="fixed inset-0 z-10 bg-black/50 md:hidden"
      @click="showMobileSidebar = false"
    ></div>

    <!-- Sidebar: conversation list -->
    <aside
      class="fixed left-0 top-0 bottom-0 z-20 w-72 flex flex-col border-r border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-900 transition-transform duration-200 md:relative md:translate-x-0"
      :class="showMobileSidebar ? 'translate-x-0' : '-translate-x-full'"
    >
      <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <h2 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('chat_title') }}</h2>
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700"
          @click="createConversation"
        >
          <span class="material-symbols-outlined text-lg">add</span>
        </button>
      </div>

      <div class="flex-1 overflow-y-auto py-1">
        <template v-if="groupedConversations.today.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_today') }}</div>
          <button
            v-for="c in groupedConversations.today"
            :key="c.id"
            type="button"
            class="w-full text-left px-4 py-2.5 text-sm transition-colors"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
          >
            <div class="truncate">{{ c.title || t('chat_emptyTitle') }}</div>
          </button>
        </template>
        <template v-if="groupedConversations.yesterday.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_yesterday') }}</div>
          <button
            v-for="c in groupedConversations.yesterday"
            :key="c.id"
            type="button"
            class="w-full text-left px-4 py-2.5 text-sm transition-colors"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
          >
            <div class="truncate">{{ c.title || t('chat_emptyTitle') }}</div>
          </button>
        </template>
        <template v-if="groupedConversations.older.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_older') }}</div>
          <button
            v-for="c in groupedConversations.older"
            :key="c.id"
            type="button"
            class="w-full text-left px-4 py-2.5 text-sm transition-colors"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
          >
            <div class="truncate">{{ c.title || t('chat_emptyTitle') }}</div>
          </button>
        </template>
        <div v-if="conversations.length === 0" class="px-4 py-8 text-center text-sm text-slate-400">
          {{ t('chat_noConversations') }}
        </div>
      </div>
    </aside>

    <!-- Main chat area -->
    <div class="flex flex-1 flex-col min-w-0">
      <!-- Header -->
      <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-100 md:hidden dark:hover:bg-slate-800"
          @click="showMobileSidebar = true"
        >
          <span class="material-symbols-outlined text-lg">menu</span>
        </button>
        <h3 class="flex-1 truncate text-sm font-semibold text-slate-900 dark:text-white">
          {{ activeConversation?.title || t('chat_emptyTitle') }}
        </h3>
        <span
          v-if="activeId && activeRunning"
          class="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
        >
          <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
        </span>
        <button
          v-if="activeId"
          type="button"
          class="rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-rose-500 dark:hover:bg-slate-800"
          @click="deleteConversation"
        >
          <span class="material-symbols-outlined text-lg">delete</span>
        </button>
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800"
          @click="close"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>

      <!-- Messages -->
      <div ref="messagesContainer" class="flex-1 overflow-y-auto bg-slate-50 p-4 dark:bg-slate-800/40">
        <div v-if="!activeId" class="flex h-full items-center justify-center">
          <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
        </div>
        <template v-else>
          <div v-if="activeMessages.length === 0" class="flex h-full items-center justify-center">
            <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
          </div>
          <div v-for="(msg, idx) in activeMessages" :key="idx" class="mb-3">
            <div class="mb-1 flex items-center gap-2 text-xs text-slate-400">
              <span>{{ msg.role === 'user' ? t('dash_me') : t('dash_ai') }}</span>
              <span v-if="msg.role === 'assistant' && msg.status === 'running'" class="inline-flex items-center gap-1 text-primary">
                <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
              </span>
              <span v-else-if="msg.role === 'assistant' && msg.status === 'done'" class="text-emerald-500">{{ t('chat_statusDone') }}</span>
              <span v-else-if="msg.role === 'assistant' && msg.status === 'error'" class="text-rose-500">!</span>
            </div>
            <div
              class="inline-block max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm"
              :class="msg.role === 'user'
                ? 'ml-auto block bg-primary text-white'
                : msg.status === 'error'
                  ? 'bg-rose-50 text-rose-600 dark:bg-rose-900/30 dark:text-rose-300'
                  : 'bg-white text-slate-700 dark:bg-slate-700 dark:text-slate-100'"
            >
              <span
                v-if="msg.role === 'assistant' && msg.status === 'running' && !msg.content"
                class="ai-thinking"
              >
                <span class="thinking-dot"></span><span class="thinking-dot"></span><span class="thinking-dot"></span>
                <span class="ml-1 align-middle">{{ t('chat_aiThinking') }}</span>
              </span>
              <template v-else>{{ msg.content }}</template>
            </div>
          </div>
          <div
            v-for="approval in activeApprovals"
            :key="approval.key"
            class="mb-3 max-w-[85%] rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-950 shadow-sm dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-100"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="material-symbols-outlined text-base">{{ approvalIcon(approval) }}</span>
              <span class="font-semibold">{{ approval.title || t('chat_approvalTitle') }}</span>
              <span class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/60 dark:text-amber-200">
                {{ approval.status === 'responding' ? t('chat_approvalResponding') : t('chat_approvalPending') }}
              </span>
            </div>
            <p v-if="approval.reason" class="mt-1 whitespace-pre-wrap text-xs text-amber-800 dark:text-amber-200">
              {{ approval.reason }}
            </p>
            <pre
              v-if="approval.command"
              class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
            >{{ approval.command }}</pre>
            <pre
              v-else-if="approvalDetailText(approval)"
              class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
            >{{ approvalDetailText(approval) }}</pre>
            <div v-if="approval.cwd || approval.toolName" class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-amber-700 dark:text-amber-200">
              <span v-if="approval.toolName">{{ t('chat_approvalTool') }}: {{ approval.toolName }}</span>
              <span v-if="approval.cwd">{{ t('chat_approvalCwd') }}: {{ approval.cwd }}</span>
            </div>
            <div class="mt-2 flex flex-wrap gap-2">
              <button
                v-if="approvalAllows(approval, 'accept_for_session')"
                type="button"
                class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'accept_for_session')"
              >
                <span class="material-symbols-outlined text-sm">verified_user</span>{{ t('chat_approvalAcceptSession') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'accept')"
                type="button"
                class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'accept')"
              >
                <span class="material-symbols-outlined text-sm">check</span>{{ t('chat_approvalAccept') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'decline')"
                type="button"
                class="inline-flex items-center gap-1 rounded border border-amber-300 bg-white px-2.5 py-1 text-xs font-semibold text-amber-800 transition hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-700 dark:bg-slate-900 dark:text-amber-100 dark:hover:bg-amber-900/50"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'decline')"
              >
                <span class="material-symbols-outlined text-sm">block</span>{{ t('chat_approvalDecline') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'cancel')"
                type="button"
                class="inline-flex items-center gap-1 rounded border border-slate-300 bg-white px-2.5 py-1 text-xs font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'cancel')"
              >
                <span class="material-symbols-outlined text-sm">close</span>{{ t('chat_approvalCancel') }}
              </button>
            </div>
            <p v-if="approval.error" class="mt-1 text-xs text-rose-600 dark:text-rose-300">{{ approval.error }}</p>
          </div>
        </template>
      </div>

      <!-- Input -->
      <div v-if="activeId" class="border-t border-slate-200 p-4 dark:border-slate-700">
        <div class="flex items-center gap-2">
          <input
            ref="inputRef"
            v-model="inputText"
            type="text"
            :placeholder="t('chat_placeholder')"
            :disabled="sending"
            class="h-10 flex-1 rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
            @keydown.enter.prevent="sendMessage"
          />
          <button
            type="button"
            class="h-10 rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="sending"
            @click="sendMessage"
          >
            {{ sending ? t('chat_sending') : t('chat_send') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from '../composables/useNavigation';
import { useAuthStore } from '../stores/auth';

const STORAGE_KEY = 'aliang-chat-data';

const { t } = useI18n();
const { showDashboard } = useNavigation();
const { isAuthenticated } = useAuthStore();

const conversations = ref([]);
const activeId = ref(null);
const inputText = ref('');
const sending = ref(false);
const messagesContainer = ref(null);
const inputRef = ref(null);
const showMobileSidebar = ref(false);

// Local agent AI WebSocket: streams the local Claude Code / Codex headless run
// token-by-token and carries the running -> done status transitions.
let aiSocket = null;
let aiSocketPromise = null;
const handledApprovalIds = new Set();

const activeConversation = computed(() =>
  conversations.value.find(c => c.id === activeId.value) || null
);

const activeMessages = computed(() =>
  activeConversation.value?.messages || []
);

const activeApprovals = computed(() =>
  (activeConversation.value?.approvals || []).filter(a => a.status === 'pending' || a.status === 'responding')
);

// True while the active conversation's latest assistant message is still streaming.
const activeRunning = computed(() => {
  const msgs = activeMessages.value;
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') return msgs[i].status === 'running';
  }
  return false;
});

function generateId() {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

function now() {
  return Date.now();
}

const groupedConversations = computed(() => {
  const todayStart = new Date();
  todayStart.setHours(0, 0, 0, 0);
  const todayTs = todayStart.getTime();

  const yesterdayStart = new Date(todayStart);
  yesterdayStart.setDate(yesterdayStart.getDate() - 1);
  const yesterdayTs = yesterdayStart.getTime();

  const today = [];
  const yesterday = [];
  const older = [];

  const sorted = [...conversations.value].sort((a, b) => b.updatedAt - a.updatedAt);
  for (const c of sorted) {
    if (c.updatedAt >= todayTs) {
      today.push(c);
    } else if (c.updatedAt >= yesterdayTs) {
      yesterday.push(c);
    } else {
      older.push(c);
    }
  }
  return { today, yesterday, older };
});

function loadFromStorage() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    if (data && Array.isArray(data.conversations)) {
      conversations.value = data.conversations.map(normalizeConversation).filter(Boolean);
      activeId.value = data.activeConversationId || null;
    }
  } catch (_) {
    // ignore corrupt data
  }
}

function saveToStorage() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      conversations: conversations.value.map(conversationForStorage),
      activeConversationId: activeId.value
    }));
  } catch (_) {
    // storage full or unavailable
  }
}

function conversationForStorage(conv) {
  const copy = { ...conv };
  copy.approvals = [];
  return copy;
}

function createConversation() {
  const conv = {
    id: generateId(),
    title: '',
    messages: [],
    approvals: [],
    createdAt: now(),
    updatedAt: now()
  };
  conversations.value.unshift(conv);
  activeId.value = conv.id;
  showMobileSidebar.value = false;
  saveToStorage();
  nextTick(() => inputRef.value?.focus());
}

function selectConversation(id) {
  activeId.value = id;
  showMobileSidebar.value = false;
  saveToStorage();
  nextTick(() => scrollToBottom());
}

function deleteConversation() {
  if (!activeId.value) return;
  if (!confirm(t('chat_deleteConfirm'))) return;
  conversations.value = conversations.value.filter(c => c.id !== activeId.value);
  if (conversations.value.length > 0) {
    activeId.value = conversations.value[0].id;
  } else {
    activeId.value = null;
  }
  saveToStorage();
}

function scrollToBottom() {
  nextTick(() => {
    if (messagesContainer.value) {
      messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight;
    }
  });
}

// --- Local agent AI streaming over WebSocket ---

function aiSocketUrl() {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/agent/ai/stream`;
}

function ensureAISocket() {
  if (aiSocket && aiSocket.readyState === WebSocket.OPEN) return Promise.resolve();
  if (aiSocket && aiSocket.readyState === WebSocket.CONNECTING && aiSocketPromise) return aiSocketPromise;

  aiSocketPromise = new Promise((resolve, reject) => {
    let sock;
    try {
      sock = new WebSocket(aiSocketUrl());
    } catch (e) {
      aiSocketPromise = null;
      reject(e);
      return;
    }
    aiSocket = sock;
    sock.onopen = () => {
      console.log('[aliang-chat] ws open', aiSocketUrl());
      aiSocketPromise = null;
      resolve();
    };
    sock.onmessage = (ev) => {
      let parsed = null;
      try {
        parsed = JSON.parse(ev.data);
      } catch (_) {
        // ignore malformed frames
      }
      if (parsed && parsed.type) {
        console.log('[aliang-chat] frame', parsed.type, parsed.type === 'ai.delta' ? `len=${(parsed.delta || '').length}` : '');
      }
      handleAIEvent(parsed);
    };
    sock.onerror = (e) => {
      console.warn('[aliang-chat] ws error', e);
    };
    sock.onclose = (e) => {
      console.log('[aliang-chat] ws close code=', e.code, 'reason=', e.reason);
      if (aiSocket === sock) aiSocket = null;
      aiSocketPromise = null;
    };
  });
  return aiSocketPromise;
}

function sendAI(payload) {
  if (aiSocket && aiSocket.readyState === WebSocket.OPEN) {
    aiSocket.send(JSON.stringify(payload));
    return true;
  }
  return false;
}

function findRunningAssistant(conv) {
  const msgs = conv.messages || [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
  }
  return null;
}

// Returns the live assistant bubble for a streaming run, creating a fresh
// placeholder if sendMessage's placeholder was lost (e.g. page reloaded mid-run
// holding a stale session id, or a state desync). Never spawns a stray bubble
// after the turn already finished (done/error) — late deltas are then ignored.
function ensureAssistantBubble(conv) {
  const asst = findRunningAssistant(conv);
  if (asst) return asst;
  const msgs = conv.messages || [];
  const last = msgs[msgs.length - 1];
  if (last && last.role === 'assistant' && (last.status === 'done' || last.status === 'error')) {
    return null;
  }
  const fresh = { role: 'assistant', content: '', status: 'running', ts: now() };
  conv.messages.push(fresh);
  return fresh;
}

function normalizeConversation(conv) {
  if (!conv || typeof conv !== 'object') return null;
  if (!Array.isArray(conv.messages)) conv.messages = [];
  conv.approvals = [];
  return conv;
}

function approvalKeyFromValues(sessionId, approvalId) {
  return `${sessionId || ''}:${approvalId || ''}`;
}

function approvalKey(approval) {
  return approvalKeyFromValues(approval?.sessionId, approval?.id);
}

function ensureApprovalList(conv) {
  if (!Array.isArray(conv.approvals)) conv.approvals = [];
  return conv.approvals;
}

function normalizeApprovalDecision(value) {
  const lower = String(value || '').replace(/[-\s]/g, '_').toLowerCase();
  if (['accept', 'accepted', 'approve', 'approved', 'allow', 'allowed', 'yes'].includes(lower)) return 'accept';
  if (['accept_for_session', 'acceptforsession', 'approved_for_session', 'approve_for_session', 'allow_for_session'].includes(lower)) return 'accept_for_session';
  if (['decline', 'declined', 'deny', 'denied', 'reject', 'rejected', 'no'].includes(lower)) return 'decline';
  if (['cancel', 'abort', 'timed_out', 'timeout'].includes(lower)) return 'cancel';
  return '';
}

function normalizeApprovalDecisions(values) {
  const input = Array.isArray(values) ? values : [];
  const out = [];
  for (const item of input) {
    const normalized = normalizeApprovalDecision(item);
    if (normalized && !out.includes(normalized)) out.push(normalized);
  }
  return out.length ? out : ['accept', 'decline', 'cancel'];
}

function removeApproval(conv, key) {
  if (!conv || !key || !Array.isArray(conv.approvals)) return;
  conv.approvals = conv.approvals.filter(item => item.key !== key);
}

function markApprovalHandled(conv, key) {
  if (!key) return;
  handledApprovalIds.add(key);
  removeApproval(conv, key);
}

function clearApprovalsForSession(conv, sessionId) {
  if (!conv || !Array.isArray(conv.approvals)) return;
  const keep = [];
  for (const item of conv.approvals) {
    if (!sessionId || item.sessionId === sessionId) {
      handledApprovalIds.add(item.key);
    } else {
      keep.push(item);
    }
  }
  conv.approvals = keep;
}

function upsertApproval(conv, event) {
  const id = event.approval_id || event.id;
  const sessionId = event.session_id || conv.aiSessionId || '';
  const key = approvalKeyFromValues(sessionId, id);
  if (!id || handledApprovalIds.has(key)) return;
  if (event.status && event.status !== 'pending') {
    markApprovalHandled(conv, key);
    return;
  }

  const approvals = ensureApprovalList(conv);
  const existing = approvals.find(item => item.key === key);
  const next = {
    ...(existing || {}),
    key,
    id,
    sessionId,
    messageId: event.message_id || existing?.messageId || '',
    provider: event.provider || existing?.provider || '',
    kind: event.kind || existing?.kind || 'tool',
    status: existing?.status === 'responding' ? 'responding' : 'pending',
    title: event.title || existing?.title || '',
    reason: event.reason || existing?.reason || '',
    command: event.command || existing?.command || '',
    cwd: event.cwd || existing?.cwd || '',
    toolName: event.tool_name || event.toolName || existing?.toolName || '',
    toolInput: event.tool_input ?? event.toolInput ?? existing?.toolInput ?? null,
    fileChanges: event.file_changes ?? event.fileChanges ?? existing?.fileChanges ?? null,
    availableDecisions: normalizeApprovalDecisions(event.available_decisions || event.availableDecisions || existing?.availableDecisions),
    error: '',
    updatedAt: now()
  };
  if (existing) {
    Object.assign(existing, next);
  } else {
    approvals.push(next);
  }
  conv.updatedAt = now();
  scrollToBottom();
}

function serializeApprovalValue(value) {
  if (value === null || value === undefined || value === '') return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return String(value);
  }
}

function approvalDetailText(approval) {
  return serializeApprovalValue(approval?.toolInput) || serializeApprovalValue(approval?.fileChanges);
}

function approvalAllows(approval, decision) {
  const normalized = normalizeApprovalDecision(decision);
  return normalizeApprovalDecisions(approval?.availableDecisions).includes(normalized);
}

function approvalIcon(approval) {
  switch (approval?.kind) {
    case 'command':
      return 'terminal';
    case 'file_change':
      return 'edit_document';
    case 'permissions':
      return 'lock';
    default:
      return 'rule';
  }
}

async function respondApproval(conv, approval, decision) {
  if (!conv || !approval || approval.status !== 'pending') return;
  const normalized = normalizeApprovalDecision(decision);
  if (!normalized) return;

  const key = approvalKey(approval);
  if (handledApprovalIds.has(key)) {
    removeApproval(conv, key);
    return;
  }

  approval.status = 'responding';
  approval.error = '';
  approval.updatedAt = now();
  saveToStorage();

  try {
    await ensureAISocket();
    const ok = sendAI({
      type: 'ai.approval.response',
      session_id: approval.sessionId || conv.aiSessionId,
      message_id: approval.messageId || '',
      approval_id: approval.id,
      decision: normalized,
      scope: normalized === 'accept_for_session' ? 'session' : 'turn'
    });
    if (!ok) throw new Error('socket-closed');
    markApprovalHandled(conv, key);
    saveToStorage();
  } catch (_) {
    if (!handledApprovalIds.has(key)) {
      approval.status = 'pending';
      approval.error = t('chat_approvalSendFailed');
      approval.updatedAt = now();
      saveToStorage();
    }
  }
}

function finishRun(conv) {
  conv.updatedAt = now();
  if (activeId.value === conv.id) sending.value = false;
  saveToStorage();
}

function handleAIEvent(e) {
  if (!e || !e.type) return;

  let conv = e.session_id ? conversations.value.find(c => c.aiSessionId === e.session_id) : null;
  // Fallback: a run started from this page always targets the active
  // conversation's live assistant bubble. If the session id didn't match
  // (e.g. reloaded localStorage holding a stale id, or an id mismatch), route
  // to the active conversation so the stream still renders instead of being
  // silently dropped.
  if (!conv && activeConversation.value) {
    conv = activeConversation.value;
  }
  if (!conv) {
    console.debug('[chat] ai event has no target conversation', e);
    return;
  }

  // Lightweight trace so a streaming/render issue can be localized from the
  // browser console (event type + session match). Delta text is intentionally
  // omitted to keep the log readable.
  console.log('[aliang-chat] ai event', e.type, 'session=', e.session_id, 'conv=', conv.id);

  let persist = false;
  switch (e.type) {
    case 'ai.session.created':
      conv.aiReady = true;
      persist = true;
      break;
    case 'ai.run.started': {
      const asst = ensureAssistantBubble(conv);
      if (asst) asst.status = 'running';
      break;
    }
    case 'ai.delta': {
      const asst = ensureAssistantBubble(conv);
      if (!asst) break;
      asst.content += e.delta || '';
      console.log('[aliang-chat] rendered delta, contentLen=', asst.content.length, 'conv=', conv.id);
      scrollToBottom();
      break;
    }
    case 'ai.status': {
      const asst = findRunningAssistant(conv);
      if (asst && !asst.content && e.status) asst.content = e.status;
      persist = true;
      break;
    }
    case 'ai.approval.request': {
      upsertApproval(conv, e);
      persist = true;
      break;
    }
    case 'ai.done': {
      const asst = findRunningAssistant(conv);
      if (asst) asst.status = 'done';
      clearApprovalsForSession(conv, e.session_id);
      finishRun(conv);
      return;
    }
    case 'ai.error': {
      const asst = findRunningAssistant(conv);
      if (asst) {
        asst.status = 'error';
        if (!asst.content) asst.content = e.error || t('chat_aiError');
      }
      clearApprovalsForSession(conv, e.session_id);
      finishRun(conv);
      return;
    }
    default:
      return;
  }
  if (persist) saveToStorage();
}

async function sendMessage() {
  if (!isAuthenticated.value) {
    if (activeConversation.value) {
      activeConversation.value.messages.push({ role: 'assistant', content: t('chat_loginRequired'), status: 'done', ts: now() });
    }
    return;
  }

  const text = inputText.value.trim();
  if (!text || sending.value || !activeId.value) return;

  const conv = activeConversation.value;
  if (!conv) return;

  conv.messages.push({ role: 'user', content: text, ts: now() });
  if (!conv.title) {
    conv.title = text.slice(0, 20);
  }
  conv.updatedAt = now();
  inputText.value = '';
  saveToStorage();
  scrollToBottom();

  // Live assistant placeholder: its content fills in token-by-token as
  // ai.delta frames arrive, and its status drives the running -> done UI.
  const assistantMsg = { role: 'assistant', content: '', status: 'running', ts: now() };
  conv.messages.push(assistantMsg);
  scrollToBottom();

  sending.value = true;
  if (!conv.aiSessionId) conv.aiSessionId = 'local-' + conv.id;
  const messageId = generateId();

  try {
    await ensureAISocket();
    if (!conv.aiSessionCreated) {
      sendAI({
        type: 'ai.session.create',
        session_id: conv.aiSessionId,
        project_path: conv.projectPath || '',
        provider: conv.provider || 'claudecode',
        mode: 'vibe',
      });
      conv.aiSessionCreated = true;
    }
    const ok = sendAI({
      type: 'ai.message',
      session_id: conv.aiSessionId,
      message_id: messageId,
      content: text,
    });
    if (!ok) throw new Error('socket-closed');
  } catch (_) {
    const asst = findRunningAssistant(conv);
    if (asst) {
      asst.status = 'error';
      asst.content = t('chat_serviceUnavailable');
    }
    sending.value = false;
    saveToStorage();
  }
}

function close() {
  showDashboard();
}

function handleKeydown(e) {
  if (e.key === 'Escape') {
    close();
  }
}

onMounted(() => {
  console.log('[aliang-chat] ChatPage mounted build=stream-v3 ws=' + aiSocketUrl());
  loadFromStorage();
  document.addEventListener('keydown', handleKeydown);
});

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown);
  if (aiSocket) {
    try { aiSocket.close(); } catch (_) {}
    aiSocket = null;
  }
});
</script>

<style scoped>
.ai-thinking {
  display: inline-flex;
  align-items: center;
}

.thinking-dot {
  display: inline-block;
  width: 5px;
  height: 5px;
  margin: 0 1px;
  border-radius: 9999px;
  background-color: rgb(148 163 184);
  animation: thinking-bounce 1.2s infinite ease-in-out both;
}

.thinking-dot:nth-child(2) { animation-delay: 0.15s; }
.thinking-dot:nth-child(3) { animation-delay: 0.3s; }

@keyframes thinking-bounce {
  0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
  40% { transform: scale(1); opacity: 1; }
}
</style>
