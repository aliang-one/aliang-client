<template>
  <div class="space-y-5">
    <!-- 标题区 -->
    <div class="flex items-start gap-3">
      <span class="material-symbols-outlined mt-0.5 text-2xl text-primary">tune</span>
      <div class="min-w-0">
        <h3 class="text-xl font-semibold text-slate-900 dark:text-white">{{ t('qs_cfg_title') }}</h3>
      </div>
    </div>

    <!-- base_url：预设三选一 -->
    <div class="space-y-2">
      <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_cfg_base_url') }}</p>
      <div class="grid gap-2" :class="chipGridClass">
        <button
          v-if="presetLocal"
          type="button"
          :class="presetChipClass(baseUrlMode === 'local')"
          @click="baseUrlMode = 'local'"
        >
          <span class="block truncate text-xs font-semibold">{{ t('qs_mode_local') }}</span>
          <span class="mt-0.5 block truncate font-mono text-[10px] text-slate-500 dark:text-slate-400" :title="presetLocal">
            {{ presetLocal }}
          </span>
        </button>
        <button
          v-if="presetPublic"
          type="button"
          :class="presetChipClass(baseUrlMode === 'public')"
          @click="baseUrlMode = 'public'"
        >
          <span class="block truncate text-xs font-semibold">{{ t('qs_mode_public') }}</span>
          <span class="mt-0.5 block truncate font-mono text-[10px] text-slate-500 dark:text-slate-400" :title="presetPublic">
            {{ presetPublic }}
          </span>
        </button>
        <button
          type="button"
          :class="presetChipClass(baseUrlMode === 'custom')"
          @click="baseUrlMode = 'custom'"
        >
          <span class="block truncate text-xs font-semibold">{{ t('qs_cfg_base_url_custom') }}</span>
        </button>
      </div>
      <input
        v-if="baseUrlMode === 'custom'"
        v-model.trim="customBaseUrl"
        type="text"
        placeholder="https://"
        :aria-label="t('qs_cfg_base_url')"
        class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 font-mono text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
      />
    </div>

    <!-- api_key：手动输入 + 从密钥列表选 -->
    <div class="space-y-2">
      <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_cfg_api_key') }}</p>
      <div class="grid gap-2" :class="keyEntries.length ? 'sm:grid-cols-[minmax(0,1fr)_minmax(0,0.8fr)]' : ''">
        <input
          v-model.trim="apiKeyDraft"
          type="text"
          autocomplete="off"
          spellcheck="false"
          :aria-label="t('qs_cfg_api_key')"
          class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 font-mono text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        <select
          v-if="keyEntries.length"
          v-model="pickedKeyId"
          :aria-label="t('qs_cfg_pick_key')"
          :title="t('qs_cfg_pick_key')"
          class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        >
          <option value="" disabled>{{ t('qs_selectKeyPh') }}</option>
          <option v-for="key in keyEntries" :key="key.id" :value="String(key.id)">
            {{ key.name }} · {{ key.group?.name || t('qs_noGroup') }} ({{ key.provider }}){{ looksMaskedAPIKey(key.key) ? ' · ' + t('qs_keyMasked') : '' }}
          </option>
        </select>
      </div>
      <p v-if="maskedKeyPicked" class="text-[11px] text-amber-600 dark:text-amber-400">
        {{ t('qs_cfg_key_masked_hint') }}
      </p>
    </div>

    <!-- model -->
    <div class="space-y-2">
      <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_cfg_model') }}</p>
      <input
        v-model.trim="modelDraft"
        type="text"
        :aria-label="t('qs_cfg_model')"
        class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
      />
    </div>

    <!-- 底部按钮行 -->
    <div class="flex items-center justify-end gap-2 border-t border-slate-100 pt-4 dark:border-slate-800">
      <button
        type="button"
        class="inline-flex min-h-10 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
        @click="emit('cancel')"
      >
        {{ t('qs_cancel') }}
      </button>
      <button
        type="button"
        class="inline-flex min-h-10 items-center justify-center rounded-lg bg-primary px-4 text-xs font-semibold text-white transition hover:bg-primary/90"
        @click="confirmVariables"
      >
        {{ t('qs_cfg_confirm') }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useI18n } from '../i18n';

const props = defineProps({
  // 当前组合变量 { base_url, api_key, model }（snake_case，与后端 combo.variables 一致）
  variables: {
    type: Object,
    required: true,
  },
  // catalog softwares[].presets：{ base_url_local, base_url_public }；后端 omitempty 指针，可能为 null
  presets: {
    type: Object,
    required: true,
  },
  // catalog api_keys（供「从密钥列表选」）；key 可能是后端脱敏串
  apiKeys: {
    type: Array,
    default: () => [],
  },
});
const emit = defineEmits(['confirm', 'cancel']);
const { t } = useI18n();

const presetLocal = computed(() => String(props.presets?.base_url_local || '').trim());
const presetPublic = computed(() => String(props.presets?.base_url_public || '').trim());
const keyEntries = computed(() => (Array.isArray(props.apiKeys) ? props.apiKeys : []));

// 本地草稿态：不直接改 props，仅在 props.variables 引用变化时重置一次
// （宿主以 v-if 挂载本组件，正常生命周期内恰好在打开时初始化一次）
const baseUrlMode = ref('custom');
const customBaseUrl = ref('');
const apiKeyDraft = ref('');
const modelDraft = ref('');
const pickedKeyId = ref('');
const maskedKeyPicked = ref(false);

function initDraft() {
  const current = String(props.variables?.base_url || '').trim();
  if (presetLocal.value && current === presetLocal.value) {
    baseUrlMode.value = 'local';
  } else if (presetPublic.value && current === presetPublic.value) {
    baseUrlMode.value = 'public';
  } else {
    baseUrlMode.value = 'custom';
    customBaseUrl.value = current;
  }
  apiKeyDraft.value = String(props.variables?.api_key || '');
  modelDraft.value = String(props.variables?.model || '');
  pickedKeyId.value = '';
  maskedKeyPicked.value = false;
}
watch(() => props.variables, initDraft, { immediate: true });

const effectiveBaseUrl = computed(() => {
  if (baseUrlMode.value === 'local') {
    return presetLocal.value;
  }
  if (baseUrlMode.value === 'public') {
    return presetPublic.value;
  }
  return customBaseUrl.value.trim();
});

// 选项数决定分栏（预设缺失时自动收窄，custom 恒在）
const chipGridClass = computed(() => {
  const count = 1 + (presetLocal.value ? 1 : 0) + (presetPublic.value ? 1 : 0);
  if (count >= 3) return 'sm:grid-cols-3';
  if (count === 2) return 'sm:grid-cols-2';
  return '';
});

function presetChipClass(active) {
  return [
    'rounded-xl border px-3 py-2 text-left transition',
    active
      ? 'border-primary/40 bg-primary/10 text-slate-900 dark:bg-primary/10 dark:text-white'
      : 'border-slate-200 bg-white text-slate-700 hover:border-primary/30 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200',
  ];
}

// 镜像 QuickSetupModal.looksMaskedAPIKey 的脱敏判断（*** / ... / …），
// 与既有 apiKeyHasPlainSecret 口径一致
function looksMaskedAPIKey(value) {
  const text = String(value || '').trim();
  if (!text) return false;
  return text.includes('***') || text.includes('...') || text.includes('…');
}

watch(pickedKeyId, (id) => {
  if (!id) {
    maskedKeyPicked.value = false;
    return;
  }
  const key = keyEntries.value.find((item) => String(item?.id) === String(id));
  if (!key) {
    maskedKeyPicked.value = false;
    return;
  }
  if (looksMaskedAPIKey(key.key)) {
    // 掩码串不可用：不填充 input，提示手动填入完整值
    maskedKeyPicked.value = true;
    return;
  }
  maskedKeyPicked.value = false;
  apiKeyDraft.value = String(key.key || '');
});

function confirmVariables() {
  emit('confirm', {
    base_url: effectiveBaseUrl.value,
    api_key: apiKeyDraft.value.trim(),
    model: modelDraft.value.trim(),
  });
}
</script>
