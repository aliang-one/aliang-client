<template>
  <!-- Mobile: conversation list overlay -->
  <div
    v-if="showMobileSidebar"
    class="fixed inset-0 z-10 bg-black/50 md:hidden"
    @click="emit('mobile-close')"
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
        @click="emit('create')"
      >
        <span class="material-symbols-outlined text-lg">add</span>
      </button>
    </div>

    <div class="flex-1 overflow-y-auto py-1">
      <!-- 三源统一清单（本地草稿 + 设备会话）：分组结构一致，单段循环渲染。
           设备会话条目带来源角标与 running 状态点；点击分流 openDeviceSession。 -->
      <template v-for="group in groups" :key="group.key">
        <template v-if="group.items.length">
          <div class="px-5 pt-3 pb-1 text-[11px] font-semibold text-slate-400 uppercase tracking-wider">{{ group.label }}</div>
          <!-- active 竖条依赖同插件值序：基类 before:opacity-0 兜底非活跃态，
               活跃态 before:opacity-100 按 opacity 插件值序（0 → 100）在生成
               样式表中居后胜出——:class 分支里不再重复写 before:opacity-0。 -->
          <div
            v-for="c in group.items"
            :key="c.id"
            role="button"
            tabindex="0"
            class="group relative mx-2 cursor-pointer rounded-lg px-3 py-2 text-left text-sm transition-colors before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:rounded-full before:bg-primary before:content-[''] before:opacity-0 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            :class="c.id === activeId
              ? 'bg-primary/10 font-medium text-primary before:opacity-100'
              : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="emit('item-click', c)"
            @keydown.enter.prevent="emit('item-click', c)"
            @keydown.space.prevent="emit('item-click', c)"
          >
            <input
              v-if="renamingId === c.id && renameSurface === 'list'"
              v-model="renameModel"
              :ref="setRenameInput"
              type="text"
              maxlength="60"
              :placeholder="t('chat_renamePlaceholder')"
              class="w-full rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
              @click.stop
              @keydown.enter="onRenameEnter"
              @keydown.esc="onRenameEscape"
              @keydown.space.stop
              @blur="emit('rename-commit')"
            />
            <div v-else class="flex items-center gap-2">
              <div class="min-w-0 flex-1 truncate">{{ c.title || t('chat_emptyTitle') }}</div>
              <span
                v-if="c.deviceSession && sourceBadgeLabel(c.deviceSession)"
                data-testid="session-source-badge"
                class="shrink-0 rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-slate-500 dark:bg-slate-700 dark:text-slate-300"
              >{{ sourceBadgeLabel(c.deviceSession) }}</span>
              <span
                v-if="conversationRunning(c)"
                data-testid="device-session-running-dot"
                class="inline-block h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-primary"
                :title="t('chat_statusRunning')"
              ></span>
              <!-- 设备会话不可在本端重命名/删除（后端无端点，title 由清单权威） -->
              <button
                v-if="!c.deviceSession"
                type="button"
                class="relative shrink-0 rounded p-1 text-slate-400 opacity-100 transition-opacity hover:bg-slate-200 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100 dark:hover:bg-slate-700 before:absolute before:-inset-2 before:content-['']"
                :aria-label="`${t('chat_rename')} ${c.title || t('chat_emptyTitle')}`"
                @click.stop="emit('rename-start', c, 'list')"
                @keydown.enter.stop
                @keydown.space.stop
              >
                <span class="material-symbols-outlined text-base">edit</span>
              </button>
            </div>
          </div>
        </template>
      </template>
      <div v-if="mergedCount === 0" class="px-4 py-8 text-center text-sm text-slate-400">
        {{ deviceAvailable ? t('chat_deviceSessionsEmpty') : t('chat_deviceSessionsLoadFailed') }}
      </div>
    </div>
  </aside>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';
import { isComposingEvent } from '../../composables/useAliangChat';
import { sessionSourceBadge } from '../../utils/chatSettings';

const props = defineProps({
  groups: { type: Array, default: () => [] },
  mergedCount: { type: Number, default: 0 },
  activeId: { type: String, default: null },
  showMobileSidebar: { type: Boolean, default: false },
  renameDraft: { type: String, default: '' },
  renamingId: { type: String, default: '' },
  renameSurface: { type: String, default: '' },
  deviceAvailable: { type: Boolean, default: true },
  // 重命名输入框元素交接：输入框在本组件渲染，焦点/选区仍由 useAliangChat
  // 的 startRename 在 nextTick 里驱动（与拆分前的函数 ref 同一套时序）。
  setRenameInput: { type: Function, required: true },
});

const emit = defineEmits([
  'mobile-close',
  'item-click',
  'create',
  'rename-start',
  'rename-commit',
  'rename-cancel',
  'rename-draft',
]);

const { t } = useI18n();

// v-model 接线：draft 状态在 useAliangChat（列表与顶栏共享同一份），本组件
// 只经 rename-draft 事件回写。经 computed 承接原生 v-model，保留 Vue 对 IME
// 组合期的守卫语义（组合期间不回写，compositionend 才提交）。
const renameModel = computed({
  get: () => props.renameDraft,
  set: (v) => emit('rename-draft', v),
});

function onRenameEnter(e) {
  // 组合期既不 preventDefault（会吃掉输入法的确认候选词），
  // 也不能省 stopPropagation（会冒泡到行级 selectConversation / 文档级 close）。
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  emit('rename-commit');
}

function onRenameEscape(e) {
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  emit('rename-cancel');
}

// 来源角标：sessionSourceBadge 返回逻辑键（active/phone/dashboard/terminal），
// 展示文案在此映射为 chat_source_* 键（逻辑键与文案解耦，单元测试可钉逻辑键）。
function sourceBadgeLabel(session) {
  const key = sessionSourceBadge(session);
  return key ? t(`chat_source_${key}`) : '';
}

// running 状态点判定推广（后台静默运行可见性，2026-11-04）：设备壳走清单/
// 快照 status（c.running），本地草稿无壳标记，从气泡状态推导——窗口关闭期间
// 照跑的会话，重进页侧栏同样亮运行点。响应式依赖 c.messages（conversations
// ref 内的响应式对象），快照对账把气泡收敛为 done 后点自动熄灭。
function conversationRunning(c) {
  if (!c) return false;
  if (c.running) return true;
  const msgs = Array.isArray(c.messages) ? c.messages : [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') return msgs[i].status === 'running';
  }
  return false;
}
</script>
