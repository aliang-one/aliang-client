<template>
  <div
    v-if="open"
    class="fixed inset-0 z-[130] flex items-center justify-center p-4"
    role="dialog"
    aria-modal="true"
    aria-label="Quick Setup"
  >
    <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="emit('close')"></div>

    <div
      class="relative z-10 flex h-[720px] w-full max-w-6xl overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-800 dark:bg-slate-900"
    >
      <!-- Left sidebar -->
      <aside class="flex w-72 shrink-0 flex-col border-r border-slate-100 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-800/30">
        <div class="border-b border-slate-100 px-6 py-5 dark:border-slate-800">
          <p class="text-[11px] font-bold uppercase tracking-[0.28em] text-slate-400">{{ t('qs_title') }}</p>
          <h3 class="mt-2 text-lg font-semibold text-slate-900 dark:text-white">{{ t('qs_presetTemplates') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('qs_description') }}
          </p>
        </div>

        <div class="flex-1 space-y-2 overflow-y-auto px-4 py-4 custom-scrollbar">
          <button
            v-for="software in allSoftwares"
            :key="software.code"
            type="button"
            :class="[
              'relative w-full rounded-xl border px-4 py-3 text-left transition-all',
              software.code === selectedSoftware
                ? 'border-primary/30 bg-white shadow-sm dark:border-primary/30 dark:bg-slate-900'
                : 'border-transparent bg-transparent hover:border-slate-200 hover:bg-white dark:hover:border-slate-700 dark:hover:bg-slate-900/60',
            ]"
            @click="selectSoftware(software.code)"
          >
            <button
              v-if="software.isCustom"
              type="button"
              class="absolute right-2 top-2 inline-flex size-5 items-center justify-center rounded-full text-slate-400 transition hover:bg-rose-50 hover:text-rose-500"
              @click.stop="removeCustomSoftware(software.code)"
            >
              <span class="material-symbols-outlined text-sm">close</span>
            </button>
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="text-sm font-semibold text-slate-900 dark:text-white">{{ software.name }}</p>
                <p
                  class="mt-1 truncate text-[11px] leading-5 text-slate-500 dark:text-slate-400"
                  :title="software.description"
                >
                  {{ software.description }}
                </p>
                <div class="mt-1.5 flex items-center gap-1.5" v-if="software.isCustom">
                  <span class="inline-flex items-center rounded-md bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary">{{ t('qs_customBadge') }}</span>
                </div>
              </div>
              <span
                :class="[
                  'mt-0.5 inline-flex size-2.5 rounded-full',
                  software.code === selectedSoftware ? 'bg-primary' : 'bg-slate-300 dark:bg-slate-600',
                ]"
              ></span>
            </div>
          </button>

          <button
            type="button"
            class="w-full rounded-xl border border-dashed border-slate-300 px-4 py-3 text-left text-sm text-slate-500 transition hover:border-primary/40 hover:text-primary dark:border-slate-700 dark:text-slate-400 dark:hover:border-primary/40 dark:hover:text-primary"
            @click="showAddSoftware = true"
          >
            {{ t('qs_addSoftware') }}
          </button>

          <div v-if="showAddSoftware" class="space-y-2 rounded-xl border border-primary/20 bg-white p-3 dark:bg-slate-900">
            <input
              v-model="newSoftwareName"
              :placeholder="t('qs_softwareNamePh')"
              class="h-9 w-full rounded-lg border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
            />
            <input
              v-model="newSoftwareDesc"
              :placeholder="t('qs_descPh')"
              class="h-9 w-full rounded-lg border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
            />
            <div class="flex gap-2">
              <button
                type="button"
                class="flex-1 rounded-lg bg-primary px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-primary/90"
                @click="confirmAddSoftware"
              >
                {{ t('qs_add') }}
              </button>
              <button
                type="button"
                class="flex-1 rounded-lg border border-slate-200 px-3 py-1.5 text-xs font-semibold text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
                @click="showAddSoftware = false"
              >
                {{ t('qs_cancel') }}
              </button>
            </div>
          </div>
        </div>
      </aside>

      <!-- Right panel -->
      <div class="flex min-w-0 flex-1 flex-col">
        <header class="flex h-16 shrink-0 items-center justify-between border-b border-slate-100 px-6 dark:border-slate-800">
          <div class="min-w-0">
            <p class="truncate text-base font-semibold text-slate-900 dark:text-white">
              {{ selectedSoftwareDef?.name || t('qs_title') }}
            </p>
            <p class="text-[11px] text-slate-500 dark:text-slate-400">
              {{ t('qs_filesPerVariant', { count: selectedSoftwareDef?.files?.length || 0 }) }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <button
              type="button"
              aria-label="Close quick setup"
              class="inline-flex size-9 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              @click="emit('close')"
            >
              <span class="material-symbols-outlined">close</span>
            </button>
          </div>
        </header>

        <div class="flex-1 overflow-y-auto px-6 py-6 custom-scrollbar">
          <div v-if="loadingCatalog" class="rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-5 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
            {{ t('qs_loading') }}
          </div>

          <div
            v-else-if="catalogStatus === 'unauthenticated'"
            class="rounded-2xl border border-amber-200 bg-amber-50 px-5 py-6 dark:border-amber-900/40 dark:bg-amber-950/20"
          >
            <p class="text-sm font-semibold text-amber-800 dark:text-amber-200">{{ t('qs_signInTitle') }}</p>
            <p class="mt-2 text-sm leading-6 text-amber-700 dark:text-amber-300">
              {{ t('qs_signInDesc') }}
            </p>
          </div>

          <div
            v-else-if="catalogStatus === 'failed'"
            class="rounded-2xl border border-rose-200 bg-rose-50 px-5 py-6 dark:border-rose-900/40 dark:bg-rose-950/20"
          >
            <p class="text-sm font-semibold text-rose-800 dark:text-rose-200">{{ t('qs_failedTitle') }}</p>
            <p class="mt-2 text-sm leading-6 text-rose-700 dark:text-rose-300">{{ catalogMessage || t('qs_tryAgain') }}</p>
          </div>

          <template v-else>
            <!-- Key selector -->
            <div v-if="isOpenCodeSelected" class="mb-4 space-y-4">
              <div>
                <div class="flex items-center justify-between gap-3">
                  <p class="text-[11px] font-semibold uppercase tracking-[0.22em] text-slate-400">{{ t('qs_opencodeProviders') }}</p>
                  <span class="text-[11px] text-slate-500 dark:text-slate-400">
                    {{ rendering ? t('qs_renderingShort') : t('qs_opencodeAutoRender') }}
                  </span>
                </div>

                <div v-if="compatibleKeys.length" class="mt-2 grid gap-2 md:grid-cols-2">
                  <label
                    v-for="key in compatibleKeys"
                    :key="key.id"
                    :class="[
                      'flex cursor-pointer items-start gap-3 rounded-xl border px-3 py-2 transition',
                      isOpenCodeKeySelected(key.id)
                        ? 'border-primary/40 bg-primary/5 text-slate-900 dark:bg-primary/10 dark:text-white'
                        : 'border-slate-200 bg-white text-slate-700 hover:border-primary/30 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200',
                    ]"
                  >
                    <input
                      class="sr-only"
                      type="checkbox"
                      :checked="isOpenCodeKeySelected(key.id)"
                      @change="toggleOpenCodeKey(key.id, $event.target.checked)"
                    />
                    <span class="material-symbols-outlined mt-0.5 text-base text-primary">
                      {{ isOpenCodeKeySelected(key.id) ? 'check_circle' : 'radio_button_unchecked' }}
                    </span>
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-sm font-semibold">{{ key.name }}</span>
                      <span class="mt-0.5 block truncate text-[11px] text-slate-500 dark:text-slate-400">
                        {{ key.group?.name || t('qs_noGroup') }} · {{ key.provider }} · {{ maskKey(key.key) }}
                      </span>
                      <span class="mt-1 block truncate font-mono text-[10px] text-slate-400 dark:text-slate-500">
                        {{ key.base_url || providerBaseUrl(key.provider) }}
                      </span>
                    </span>
                  </label>
                </div>
              </div>

              <div v-if="selectedOpenCodeKeys.length" class="rounded-xl border border-slate-200 bg-slate-50/70 p-3 dark:border-slate-800 dark:bg-slate-950/40">
                <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_opencodeAutoBaseUrl') }}</p>
                <div class="mt-2 grid gap-2 md:grid-cols-2">
                  <div
                    v-for="key in selectedOpenCodeKeys"
                    :key="`base-${key.id}`"
                    class="rounded-lg border border-slate-200 bg-white px-3 py-2 dark:border-slate-800 dark:bg-slate-900"
                  >
                    <span class="block truncate text-[11px] font-semibold text-slate-600 dark:text-slate-300">
                      {{ key.name }} · {{ key.provider }}
                    </span>
                    <span class="mt-1 block truncate font-mono text-[11px] text-slate-500 dark:text-slate-400">
                      {{ key.base_url || providerBaseUrl(key.provider) }}
                    </span>
                  </div>
                </div>
              </div>

              <div v-if="selectedOpenCodeKeys.length" class="grid gap-3 md:grid-cols-3">
                <label class="block">
                  <span class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_opencodeModelProvider') }}</span>
                  <select
                    v-model="openCodeModelKeyId"
                    class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                  >
                    <option v-for="key in selectedOpenCodeKeys" :key="`model-${key.id}`" :value="String(key.id)">
                      {{ key.name }} ({{ key.provider }})
                    </option>
                  </select>
                </label>
                <label class="block md:col-span-2">
                  <span class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_opencodeModel') }}</span>
                  <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,0.8fr)]">
                    <select
                      v-model="openCodeModel"
                      class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                      :disabled="loadingOpenCodeModels || !currentOpenCodeModels.length"
                    >
                      <option value="" disabled>
                        {{ loadingOpenCodeModels ? t('qs_opencodeModelsLoading') : t('qs_opencodeModelSelectPh') }}
                      </option>
                      <option v-for="model in currentOpenCodeModels" :key="model.id" :value="model.id">
                        {{ model.name && model.name !== model.id ? `${model.id} · ${model.name}` : model.id }}
                      </option>
                    </select>
                    <input
                      v-model.trim="openCodeModel"
                      type="text"
                      :placeholder="defaultOpenCodeModel(selectedOpenCodeModelKey?.provider)"
                      class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    />
                  </div>
                  <p class="mt-1 text-[11px] text-slate-500 dark:text-slate-400">
                    <template v-if="loadingOpenCodeModels">{{ t('qs_opencodeModelsLoading') }}</template>
                    <template v-else-if="openCodeModelError">{{ openCodeModelError }}</template>
                    <template v-else-if="currentOpenCodeModels.length">{{ t('qs_opencodeModelsLoaded', { count: currentOpenCodeModels.length }) }}</template>
                    <template v-else>{{ t('qs_opencodeModelsManual') }}</template>
                  </p>
                  <div v-if="currentOpenCodeModels.length" class="mt-2 flex max-h-24 flex-wrap gap-1.5 overflow-y-auto pr-1 custom-scrollbar">
                    <button
                      v-for="model in currentOpenCodeModels"
                      :key="`model-option-${model.id}`"
                      type="button"
                      :class="[
                        'min-h-7 max-w-full truncate rounded-lg border px-2.5 text-[11px] font-medium transition',
                        openCodeModel === model.id
                          ? 'border-primary/40 bg-primary/10 text-primary'
                          : 'border-slate-200 bg-white text-slate-600 hover:border-primary/30 hover:text-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300',
                      ]"
                      :title="model.name && model.name !== model.id ? `${model.id} · ${model.name}` : model.id"
                      @click="selectOpenCodeModel(model.id)"
                    >
                      {{ model.id }}
                    </button>
                  </div>
                </label>
                <label class="block">
                  <span class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_opencodeSmallModel') }}</span>
                  <input
                    v-model.trim="openCodeSmallModel"
                    type="text"
                    :placeholder="t('qs_opencodeSmallModelPh')"
                    class="h-10 w-full rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                  />
                </label>
              </div>
            </div>

            <div v-else class="mb-4">
              <p class="text-[11px] font-semibold uppercase tracking-[0.22em] text-slate-400">{{ t('qs_apiKey') }}</p>
              <div class="mt-2 flex items-center gap-3">
                <select
                  v-model="selectedKeyId"
                  class="flex-1 h-10 rounded-xl border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                >
                  <option value="" disabled>{{ t('qs_selectKeyPh') }}</option>
                  <option v-for="key in apiKeys" :key="key.id" :value="key.id" :disabled="!isKeyCompatible(key)">
                    {{ key.name }} · {{ key.group?.name || t('qs_noGroup') }} ({{ key.provider }}){{ isKeyCompatible(key) ? '' : ' ' + t('qs_incompatible') }}
                  </option>
                </select>
                <button
                  type="button"
                  class="inline-flex h-10 items-center justify-center rounded-xl border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                  :disabled="!apiKeyHasPlainSecret(selectedKey)"
                  @click="copySelectedKey"
                >
                  {{ t('qs_copyKey') }}
                </button>
              </div>
              <div v-if="selectedKey" class="mt-2 flex items-center gap-2 text-[11px] text-slate-500 dark:text-slate-400">
                <span>{{ selectedKey.group?.name || t('qs_noGroup') }}</span>
                <span>·</span>
                <span class="font-mono">{{ maskKey(selectedKey.key) }}</span>
              </div>
            </div>

            <!-- Config editor -->
            <div v-if="currentVariant || selectedSoftwareDef?.isCustom" class="mt-6">
              <!-- Notes -->
              <div v-if="currentVariant.notes?.length" class="mb-4 rounded-2xl border border-sky-200 bg-sky-50/70 px-4 py-3 dark:border-sky-900/40 dark:bg-sky-950/20">
                <p class="text-[11px] font-semibold uppercase tracking-wide text-sky-700 dark:text-sky-300">{{ t('qs_notes') }}</p>
                <p
                  v-for="(note, index) in currentVariant.notes"
                  :key="`note-${index}`"
                  class="mt-2 text-sm leading-6 text-sky-700 dark:text-sky-300"
                >
                  {{ note }}
                </p>
              </div>

              <!-- File tabs -->
              <div class="flex flex-wrap gap-2">
                <button
                  v-for="file in editableFiles"
                  :key="file.code"
                  type="button"
                  :class="[
                    'inline-flex min-h-9 items-center justify-center rounded-full border px-3 text-[11px] font-semibold transition',
                    file.code === selectedFileCode
                      ? 'border-primary/30 bg-primary/10 text-primary'
                      : 'border-slate-200 text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800',
                  ]"
                  @click="selectedFileCode = file.code"
                >
                  {{ file.label }}
                  <button
                    v-if="selectedSoftwareDef?.isCustom"
                    type="button"
                    class="ml-1 inline-flex size-4 items-center justify-center rounded-full text-slate-400 hover:text-rose-500"
                    @click.stop="removeFile(file.code)"
                  >
                    <span class="material-symbols-outlined text-xs">close</span>
                  </button>
                </button>
                <button
                  v-if="selectedSoftwareDef?.isCustom"
                  type="button"
                  class="inline-flex min-h-9 items-center justify-center rounded-full border border-dashed border-slate-300 px-3 text-[11px] text-slate-500 transition hover:border-primary/40 hover:text-primary dark:border-slate-700 dark:text-slate-400"
                  @click="addNewFile"
                >
                  {{ t('qs_addFile') }}
                </button>
              </div>

              <!-- Editor -->
              <div v-if="currentFile" class="mt-4 space-y-4">
                <div>
                  <label class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_targetPath') }}</label>
                  <input
                    :value="currentFile.path"
                    type="text"
                    class="h-10 w-full rounded-xl border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
                    @input="updateCurrentFile('path', $event.target.value)"
                  />
                </div>

                <div>
                  <label class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_version') }}</label>
                  <input
                    :value="currentFile.version || ''"
                    type="text"
                    :placeholder="t('qs_versionPh')"
                    class="h-10 w-full rounded-xl border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
                    @input="updateCurrentFile('version', $event.target.value)"
                  />
                </div>

                <div>
                  <div class="mb-1 flex items-center justify-between gap-3">
                    <label class="block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_content') }}</label>
                    <button
                      type="button"
                      class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                      @click="copyFile(currentFile)"
                    >
                      {{ t('qs_copyFile') }}
                    </button>
                  </div>
                  <textarea
                    :value="currentFile.content"
                    class="code-editor h-[320px] w-full rounded-2xl border border-slate-200 bg-slate-950 px-4 py-3 text-[12px] leading-6 text-slate-100 outline-none transition focus:border-primary dark:border-slate-700"
                    spellcheck="false"
                    @input="updateCurrentFile('content', $event.target.value)"
                  ></textarea>
                </div>
              </div>

              <!-- Apply -->
              <div class="mt-4 flex justify-end">
                <button
                  type="button"
                  class="inline-flex min-h-10 items-center justify-center rounded-lg border border-slate-200 px-3 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                  :disabled="applying || rendering || !canApplyCurrentVariant"
                  @click="applyCurrentVariant"
                >
                  {{ applying ? t('qs_applying') : t('qs_applyFiles') }}
                </button>
              </div>
            </div>

            <div v-else class="mt-6 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-5 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
              <template v-if="rendering">{{ t('qs_rendering') }}</template>
              <template v-else-if="!compatibleKeys.length && !selectedSoftwareDef?.isCustom">{{ t('qs_noCompatibleKeys') }}</template>
              <template v-else-if="isOpenCodeSelected">{{ t('qs_selectOpenCodeKeys') }}</template>
              <template v-else>{{ t('qs_selectKeyAbove') }}</template>
            </div>

            <div v-if="statusMessage" class="mt-6 rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 dark:border-slate-800 dark:bg-slate-900/50 dark:text-slate-300">
              {{ statusMessage }}
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { applyQuickSetup, getQuickSetupCatalog, getQuickSetupModels, renderQuickSetup } from '../services/quickSetupApi';
import { useI18n } from '../i18n';

const { t } = useI18n();

const props = defineProps({
  open: {
    type: Boolean,
    default: false,
  },
});
const emit = defineEmits(['close']);
const loadingCatalog = ref(false);
const rendering = ref(false);
const applying = ref(false);
const catalogStatus = ref('idle');
const catalogMessage = ref('');
const statusMessage = ref('');
const softwares = ref([]);
const apiKeys = ref([]);
const selectedSoftware = ref('');
const selectedKeyId = ref('');
const selectedOpenCodeKeyIds = ref([]);
const openCodeModelKeyId = ref('');
const openCodeModel = ref('');
const openCodeSmallModel = ref('');
const openCodeModelsByKeyId = ref({});
const loadingOpenCodeModels = ref(false);
const openCodeModelError = ref('');
const openCodeModelLoadPromises = new Map();
const currentVariant = ref(null);
const selectedFileCode = ref('');
const editableFiles = ref([]);
const customSoftwares = ref([]);
const showAddSoftware = ref(false);
const newSoftwareName = ref('');
const newSoftwareDesc = ref('');
let fileCounter = 0;
const allSoftwares = computed(() => [
  ...softwares.value,
  ...customSoftwares.value,
]);
const selectedSoftwareDef = computed(() => allSoftwares.value.find((item) => item.code === selectedSoftware.value) || null);
const isOpenCodeSelected = computed(() => selectedSoftwareDef.value?.code === 'opencode');
const compatibleKeys = computed(() => {
  const supportedProviders = new Set(selectedSoftwareDef.value?.supported_providers || []);
  return apiKeys.value.filter((key) => supportedProviders.size === 0 || supportedProviders.has(key.provider));
});
function isKeyCompatible(key) {
  const supportedProviders = new Set(selectedSoftwareDef.value?.supported_providers || []);
  return supportedProviders.size === 0 || supportedProviders.has(key.provider);
}
const selectedKey = computed(() => {
  if (!selectedKeyId.value) return null;
  return apiKeys.value.find((k) => String(k.id) === String(selectedKeyId.value)) || null;
});
const selectedOpenCodeKeys = computed(() => {
  const selected = new Set(selectedOpenCodeKeyIds.value.map((id) => String(id)));
  return compatibleKeys.value.filter((key) => selected.has(String(key.id)));
});
const selectedOpenCodeModelKey = computed(() => {
  if (!selectedOpenCodeKeys.value.length) return null;
  return selectedOpenCodeKeys.value.find((key) => String(key.id) === String(openCodeModelKeyId.value)) || selectedOpenCodeKeys.value[0];
});
const currentOpenCodeModels = computed(() => {
  const keyId = String(openCodeModelKeyId.value || '');
  return Array.isArray(openCodeModelsByKeyId.value[keyId]) ? openCodeModelsByKeyId.value[keyId] : [];
});
const loadingOpenCodeModelKeyIds = ref(new Set());
const canApplyCurrentVariant = computed(() => {
  if (!selectedSoftware.value) return false;
  if (isOpenCodeSelected.value) {
    return selectedOpenCodeKeys.value.length > 0;
  }
  return editableFiles.value.length > 0;
});
const currentFile = computed(() => {
  return editableFiles.value.find((item) => item.code === selectedFileCode.value) || editableFiles.value[0] || null;
});
let openCodeRenderTimer = null;
function maskKey(key) {
  if (!key) return '';
  if (key.length <= 12) return key;
  return key.slice(0, 6) + '•••••••' + key.slice(-4);
}
function looksMaskedAPIKey(value) {
  const text = String(value || '').trim();
  if (!text) return false;
  return text.includes('***') || text.includes('...') || text.includes('…');
}
function apiKeyHasPlainSecret(key) {
  const value = String(key?.key || '').trim();
  return Boolean(value) && !looksMaskedAPIKey(value);
}
function keyProvider(keyId) {
  return apiKeys.value.find((key) => String(key.id) === String(keyId))?.provider || '';
}
function defaultOpenCodeModel(provider) {
  return provider === 'anthropic' ? 'claude-sonnet-4-5' : 'gpt-5';
}
function providerBaseUrl(provider) {
  const exact = apiKeys.value.find((key) => key.provider === provider && key.base_url)?.base_url;
  const fallback = apiKeys.value.find((key) => key.base_url)?.base_url || '';
  const root = exact || fallback;
  if (!root) return '';
  let trimmed = String(root).replace(/\/+$/, '');
  if (trimmed.endsWith('/v1')) {
    trimmed = trimmed.slice(0, -3);
  }
  if (provider === 'openai') {
    return `${trimmed}/v1`;
  }
  return trimmed;
}
function isOpenCodeKeySelected(keyId) {
  return selectedOpenCodeKeyIds.value.some((id) => String(id) === String(keyId));
}
function toggleOpenCodeKey(keyId, checked) {
  const id = String(keyId);
  if (checked) {
    if (!isOpenCodeKeySelected(id)) {
      selectedOpenCodeKeyIds.value = [...selectedOpenCodeKeyIds.value, id];
    }
  } else {
    selectedOpenCodeKeyIds.value = selectedOpenCodeKeyIds.value.filter((item) => String(item) !== id);
  }
  syncOpenCodeModelSelection();
  clearCurrentRender();
  scheduleOpenCodeRender();
}
function syncOpenCodeModelSelection() {
  if (!selectedOpenCodeKeys.value.length) {
    openCodeModelKeyId.value = '';
    return;
  }
  const selectedModelKeyStillPresent = selectedOpenCodeKeys.value.some((key) => String(key.id) === String(openCodeModelKeyId.value));
  if (!selectedModelKeyStillPresent) {
    const preferred = selectedOpenCodeKeys.value.find((key) => key.provider === 'openai') || selectedOpenCodeKeys.value[0];
    openCodeModelKeyId.value = String(preferred.id);
  }
  if (!openCodeModel.value.trim()) {
    openCodeModel.value = defaultOpenCodeModel(keyProvider(openCodeModelKeyId.value));
  }
}
function refreshOpenCodeModelsLoading() {
  loadingOpenCodeModels.value = loadingOpenCodeModelKeyIds.value.has(String(openCodeModelKeyId.value || ''));
}
function selectOpenCodeModel(modelId) {
  const value = String(modelId || '').trim();
  if (!value) {
    return;
  }
  openCodeModel.value = value;
}
async function loadOpenCodeModelsForKey(keyId) {
  const id = String(keyId || '');
  if (!id) {
    return;
  }
  if (Array.isArray(openCodeModelsByKeyId.value[id])) {
    if (String(openCodeModelKeyId.value) === id) {
      openCodeModelError.value = '';
      if (!openCodeModel.value.trim() && openCodeModelsByKeyId.value[id].length) {
        openCodeModel.value = openCodeModelsByKeyId.value[id][0].id;
      }
    }
    return;
  }
  const pendingLoad = openCodeModelLoadPromises.get(id);
  if (pendingLoad) {
    await pendingLoad;
    return;
  }

  const loadPromise = (async () => {
    loadingOpenCodeModelKeyIds.value = new Set([...loadingOpenCodeModelKeyIds.value, id]);
    refreshOpenCodeModelsLoading();
    if (String(openCodeModelKeyId.value) === id) {
      openCodeModelError.value = '';
    }
    try {
      const result = await getQuickSetupModels(id);
      const models = Array.isArray(result?.models) ? result.models.filter((model) => model?.id) : [];
      openCodeModelsByKeyId.value = {
        ...openCodeModelsByKeyId.value,
        [id]: models,
      };
      if (String(openCodeModelKeyId.value) === id) {
        const stillAvailable = models.some((model) => model.id === openCodeModel.value);
        if ((!openCodeModel.value.trim() || !stillAvailable) && models.length) {
          openCodeModel.value = models[0].id;
        }
        if (!models.length && !openCodeModel.value.trim()) {
          openCodeModel.value = defaultOpenCodeModel(keyProvider(id));
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : '';
      if (String(openCodeModelKeyId.value) === id) {
        openCodeModelError.value = message || t('qs_opencodeModelsFailed');
      }
      if (String(openCodeModelKeyId.value) === id && !openCodeModel.value.trim()) {
        openCodeModel.value = defaultOpenCodeModel(keyProvider(id));
      }
    } finally {
      openCodeModelLoadPromises.delete(id);
      const nextLoadingKeyIds = new Set(loadingOpenCodeModelKeyIds.value);
      nextLoadingKeyIds.delete(id);
      loadingOpenCodeModelKeyIds.value = nextLoadingKeyIds;
      refreshOpenCodeModelsLoading();
    }
  })();
  openCodeModelLoadPromises.set(id, loadPromise);
  await loadPromise;
}
function buildOpenCodeSpec() {
  syncOpenCodeModelSelection();
  return {
    model_key_id: Number(openCodeModelKeyId.value) || 0,
    model_provider: keyProvider(openCodeModelKeyId.value),
    model: openCodeModel.value.trim() || defaultOpenCodeModel(keyProvider(openCodeModelKeyId.value)),
    small_model: openCodeSmallModel.value.trim(),
    provider_ids: selectedOpenCodeKeys.value.map((key) => Number(key.id)).filter(Boolean),
    providers: selectedOpenCodeKeys.value.map((key) => ({
      key_id: Number(key.id),
      base_url: String(key.base_url || providerBaseUrl(key.provider) || '').trim(),
    })),
  };
}
function clearCurrentRender() {
  currentVariant.value = null;
  editableFiles.value = [];
  selectedFileCode.value = '';
}
function scheduleOpenCodeRender(delay = 450) {
  if (!isOpenCodeSelected.value || selectedSoftwareDef.value?.isCustom) {
    return;
  }
  if (openCodeRenderTimer) {
    window.clearTimeout(openCodeRenderTimer);
  }
  openCodeRenderTimer = window.setTimeout(() => {
    openCodeRenderTimer = null;
    renderSelectedKey();
  }, delay);
}
async function loadCatalog() {
  loadingCatalog.value = true;
  statusMessage.value = '';
  try {
    const result = await getQuickSetupCatalog();
    catalogStatus.value = result.status || 'idle';
    catalogMessage.value = result.message || '';
    if (result.status !== 'success' || !result.data) {
      softwares.value = [];
      apiKeys.value = [];
      currentVariant.value = null;
      editableFiles.value = [];
      return;
    }
    softwares.value = Array.isArray(result.data.softwares) ? result.data.softwares : [];
    apiKeys.value = Array.isArray(result.data.api_keys) ? result.data.api_keys : [];
    if (!selectedSoftware.value || !softwares.value.some((item) => item.code === selectedSoftware.value)) {
      selectedSoftware.value = softwares.value[0]?.code || '';
    }
  } catch (error) {
    catalogStatus.value = 'failed';
    catalogMessage.value = error instanceof Error ? error.message : t('qs_failedCatalog');
    statusMessage.value = catalogMessage.value;
  } finally {
    loadingCatalog.value = false;
  }
}
async function renderSelectedKey() {
  if (!selectedSoftware.value) {
    clearCurrentRender();
    return;
  }
  const keyIds = isOpenCodeSelected.value
    ? selectedOpenCodeKeys.value.map((key) => key.id)
    : [selectedKeyId.value].filter(Boolean);
  if (!keyIds.length) {
    clearCurrentRender();
    return;
  }
  rendering.value = true;
  try {
    const options = isOpenCodeSelected.value ? { opencode: buildOpenCodeSpec() } : {};
    const result = await renderQuickSetup(selectedSoftware.value, keyIds, options);
    const variantList = Array.isArray(result?.variants) ? result.variants : [];
    if (variantList.length > 0) {
      currentVariant.value = variantList[0];
      editableFiles.value = (variantList[0].files || []).map((f) => ({ ...f }));
      selectedFileCode.value = editableFiles.value[0]?.code || '';
    } else {
      clearCurrentRender();
    }
  } catch (error) {
    statusMessage.value = error instanceof Error ? error.message : t('qs_failedRender');
    clearCurrentRender();
  } finally {
    rendering.value = false;
  }
}
function selectSoftware(code) {
  if (selectedSoftware.value === code) {
    return;
  }
  selectedSoftware.value = code;
}
function confirmAddSoftware() {
  const name = newSoftwareName.value.trim();
  if (!name) return;
  const code = 'custom-' + name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');
  customSoftwares.value.push({
    code,
    name,
    description: newSoftwareDesc.value.trim() || t('qs_customDesc'),
    supported_providers: [],
    files: [],
    isCustom: true,
  });
  showAddSoftware.value = false;
  newSoftwareName.value = '';
  newSoftwareDesc.value = '';
  selectedSoftware.value = code;
}
function removeCustomSoftware(code) {
  customSoftwares.value = customSoftwares.value.filter((s) => s.code !== code);
  if (selectedSoftware.value === code) {
    selectedSoftware.value = softwares.value[0]?.code || '';
  }
}
function addNewFile() {
  fileCounter++;
  const newFile = {
    code: `custom-file-${fileCounter}`,
    label: `file-${fileCounter}`,
    path: '~/path/to/config',
    format: 'text',
    kind: 'file',
    content: '',
    version: '',
  };
  editableFiles.value.push(newFile);
  selectedFileCode.value = newFile.code;
}
function removeFile(code) {
  const idx = editableFiles.value.findIndex((f) => f.code === code);
  if (idx < 0) return;
  editableFiles.value.splice(idx, 1);
  if (selectedFileCode.value === code) {
    selectedFileCode.value = editableFiles.value[0]?.code || ''
  }
}
function updateCurrentFile(field, value) {
  const index = editableFiles.value.findIndex((item) => item.code === selectedFileCode.value);
  if (index < 0) {
    return;
  }
  editableFiles.value[index] = {
    ...editableFiles.value[index],
    [field]: value,
  };
}
async function copyText(value, successText) {
  try {
    await navigator.clipboard.writeText(value);
    statusMessage.value = successText;
  } catch (error) {
    statusMessage.value = error instanceof Error ? error.message : t('qs_copyFailed');
  }
}
async function copySelectedKey() {
  if (!apiKeyHasPlainSecret(selectedKey.value)) {
    statusMessage.value = t('qs_keyMasked');
    return;
  }
  await copyText(selectedKey.value.key, t('qs_copiedKey', { name: selectedKey.value.name }));
}
async function copyFile(file) {
  if (!file) {
    return;
  }
  await copyText(file.content || '', t('qs_copiedFile', { label: file.label }));
}
async function applyCurrentVariant() {
  if (!selectedSoftware.value) {
    return;
  }
  applying.value = true;
  try {
    if (isOpenCodeSelected.value) {
      await renderSelectedKey();
    }
    if (!editableFiles.value.length) {
      return;
    }
    const result = await applyQuickSetup(selectedSoftware.value, editableFiles.value);
    const writtenCount = Array.isArray(result?.written) ? result.written.length : 0;
    statusMessage.value = writtenCount > 0
      ? t('qs_appliedFiles', { count: writtenCount, name: selectedSoftwareDef.value?.name || selectedSoftware.value })
      : t('qs_noFilesWritten');
  } catch (error) {
    statusMessage.value = error instanceof Error ? error.message : t('qs_failedApply');
  } finally {
    applying.value = false;
  }
}
function onModalKeydown(event) {
  if (event.key === 'Escape' && props.open) {
    event.stopImmediatePropagation();
    emit('close');
  }
}
watch(
  () => props.open,
  async (value) => {
    if (!value) {
      return;
    }
    await loadCatalog();
  },
  { immediate: true },
);
// Software switch -> auto-select first compatible key
watch(selectedSoftware, async () => {
  clearCurrentRender();
  const keys = compatibleKeys.value;
  if (keys.length === 0) {
    selectedKeyId.value = '';
    selectedOpenCodeKeyIds.value = [];
    openCodeModelKeyId.value = '';
    openCodeModelError.value = '';
    return;
  }
  const preferred = keys.find((k) => k.provider === 'openai') || keys[0];
  if (isOpenCodeSelected.value) {
    selectedOpenCodeKeyIds.value = [String(preferred.id)];
    openCodeModelKeyId.value = String(preferred.id);
    openCodeModel.value = '';
    openCodeSmallModel.value = '';
    await loadOpenCodeModelsForKey(openCodeModelKeyId.value);
    if (!openCodeModel.value.trim()) {
      openCodeModel.value = defaultOpenCodeModel(preferred.provider);
    }
    scheduleOpenCodeRender(0);
    return;
  }
  selectedKeyId.value = preferred.id;
});
// Key switch -> render config
watch(selectedKeyId, () => {
  if (selectedSoftwareDef.value?.isCustom || isOpenCodeSelected.value) {
    return;
  }
  renderSelectedKey();
});
watch(selectedOpenCodeKeyIds, async () => {
  if (!isOpenCodeSelected.value) {
    return;
  }
  syncOpenCodeModelSelection();
  await loadOpenCodeModelsForKey(openCodeModelKeyId.value);
});
watch(openCodeModelKeyId, async () => {
  if (!isOpenCodeSelected.value) {
    return;
  }
  openCodeModel.value = '';
  await loadOpenCodeModelsForKey(openCodeModelKeyId.value);
  if (!openCodeModel.value.trim()) {
    openCodeModel.value = defaultOpenCodeModel(keyProvider(openCodeModelKeyId.value));
  }
  scheduleOpenCodeRender();
});
watch(openCodeModel, () => {
  if (isOpenCodeSelected.value) {
    scheduleOpenCodeRender();
  }
});
watch(openCodeSmallModel, () => {
  if (isOpenCodeSelected.value) {
    scheduleOpenCodeRender();
  }
});
onMounted(() => {
  window.addEventListener('keydown', onModalKeydown, true);
});
onUnmounted(() => {
  if (openCodeRenderTimer) {
    window.clearTimeout(openCodeRenderTimer);
  }
  window.removeEventListener('keydown', onModalKeydown, true);
});
</script>
