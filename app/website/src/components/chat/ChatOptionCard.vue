<template>
  <!-- 终态（answered）：折叠为「已选择」单行卡，选项行/输入区全部退场。
       作答成功后由 useAliangChat.respondOption 置 status='answered' 并记录
       selected/customText——组件只发 respond 事件，终态由状态驱动。 -->
  <div
    v-if="card.status === 'answered'"
    data-testid="chat-option-card"
    class="mb-3 max-w-[85%] rounded-2xl border border-primary/30 bg-primary/5 px-4 py-2.5 text-sm text-slate-600 shadow-sm dark:border-primary/40 dark:bg-primary/10 dark:text-slate-300"
  >
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="material-symbols-outlined text-base leading-none text-primary">check_circle</span>
      <span class="font-semibold text-primary dark:text-primary">{{ t('chat_optionSelected') }}:</span>
      <span class="min-w-0 font-medium">{{ answeredSummary }}</span>
    </div>
  </div>
  <!-- 待答（pending/sending）：两步作答——点选项行仅本地选中高亮（不发帧），
       底部确认键才发包（emit respond 由上层发 ai.option.response）。 -->
  <div
    v-else
    data-testid="chat-option-card"
    class="mb-3 max-w-[85%] rounded-2xl border border-sky-200 bg-sky-50 px-4 py-3 text-sm text-slate-700 shadow-sm dark:border-sky-800/60 dark:bg-sky-950/40 dark:text-slate-100"
  >
    <div class="flex flex-wrap items-center gap-2">
      <span class="flex size-8 shrink-0 items-center justify-center rounded-full bg-sky-100 text-sky-600 dark:bg-sky-900/40 dark:text-sky-300">
        <span class="material-symbols-outlined text-lg">checklist</span>
      </span>
      <span class="font-semibold">{{ card.title || t('chat_optionTitle') }}</span>
      <span
        v-if="card.multi"
        class="rounded-full bg-sky-100 px-2 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-900/60 dark:text-sky-200"
      >{{ t('chat_optionMulti') }}</span>
      <span
        v-if="card.status === 'sending'"
        class="rounded-full bg-sky-100 px-2 py-0.5 text-[11px] font-medium text-sky-700 dark:bg-sky-900/60 dark:text-sky-200"
      >{{ t('chat_approvalResponding') }}</span>
    </div>
    <p
      v-if="card.sourceId"
      class="mt-1.5 text-xs font-medium text-sky-700 dark:text-sky-200"
    >{{ t('chat_approvalFromSession', { id: card.sourceId }) }}</p>
    <p
      v-if="card.summary"
      class="mt-1.5 whitespace-pre-wrap text-xs text-sky-800 dark:text-sky-200"
    >{{ card.summary }}</p>
    <div class="mt-2 space-y-1.5">
      <button
        v-for="opt in card.options"
        :key="opt.id"
        type="button"
        :role="card.multi ? 'checkbox' : 'radio'"
        :aria-checked="isSelected(opt.id) ? 'true' : 'false'"
        class="flex w-full min-h-[40px] items-start gap-2 rounded-xl border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-70 md:min-h-0"
        :class="isSelected(opt.id)
          ? 'border-primary bg-primary/10'
          : 'border-slate-200 bg-white hover:border-primary/50 dark:border-slate-700 dark:bg-slate-900'"
        :disabled="card.status === 'sending'"
        @click="toggle(opt.id)"
      >
        <span class="material-symbols-outlined mt-0.5 shrink-0 text-[16px] leading-none text-primary">
          {{ isSelected(opt.id) ? (card.multi ? 'check_box' : 'radio_button_checked') : (card.multi ? 'check_box_outline_blank' : 'radio_button_unchecked') }}
        </span>
        <span class="min-w-0">
          <span class="block font-medium">{{ opt.label }}</span>
          <span
            v-if="opt.description"
            class="mt-0.5 block text-xs text-slate-500 dark:text-slate-400"
          >{{ opt.description }}</span>
          <!-- previews 折叠块：details 自带开关语义，click.stop 防止开合预览
               顺带翻转选项选中态。 -->
          <details
            v-if="opt.previews && opt.previews.length"
            class="mt-1"
            @click.stop
          >
            <summary class="cursor-pointer text-[11px] text-slate-400 underline underline-offset-2">{{ t('chat_optionPreview') }}</summary>
            <pre
              v-for="(pv, pi) in opt.previews"
              :key="pi"
              class="mt-1 max-h-40 overflow-auto rounded-lg bg-slate-950 px-3 py-2 font-mono text-xs text-slate-100"
            >{{ pv }}</pre>
          </details>
        </span>
      </button>
    </div>
    <div v-if="card.allowCustom" class="mt-2">
      <textarea
        v-model="localCustom"
        rows="2"
        :placeholder="t('chat_optionCustom')"
        :disabled="card.status === 'sending'"
        class="min-h-[40px] w-full resize-none rounded-xl border border-slate-200 bg-white px-3 py-2 text-xs text-slate-700 outline-none placeholder:text-slate-400 focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100 dark:placeholder:text-slate-500 md:min-h-0"
      ></textarea>
    </div>
    <div class="mt-2.5">
      <button
        type="button"
        data-testid="chat-option-submit"
        class="inline-flex min-h-[40px] items-center gap-1 rounded-lg bg-primary px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60 md:min-h-0"
        :disabled="!canSubmit"
        @click="submit"
      >
        <span class="material-symbols-outlined text-sm">check</span>{{ t('chat_optionSubmit') }}
      </button>
    </div>
    <p
      v-if="card.error"
      class="mt-1 text-xs text-rose-600 dark:text-rose-300"
    >{{ card.error }}</p>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useI18n } from '../../i18n';

// 纯渲染：结构化方案交互卡（Task 14，ai.option 两步作答）。与审批卡
// （ChatApprovalCard）是兄弟不是改造：本地选中 ref 只承载高亮，提交发射
// emit('respond', {selected, customText})，由上层 useAliangChat.respondOption
// 发帧并在成功后置 card.status='answered'（终态渲染由状态驱动）。
const props = defineProps({
  card: { type: Object, required: true },
  // 时钟节拍（与审批卡 prop 面对齐预留；当前卡无倒计时字段，暂不消费）。
  nowTick: { type: Number, default: 0 },
});

const emit = defineEmits(['respond']);

const { t } = useI18n();

// 本地两步作答第一步：选中集合（单选=至多一项；再点已选项=取消选择）。
const localSelected = ref([]);
const localCustom = ref('');

function isSelected(id) {
  return localSelected.value.includes(id);
}

function toggle(id) {
  if (props.card.status !== 'pending') return;
  const idx = localSelected.value.indexOf(id);
  if (props.card.multi) {
    if (idx >= 0) localSelected.value.splice(idx, 1);
    else localSelected.value.push(id);
    return;
  }
  localSelected.value = idx >= 0 ? [] : [id];
}

// 无选中禁用提交（自定义文本只是选择的补充，不单独解锁——与 e2e/组件测
// 试钉死的禁用语义一致）。sending 期一并锁死（respondOption 置位在先，这里
// 双保险防重复发包）。
const canSubmit = computed(() =>
  props.card.status === 'pending' && localSelected.value.length > 0
);

function submit() {
  if (!canSubmit.value) return;
  emit('respond', {
    selected: [...localSelected.value],
    customText: localCustom.value.trim(),
  });
}

// 卡内容被同 key 新请求顶掉（upsert 换 options 数组）时复位本地选中——
// 新 run 的新方案不应继承旧高亮；发送失败恢复 pending（options 未换）时
// 保留已选，用户直接重试即可。
watch(
  () => props.card.options,
  () => {
    localSelected.value = [];
    localCustom.value = '';
  },
);

// 终态摘要：选中项 label（缺 label 退 id）拼接 + 自定义补充。
const answeredSummary = computed(() => {
  const labels = (props.card.selected || []).map((id) =>
    props.card.options.find(o => o.id === id)?.label || id,
  );
  const custom = (props.card.customText || '').trim();
  return custom ? `${labels.join('、')}；${custom}` : labels.join('、');
});
</script>
