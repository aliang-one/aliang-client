<template>
  <dialog
    ref="dialog"
    class="task-drawer fixed inset-y-0 left-auto right-0 m-0 h-dvh max-h-none w-full max-w-none bg-white p-0 text-slate-900 shadow-xl dark:bg-slate-900 dark:text-slate-100 md:w-[36rem]"
    data-testid="chat-operation-panel"
    aria-labelledby="task-drawer-title"
    @cancel.prevent="emit('close')"
    @click.self="emit('close')"
  >
    <div class="flex h-full min-h-0 flex-col">
      <header class="flex shrink-0 items-center gap-3 border-b border-slate-200 px-4 py-2 dark:border-slate-700">
        <h2 id="task-drawer-title" class="font-semibold">{{ t('operation_title') }}</h2>
        <span class="text-sm text-primary">{{ t('operation_activeCount', { count: activeCount }) }}</span>
        <button autofocus type="button" class="ml-auto grid min-h-11 min-w-11 place-items-center rounded-lg hover:bg-slate-100 focus-visible:ring-2 focus-visible:ring-primary dark:hover:bg-slate-800"
          :aria-label="t('operation_close')" @click="emit('close')">
          <span class="material-symbols-outlined" aria-hidden="true">close</span>
        </button>
      </header>
      <p v-if="!connected" class="shrink-0 border-b border-slate-200 px-4 py-2 text-sm text-slate-600 dark:border-slate-700 dark:text-slate-300">
        {{ t('operation_stale') }} <span v-if="syncedAt">{{ dateLabel(syncedAt) }}</span>
      </p>
      <div class="relative flex min-h-0 flex-1 flex-col md:flex-row">
        <div :class="selected ? 'hidden md:flex' : 'flex'" class="min-h-0 flex-1 flex-col md:w-60 md:flex-none md:border-r md:border-slate-200 md:dark:border-slate-700">
          <div class="grid gap-2 border-b border-slate-200 p-3 dark:border-slate-700">
            <button v-if="sessionId" type="button" class="min-h-11 text-left text-sm text-primary"
              @click="sessionId = ''; source = ''">{{ t('operation_all') }}</button>
            <button v-if="sessionId && source === 'subagent'" type="button" class="min-h-11 text-left text-sm text-primary"
              @click="emit('open-subagents')">{{ t('chat_subagentEntryView') }}</button>
            <select v-model="status" :aria-label="t('operation_statusFilter')" class="min-h-11 rounded-md border border-slate-300 bg-transparent px-2 dark:border-slate-600 dark:bg-slate-900">
              <option value="all">{{ t('operation_all') }}</option>
              <option value="active">{{ t('operation_active') }}</option>
              <option value="finished">{{ t('operation_finished') }}</option>
            </select>
            <select v-model="source" :aria-label="t('operation_typeFilter')" class="min-h-11 rounded-md border border-slate-300 bg-transparent px-2 dark:border-slate-600 dark:bg-slate-900">
              <option value="">{{ t('operation_allTypes') }}</option>
              <option v-for="kind in sources" :key="kind" :value="kind">{{ sourceLabel(kind) }}</option>
            </select>
            <select v-if="devices.length > 1" v-model="device" :aria-label="t('operation_deviceFilter')" class="min-h-11 rounded-md border border-slate-300 bg-transparent px-2 dark:border-slate-600 dark:bg-slate-900">
              <option value="">{{ t('operation_allDevices') }}</option>
              <option v-for="id in devices" :key="id" :value="id">{{ deviceLabel(id) }}</option>
            </select>
          </div>
          <div class="min-h-0 flex-1 overflow-y-auto">
            <p v-if="!visible.length" class="p-4 text-sm text-slate-500 dark:text-slate-400">{{ t('operation_empty') }}</p>
            <button v-for="item in visible" :key="item.operation_id" type="button"
              class="block min-h-11 w-full border-b border-slate-100 px-3 py-3 text-left hover:bg-slate-50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary dark:border-slate-800 dark:hover:bg-slate-800"
              :class="item.operation_id === selectedId ? 'bg-primary/10' : ''"
              :aria-pressed="item.operation_id === selectedId" @click="selectedId = item.operation_id">
              <span class="block truncate text-sm font-medium">{{ item.title }}</span>
              <span class="block truncate text-xs text-slate-600 dark:text-slate-400">
                {{ sourceLabel(item.source_kind) }} · {{ t('operation_phase_' + item.phase) }}
                <template v-if="item.source_device_id"> · {{ deviceLabel(item.source_device_id) }}</template>
              </span>
              <span v-if="elapsedById.has(item.operation_id)" class="block text-xs tabular-nums text-slate-600 dark:text-slate-400">
                {{ t('operation_elapsed', { value: elapsedById.get(item.operation_id) }) }}
              </span>
              <span v-if="item.step" class="block truncate text-xs text-slate-600 dark:text-slate-400">{{ item.step }}</span>
              <progress v-if="operationPercent(item) !== null" :value="operationPercent(item)" max="100" :aria-label="item.title"
                class="mt-2 block h-1 w-full accent-primary"></progress>
            </button>
          </div>
        </div>
        <section v-if="selected" class="min-w-0 flex-1 overflow-y-auto p-4">
          <button type="button" class="mb-3 min-h-11 rounded px-2 text-primary focus-visible:ring-2 focus-visible:ring-primary md:hidden"
            @click="selectedId = ''">{{ t('operation_back') }}</button>
          <h3 class="break-words text-lg font-semibold">{{ selected.title }}</h3>
          <p class="mt-1 text-sm font-medium text-primary">{{ t('operation_phase_' + selected.phase) }}</p>
          <p v-if="elapsedById.has(selected.operation_id)" class="mt-1 text-sm tabular-nums text-slate-600 dark:text-slate-400">
            {{ t('operation_elapsed', { value: elapsedById.get(selected.operation_id) }) }}
          </p>
          <div v-if="operationPercent(selected) !== null" class="mt-4 space-y-1">
            <progress :value="operationPercent(selected)" max="100" :aria-label="selected.title" class="block h-2 w-full accent-primary"></progress>
            <p class="text-sm tabular-nums">{{ numberLabel(operationPercent(selected)) }}% · {{ progressLabel(selected.progress) }}</p>
          </div>
          <p v-else-if="selected.progress" class="mt-4 text-sm tabular-nums">{{ progressLabel(selected.progress) }}</p>
          <p v-if="selected.step || selected.stage" class="mt-4 break-words text-sm">{{ selected.step || t('operation_stage_' + selected.stage) }}</p>
          <p v-if="selected.message" class="mt-3 whitespace-pre-wrap break-words text-sm">{{ selected.message }}</p>
          <p v-if="selected.error_code" class="mt-3 break-words text-sm text-red-600 dark:text-red-400">{{ selected.error_code }}</p>
          <p v-if="selected.rate > 0" class="mt-2 text-sm tabular-nums">
            {{ t('operation_rate', { value: numberLabel(selected.rate), unit: selected.progress?.unit ? t('operation_unit_' + selected.progress.unit) : '' }) }}
            <span v-if="selected.eta_seconds != null"> · {{ t('operation_eta', { value: durationLabel(selected.eta_seconds) }) }}</span>
          </p>
          <dl class="mt-4 space-y-3 border-t border-slate-200 pt-4 text-sm dark:border-slate-700">
            <div><dt class="text-slate-500">{{ t('operation_typeFilter') }}</dt><dd>{{ sourceLabel(selected.source_kind) }}</dd></div>
            <div v-if="selected.source_device_id"><dt class="text-slate-500">{{ t('operation_deviceFilter') }}</dt><dd class="break-all">{{ deviceLabel(selected.source_device_id) }}</dd></div>
            <div v-if="selected.started_at"><dt class="text-slate-500">{{ t('operation_started') }}</dt><dd>{{ dateLabel(selected.started_at) }}</dd></div>
            <div><dt class="text-slate-500">{{ t('operation_updated') }}</dt><dd>{{ dateLabel(selected.updated_at) }}</dd></div>
            <div v-if="selected.finished_at"><dt class="text-slate-500">{{ t('operation_ended') }}</dt><dd>{{ dateLabel(selected.finished_at) }}</dd></div>
          </dl>
          <section v-if="selected.transitions?.length > 1" class="mt-4 border-t border-slate-200 pt-4 text-sm dark:border-slate-700">
            <h4 class="font-semibold">{{ t('operation_timeline') }}</h4>
            <ol class="mt-2 space-y-1 text-slate-600 dark:text-slate-400">
              <li v-for="(entry, index) in selected.transitions" :key="index">
                <span>{{ entry.stage ? t('operation_stage_' + entry.stage) : t('operation_phase_' + entry.phase) }}</span> · <time :datetime="entry.at">{{ dateLabel(entry.at) }}</time>
              </li>
            </ol>
          </section>
          <button v-if="selected.session_id && canOpenSession(selected.session_id)" type="button"
            class="mt-4 min-h-11 rounded px-2 text-primary focus-visible:ring-2 focus-visible:ring-primary"
            @click="emit('open-session', selected.session_id)">{{ t('operation_openSession') }}</button>
        </section>
        <p v-else class="hidden flex-1 p-4 text-sm text-slate-500 md:block">{{ t('operation_select') }}</p>
      </div>
    </div>
  </dialog>
</template>

<script setup>
import { computed, onMounted, onBeforeUnmount, ref, watchEffect } from 'vue';
import { useI18n } from '../../i18n';
import { isActiveOperation, operationElapsedLabel, operationPercent } from '../../utils/operationActivity';

const props = defineProps({
  operations: { type: Array, required: true },
  connected: { type: Boolean, default: false },
  syncedAt: { type: String, default: '' },
  canOpenSession: { type: Function, default: () => false },
  initialSessionId: { type: String, default: '' },
  initialSource: { type: String, default: '' },
});
const emit = defineEmits(['close', 'open-session', 'open-subagents']);
const { t } = useI18n();
const dialog = ref(null);
const status = ref('all');
const sessionId = ref(props.initialSessionId);
const source = ref(props.initialSource);
const device = ref('');
const selectedId = ref('');
const activeCount = computed(() => props.operations.filter(isActiveOperation).length);
const sources = computed(() => [...new Set(props.operations.map(row => row.source_kind))]);
const devices = computed(() => [...new Set(props.operations.map(row => row.source_device_id).filter(Boolean))]);
const visible = computed(() => props.operations.filter(row =>
  (!sessionId.value || row.session_id === sessionId.value) &&
  (status.value === 'all' || isActiveOperation(row) === (status.value === 'active')) &&
  (!source.value || source.value === row.source_kind) &&
  (!device.value || device.value === row.source_device_id)));
const selected = computed(() => visible.value.find(row => row.operation_id === selectedId.value));
const now = ref(Date.now());
const showLiveElapsed = computed(() => visible.value.some(row =>
  (row.phase === 'running' || row.phase === 'waiting') && Number.isFinite(Date.parse(row.started_at))));
watchEffect(onCleanup => {
  if (!showLiveElapsed.value) return;
  now.value = Date.now();
  const timer = setInterval(() => { now.value = Date.now(); }, 1000);
  onCleanup(() => clearInterval(timer));
});
const elapsedById = computed(() => {
  const labels = new Map();
  for (const row of visible.value) {
    const label = operationElapsedLabel(row, now.value);
    if (label !== null) labels.set(row.operation_id, label);
  }
  return labels;
});
const sourceLabel = kind => t('operation_type_' + kind);
const deviceLabel = id => props.operations.find(row => row.source_device_id === id && row.source_device_name)?.source_device_name ?? id;
const durationLabel = seconds => {
  const formatter = new Intl.NumberFormat(undefined, { style: 'unit', unit: seconds >= 60 ? 'minute' : 'second' });
  return formatter.format(seconds >= 60 ? Math.ceil(seconds / 60) : seconds);
};
const dateLabel = value => new Date(value).toLocaleString();
const numberLabel = value => new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 }).format(value);
const progressLabel = ({ current, total, unit }) =>
  [total ? `${numberLabel(current)} / ${numberLabel(total)}` : numberLabel(current), unit && t('operation_unit_' + unit)].filter(Boolean).join(' ');

onMounted(() => dialog.value?.showModal());
onBeforeUnmount(() => dialog.value?.close());
</script>

<style scoped>
.task-drawer::backdrop { background: rgb(15 23 42 / 45%); }
.task-drawer[open] { animation: drawer-in 200ms ease-out; }
@keyframes drawer-in { from { transform: translateX(100%); } to { transform: translateX(0); } }
@media (prefers-reduced-motion: reduce) { .task-drawer[open] { animation: none; } }
</style>
