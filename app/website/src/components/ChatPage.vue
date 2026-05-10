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
            <div class="mb-1 text-xs text-slate-400">{{ msg.role === 'user' ? t('dash_me') : t('dash_ai') }}</div>
            <div
              class="inline-block max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm"
              :class="msg.role === 'user'
                ? 'ml-auto block bg-primary text-white'
                : 'bg-white text-slate-700 dark:bg-slate-700 dark:text-slate-100'"
            >{{ msg.content }}</div>
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
import { ref, computed, watch, nextTick, onMounted } from 'vue';
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

const activeConversation = computed(() =>
  conversations.value.find(c => c.id === activeId.value) || null
);

const activeMessages = computed(() =>
  activeConversation.value?.messages || []
);

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
      conversations.value = data.conversations;
      activeId.value = data.activeConversationId || null;
    }
  } catch (_) {
    // ignore corrupt data
  }
}

function saveToStorage() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      conversations: conversations.value,
      activeConversationId: activeId.value
    }));
  } catch (_) {
    // storage full or unavailable
  }
}

function createConversation() {
  const conv = {
    id: generateId(),
    title: '',
    messages: [],
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

async function sendMessage() {
  if (!isAuthenticated.value) {
    if (activeConversation.value) {
      activeConversation.value.messages.push({ role: 'assistant', content: t('chat_loginRequired'), ts: now() });
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

  sending.value = true;
  try {
    const historyPayload = conv.messages.slice(-20).map(m => ({ role: m.role, content: m.content }));

    const response = await fetch('/api/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: text, history: historyPayload })
    });

    if (!response.ok) throw new Error(`HTTP ${response.status}`);

    const data = await response.json();
    const reply = data?.data?.reply;
    if (!reply || !String(reply).trim()) throw new Error('Empty AI response');

    conv.messages.push({ role: 'assistant', content: String(reply).trim(), ts: now() });
  } catch (_) {
    conv.messages.push({ role: 'assistant', content: t('chat_serviceUnavailable'), ts: now() });
  } finally {
    conv.updatedAt = now();
    sending.value = false;
    saveToStorage();
    scrollToBottom();
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
  loadFromStorage();
  document.addEventListener('keydown', handleKeydown);
});

import { onUnmounted } from 'vue';
onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown);
});
</script>
