<template>
  <div
    class="fixed inset-0 z-[1080] flex items-end justify-center bg-slate-950/65 backdrop-blur-sm sm:items-center sm:p-4"
    role="dialog"
    aria-modal="true"
    :aria-label="t('listener_panelTitle')"
    @click.self="requestClose"
  >
    <div
      class="flex h-[85dvh] w-full max-w-2xl flex-col overflow-hidden rounded-t-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900 sm:h-[80vh] sm:max-h-[80dvh] sm:rounded-2xl"
    >
      <div class="flex shrink-0 items-center justify-between gap-3 border-b border-slate-200 bg-slate-50/85 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/65">
        <div class="flex min-w-0 items-center gap-2">
          <span class="material-symbols-outlined text-lg text-primary">notifications</span>
          <h3 class="truncate text-sm font-bold text-slate-900 dark:text-slate-100">{{ t('listener_panelTitle') }}</h3>
          <span class="shrink-0 rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">{{ listeners.length }}</span>
        </div>
        <div class="flex shrink-0 items-center gap-1">
          <button
            type="button"
            class="inline-flex h-11 items-center gap-1 rounded-lg px-2.5 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 disabled:opacity-60 dark:text-slate-300 dark:hover:bg-slate-800"
            :disabled="loading"
            @click="loadListeners"
          >
            <span class="material-symbols-outlined text-base" :class="{ 'motion-safe:animate-spin': loading }">refresh</span>
            {{ t('listener_refresh') }}
          </button>
          <button
            type="button"
            class="flex h-11 w-11 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            :aria-label="t('listener_close')"
            @click="requestClose"
          >
            <span class="material-symbols-outlined text-lg">close</span>
          </button>
        </div>
      </div>

      <!-- 云端离线=预期态:专用横幅,不走全局报错弹窗(Task 16 拍板) -->
      <div
        v-if="cloudOffline"
        role="status"
        class="shrink-0 border-b border-amber-200 bg-amber-50 px-4 py-2.5 text-xs font-medium text-amber-800 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-200"
      >
        <span class="material-symbols-outlined align-[-0.2em] text-sm">cloud_off</span>
        {{ t('listener_cloudOffline') }}
      </div>

      <div class="min-h-0 flex-1 overflow-y-auto">
        <div v-if="loading && !hasLoaded" class="flex min-h-36 items-center justify-center text-xs text-slate-400">
          {{ t('listener_loading') }}
        </div>
        <div v-else-if="!listeners.length" class="flex min-h-36 flex-col items-center justify-center px-5 text-center">
          <span class="material-symbols-outlined text-2xl text-slate-300 dark:text-slate-600">notifications</span>
          <p class="mt-2 text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('listener_emptyTitle') }}</p>
          <p class="mt-1 text-[10px] leading-4 text-slate-400">{{ t('listener_emptyHint') }}</p>
        </div>
        <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800">
          <li v-for="row in listeners" :key="row.id" class="px-4 py-3" :data-listener-id="row.id">
            <div class="flex items-start gap-2">
              <button
                type="button"
                class="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-300"
                :aria-expanded="expandedId === row.id"
                :aria-label="t('listener_eventsTitle')"
                @click.stop="toggleRow(row)"
              >
                <span class="material-symbols-outlined text-lg transition-transform" :class="expandedId === row.id ? 'rotate-180' : ''">expand_more</span>
              </button>
              <div class="min-w-0 flex-1 cursor-pointer" @click="toggleRow(row)">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span
                    class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
                    :class="listenerToneClass(badgeOf(row.status).tone)"
                  >{{ t(badgeOf(row.status).key) }}</span>
                  <p class="min-w-0 flex-1 truncate text-xs font-bold text-slate-800 dark:text-slate-100">{{ rowView(row).title }}</p>
                </div>
                <p class="mt-1 break-words text-[10px] leading-4 text-slate-500 dark:text-slate-400">{{ rowView(row).statusLine }}</p>
                <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10px] text-slate-400">
                  <span>{{ t('listener_colLastFired') }}: {{ rowView(row).lastFiredText }}</span>
                  <span aria-hidden="true">·</span>
                  <span>{{ t('listener_colTimes') }}: {{ Number(row.fired_count || 0) }}</span>
                </div>
              </div>
              <div class="flex shrink-0 flex-col items-end gap-1.5">
                <button
                  v-if="row.status === 'error'"
                  type="button"
                  class="inline-flex min-h-9 items-center gap-1 rounded-lg border border-slate-200 px-2.5 text-[10px] font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
                  :disabled="actionPendingId === row.id"
                  @click.stop="resumeRow(row)"
                >
                  <span class="material-symbols-outlined text-sm">restart_alt</span>
                  {{ t('listener_actionResume') }}
                </button>
                <button
                  type="button"
                  class="inline-flex min-h-9 items-center gap-1 rounded-lg border border-slate-200 px-2.5 text-[10px] font-bold text-slate-500 transition-colors hover:border-red-300 hover:bg-red-50 hover:text-red-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-red-200 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-400 dark:hover:border-red-500/40 dark:hover:bg-red-500/10 dark:hover:text-red-300"
                  :disabled="actionPendingId === row.id"
                  @click.stop="deleteRow(row)"
                >
                  <span class="material-symbols-outlined text-sm">delete</span>
                  {{ t('listener_actionDelete') }}
                </button>
              </div>
            </div>

            <div v-if="expandedId === row.id" class="mt-2.5 border-t border-slate-100 pt-2.5 dark:border-slate-800">
              <p class="text-[10px] font-bold uppercase tracking-wider text-slate-400">{{ t('listener_eventsTitle') }}</p>
              <p v-if="eventsState(row.id).loading" class="mt-1.5 text-[10px] text-slate-400">{{ t('listener_loading') }}</p>
              <p v-else-if="eventsState(row.id).error" class="mt-1.5 break-words text-[10px] leading-4 text-rose-500 dark:text-rose-400">{{ eventsState(row.id).error }}</p>
              <p v-else-if="!eventsState(row.id).items.length" class="mt-1.5 text-[10px] text-slate-400">—</p>
              <ul v-else class="mt-1.5 space-y-1">
                <li
                  v-for="item in eventsState(row.id).items"
                  :key="item.key"
                  class="flex items-start justify-between gap-3 text-[10px] leading-4"
                >
                  <span class="min-w-0 flex-1 break-words text-slate-600 dark:text-slate-300">{{ item.summary }}</span>
                  <span class="shrink-0 text-slate-400">{{ eventTime(item.fired_at) }}</span>
                </li>
              </ul>
            </div>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script>
import { getListeners, getListenerEvents, listenerPatch, listenerDelete } from '../../services/agentApi';
import { useI18n } from '../../i18n';
import { showErrorDialog } from '../../composables/useErrorDialog';
import {
  listenerStatusBadge,
  formatListenerRow,
  formatRelativeTime,
  isListenerCloudOffline,
  normalizeListenerList,
} from '../../utils/listenerFormat';

// 徽标色调 → 样式类(与 AgentRuntimeSessions 状态徽章同款配色词表)。
const TONE_CLASSES = {
  ok: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
  warn: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
  error: 'bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-300',
  muted: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300',
};

// 纯函数导出(仓内 SFC 纯逻辑导出先例:AgentActivitySettings.matchesAgentFilter)。
export function listenerToneClass(tone) {
  return Object.prototype.hasOwnProperty.call(TONE_CLASSES, tone) ? TONE_CLASSES[tone] : TONE_CLASSES.muted;
}

// 事件载荷归一:getListenerEvents 已解包为数组,此处再兜一层(缺值/非数组
// → [])并截最近 20 条(云端按时间倒序返回,slice(0,20)=最近 20)。key 用
// fingerprint+序号防同指纹去重误伤。
export function normalizeListenerEvents(data) {
  const source = Array.isArray(data) ? data : [];
  return source.slice(0, 20).map((item, index) => ({
    key: `${item?.listener_id || ''}-${item?.fingerprint || ''}-${index}`,
    summary: typeof item?.summary === 'string' ? item.summary : '',
    fired_at: typeof item?.fired_at === 'string' ? item.fired_at : '',
  }));
}

// Task 17 刷新钩子（纯函数导出供单测）：useAliangChat 收 listener.triggered
// 后派发窗事件 listener-panel-refresh（aliang:tun-progress-* 同惯例），面板
// 在 mounted/beforeUnmount 挂/摘监听。决策语义——①列表恒重载（本面板 5s
// 轮询之上的实时层）；②仅当前展开行恰为命中监听器时重拉其事件区（错位行
// 不打扰，toggleRow 的懒加载缓存语义保持）。畸形 detail（缺 listener_id）
// 零动作。
export function resolveListenerPanelRefresh(detail, expandedId) {
  const listenerId = typeof detail?.listener_id === 'string' ? detail.listener_id : '';
  return {
    reloadList: !!listenerId,
    reloadEventsId: listenerId && listenerId === expandedId ? listenerId : '',
  };
}

export default {
  name: 'ListenerSettings',
  emits: ['close'],
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      listeners: [],
      loading: false,
      hasLoaded: false,
      cloudOffline: false,
      pollTimer: null,
      expandedId: '',
      eventsById: {},
      eventsLoadSeq: {}, // id → 递增请求令牌(过期响应守卫,M3)
      actionPendingId: '',
    };
  },
  mounted() {
    this.loadListeners();
    this.pollTimer = window.setInterval(() => {
      if (document.visibilityState === 'visible') this.loadListeners();
    }, 5000);
    // Task 17 实时刷新层：useAliangChat 的 listener.triggered 轻通知同帧派发
    // 窗事件（5s 轮询兜底之上的即时路径），摘挂在 beforeUnmount 对称成对。
    window.addEventListener('listener-panel-refresh', this.onPanelRefresh);
  },
  beforeUnmount() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
    window.removeEventListener('listener-panel-refresh', this.onPanelRefresh);
  },
  methods: {
    async loadListeners() {
      if (this.loading) return;
      this.loading = true;
      try {
        const data = await getListeners(); // agentApi 已解包为数组
        this.listeners = normalizeListenerList(data);
        this.cloudOffline = false; // 轮询自愈:任一次成功即撤离线横幅
      } catch (error) {
        if (isListenerCloudOffline(error)) {
          // 云端离线=预期态:亮专用横幅、保留上一份列表,不弹全局错误框
          this.cloudOffline = true;
        } else {
          this.cloudOffline = false;
          showErrorDialog(this.t('listener_loadFailed', { msg: error?.message || '' }));
        }
      } finally {
        this.hasLoaded = true;
        this.loading = false;
      }
    },
    badgeOf(status) {
      return listenerStatusBadge(status);
    },
    rowView(row) {
      return formatListenerRow(row, this.t);
    },
    eventTime(iso) {
      return formatRelativeTime(iso, undefined, this.t) || '—';
    },
    eventsState(id) {
      return this.eventsById[id] || { loading: false, error: '', items: [] };
    },
    toggleRow(row) {
      const id = row?.id || '';
      if (this.expandedId === id) {
        this.expandedId = '';
        return;
      }
      this.expandedId = id;
      // 懒加载 + 失败重试:首次展开拉取;上次失败的缓存再次展开时重拉
      // (成功缓存不冲刷——列表轮询不打扰已展开的事件区)。
      if (id && (!this.eventsById[id] || this.eventsById[id].error)) this.loadEvents(id);
    },
    async loadEvents(id) {
      // 懒加载:首次展开(或失败重试)才拉;之后用缓存,列表轮询不冲刷已展开
      // 的事件区。过期响应守卫(fetchScrollback 的 requested!==current 同款),
      // 令牌用原始 number 而非对象身份——对象存进 reactive data 会读回 Proxy,
      // 身份比较恒假(Vue3 reactive 陷阱),原始值经 Proxy 透传才稳。
      const seq = (this.eventsLoadSeq[id] || 0) + 1;
      this.eventsLoadSeq[id] = seq;
      this.eventsById[id] = { loading: true, error: '', items: [] };
      try {
        const data = await getListenerEvents(id); // agentApi 已解包为数组
        if (this.eventsLoadSeq[id] !== seq) return; // 同 id 已重开新请求:旧响应丢弃
        this.eventsById[id] = { loading: false, error: '', items: normalizeListenerEvents(data) };
      } catch (error) {
        if (this.eventsLoadSeq[id] !== seq) return;
        if (isListenerCloudOffline(error)) this.cloudOffline = true; // 面内横幅统一表达,不重复弹窗
        this.eventsById[id] = {
          loading: false,
          error: this.t('listener_loadFailed', { msg: error?.message || '' }),
          items: [],
        };
      }
    },
    async resumeRow(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      try {
        // PATCH 透传契约:updated_at 是云端 LWW 仲裁锚(Task 11 handler 注释),
        // 复活随行携带;行上缺字段时自然省略。
        const payload = { status: 'active' };
        if (row.updated_at) payload.updated_at = row.updated_at;
        await listenerPatch(row.id, payload);
        await this.loadListeners();
      } catch (error) {
        if (!isListenerCloudOffline(error)) {
          showErrorDialog(this.t('listener_resumeFailed', { msg: error?.message || '' }));
        } else {
          this.cloudOffline = true;
        }
      } finally {
        this.actionPendingId = '';
      }
    },
    async deleteRow(row) {
      if (this.actionPendingId) return;
      if (!window.confirm(this.t('listener_confirmDelete'))) return;
      this.actionPendingId = row.id;
      try {
        await listenerDelete(row.id);
        if (this.expandedId === row.id) this.expandedId = '';
        await this.loadListeners();
      } catch (error) {
        if (!isListenerCloudOffline(error)) {
          showErrorDialog(this.t('listener_deleteFailed', { msg: error?.message || '' }));
        } else {
          this.cloudOffline = true;
        }
      } finally {
        this.actionPendingId = '';
      }
    },
    requestClose() {
      this.$emit('close');
    },
    onPanelRefresh(event) {
      const { reloadList, reloadEventsId } = resolveListenerPanelRefresh(event?.detail, this.expandedId);
      if (reloadList) this.loadListeners();
      if (reloadEventsId) this.loadEvents(reloadEventsId);
    },
  },
};
</script>
