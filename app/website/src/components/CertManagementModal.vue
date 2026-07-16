<template>
  <div
    v-if="modelValue"
    class="fixed inset-0 z-[120] flex items-center justify-center p-4"
  >
    <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="close"></div>
    <div
      class="relative z-10 w-full max-w-3xl bg-white dark:bg-slate-900 rounded-lg border border-slate-200 dark:border-slate-700 shadow-2xl overflow-hidden"
    >
      <div class="px-6 py-4 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between">
        <div>
          <h3 class="text-lg font-bold text-slate-900 dark:text-slate-100">{{ t('cert_title') }}</h3>
          <p class="text-xs text-slate-500 dark:text-slate-400 mt-1">{{ t('cert_subtitle') }}</p>
        </div>
        <button
          type="button"
          :aria-label="t('cert_title')"
          class="size-11 rounded-full flex items-center justify-center text-slate-500 hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:hover:bg-slate-800"
          @click="close"
        >
          <span class="material-symbols-outlined">close</span>
        </button>
      </div>

      <div class="max-h-[calc(100vh-7rem)] space-y-5 overflow-y-auto p-6">
        <div class="flex items-center gap-2 flex-wrap">
          <span class="px-2.5 py-1.5 bg-slate-100 dark:bg-slate-800 rounded-md text-xs font-medium text-slate-700 dark:text-slate-300">
            {{ t('cert_localCert') }}
          </span>
          <span class="text-[11px] text-slate-400">{{ t('cert_autoRefreshing') }}</span>
          <span class="text-[11px] text-slate-400">{{ t('cert_lastRefreshed', { time: lastRefreshed }) }}</span>
          <span
            class="inline-block size-3.5 border-2 border-slate-200 border-t-primary rounded-full animate-spin"
          ></span>
        </div>

        <!-- Status Display -->
        <div
          class="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/40 border border-slate-200 dark:border-slate-700"
        >
          <div v-if="statusError" class="text-sm text-red-500">{{ statusError }}</div>
          <div v-else-if="certStatus" class="space-y-2">
            <div class="flex flex-wrap gap-2">
              <span
                class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium"
                :class="certStatus.is_exported
                  ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-500/20 dark:text-slate-400'"
              >
                <span class="material-symbols-outlined text-[12px]">{{ certStatus.is_exported ? 'check_circle' : 'cancel' }}</span>
                {{ certStatus.is_exported ? t('cert_exported') : t('cert_notExported') }}
              </span>
              <span
                class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium"
                :class="certStatus.is_installed
                  ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-300'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-500/20 dark:text-slate-400'"
              >
                <span class="material-symbols-outlined text-[12px]">{{ certStatus.is_installed ? 'check_circle' : 'cancel' }}</span>
                {{ certStatus.is_installed ? t('cert_installed') : t('cert_notInstalled') }}
              </span>
              <span
                class="inline-flex items-center gap-1 px-2 py-0.5 rounded text-[11px] font-medium"
                :class="certStatus.is_trusted
                  ? 'bg-blue-100 text-blue-700 dark:bg-blue-500/20 dark:text-blue-300'
                  : 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-300'"
              >
                <span class="material-symbols-outlined text-[12px]">{{ certStatus.is_trusted ? 'shield' : 'warning' }}</span>
                {{ certStatus.is_trusted ? t('cert_trusted') : t('cert_notTrusted') }}
              </span>
            </div>
            <div class="text-xs text-slate-500 dark:text-slate-400 space-y-0.5 mt-2">
              <div><strong>{{ t('cert_subject') }}</strong> {{ certStatus.subject || '-' }}</div>
              <div><strong>{{ t('cert_issuer') }}</strong> {{ certStatus.issuer || '-' }}</div>
              <div><strong>{{ t('cert_validity') }}</strong> {{ certStatus.not_before || '-' }} ~ {{ certStatus.not_after || '-' }}</div>
              <div><strong>{{ t('cert_fingerprint') }}</strong> <code class="break-all">{{ certStatus.fingerprint || '-' }}</code></div>
              <div v-if="certStatus.install_path"><strong>{{ t('cert_installPath') }}</strong> <code class="break-all">{{ certStatus.install_path }}</code></div>
              <div><strong>{{ t('cert_source') }}</strong> {{ sourceLabel(certStatus.source) }}</div>
              <div v-if="certStatus.key_algorithm"><strong>{{ t('cert_keyAlgorithm') }}</strong> {{ certStatus.key_algorithm }}</div>
            </div>
          </div>
          <div v-else class="text-sm text-slate-400">{{ t('cert_loadingCertInfo') }}</div>
        </div>

        <!-- Action Buttons -->
        <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 gap-2">
          <button
            type="button"
            :disabled="operationBusy"
            class="min-h-9 flex items-center justify-center gap-1.5 px-2.5 py-1.5 border border-slate-200 dark:border-slate-700 rounded-md text-xs hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @click="installCert"
          >
            <span class="material-symbols-outlined text-[16px] leading-none text-slate-500">check_circle</span>
            <span>{{ t('cert_installToSystem') }}</span>
          </button>
          <button
            type="button"
            :disabled="downloading"
            class="min-h-9 flex items-center justify-center gap-1.5 px-2.5 py-1.5 border border-slate-200 dark:border-slate-700 rounded-md text-xs hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @click="downloadCertFile"
          >
            <span
              v-if="downloading"
              class="inline-block size-3.5 border-2 border-slate-300 border-t-primary rounded-full animate-spin"
            ></span>
            <span class="material-symbols-outlined text-[16px] leading-none text-slate-500">download</span>
            <span>{{ t('cert_downloadPem') }}</span>
          </button>
          <button
            type="button"
            :disabled="operationBusy"
            class="min-h-9 flex items-center justify-center gap-1.5 px-2.5 py-1.5 border border-red-200 dark:border-red-500/40 rounded-md text-xs text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @click="removeCert"
          >
            <span class="material-symbols-outlined text-[16px] leading-none text-red-500">delete</span>
            <span>{{ t('cert_removeCert') }}</span>
          </button>
          <button
            type="button"
            :disabled="operationBusy"
            class="min-h-9 flex items-center justify-center gap-1.5 px-2.5 py-1.5 border border-amber-200 dark:border-amber-500/40 rounded-md text-xs text-amber-700 dark:text-amber-300 hover:bg-amber-50 dark:hover:bg-amber-900/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            @click="generateCert"
          >
            <span class="material-symbols-outlined text-[16px] leading-none text-amber-500">autorenew</span>
            <span>{{ t('cert_regenerateCert') }}</span>
          </button>
        </div>

        <section class="border-y border-slate-200 py-5 dark:border-slate-700" aria-labelledby="cert-import-title">
          <div class="flex flex-col gap-4">
            <div class="flex flex-col gap-1 sm:flex-row sm:items-start sm:justify-between sm:gap-4">
              <div>
                <h4 id="cert-import-title" class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ t('cert_importTitle') }}</h4>
                <p class="mt-1 text-xs leading-5 text-slate-600 dark:text-slate-400">{{ t('cert_importDesc') }}</p>
              </div>
              <button
                v-if="certStatus?.can_rollback"
                type="button"
                :disabled="operationBusy"
                class="min-h-11 shrink-0 cursor-pointer rounded-md border border-slate-300 px-3 text-xs font-semibold text-slate-700 transition-colors duration-200 hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-200 dark:hover:bg-slate-800"
                @click="rollbackCertificate"
              >
                <span class="material-symbols-outlined mr-1 align-middle text-[16px]">undo</span>
                {{ t('cert_rollback') }}
              </button>
            </div>

            <div class="inline-flex w-full rounded-md border border-slate-200 p-1 sm:w-fit dark:border-slate-700" role="radiogroup" :aria-label="t('cert_importFormat')">
              <button
                v-for="mode in importModes"
                :key="mode.value"
                type="button"
                role="radio"
                :aria-checked="importMode === mode.value"
                class="min-h-11 flex-1 cursor-pointer rounded px-4 text-xs font-semibold transition-colors duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary sm:flex-none"
                :class="importMode === mode.value ? 'bg-slate-900 text-white dark:bg-primary' : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
                @click="setImportMode(mode.value)"
              >
                {{ t(mode.labelKey) }}
              </button>
            </div>

            <div v-if="importMode === 'pem'" class="grid gap-4 sm:grid-cols-2">
              <label class="block text-xs font-semibold text-slate-700 dark:text-slate-200">
                {{ t('cert_caFile') }}
                <input
                  type="file"
                  accept=".pem,.crt,.cer,application/x-x509-ca-cert"
                  class="mt-2 block min-h-11 w-full cursor-pointer rounded-md border border-slate-300 bg-white px-3 py-2 text-xs text-slate-700 file:mr-3 file:cursor-pointer file:rounded file:border-0 file:bg-slate-100 file:px-3 file:py-1 file:text-xs file:font-semibold focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-slate-600 dark:bg-slate-950 dark:text-slate-200 dark:file:bg-slate-800"
                  @change="setImportFile('certificate', $event)"
                />
              </label>
              <label class="block text-xs font-semibold text-slate-700 dark:text-slate-200">
                {{ t('cert_keyFile') }}
                <input
                  type="file"
                  accept=".pem,.key,application/x-pem-file"
                  class="mt-2 block min-h-11 w-full cursor-pointer rounded-md border border-slate-300 bg-white px-3 py-2 text-xs text-slate-700 file:mr-3 file:cursor-pointer file:rounded file:border-0 file:bg-slate-100 file:px-3 file:py-1 file:text-xs file:font-semibold focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-slate-600 dark:bg-slate-950 dark:text-slate-200 dark:file:bg-slate-800"
                  @change="setImportFile('privateKey', $event)"
                />
              </label>
            </div>

            <div v-else class="grid gap-4 sm:grid-cols-2">
              <label class="block text-xs font-semibold text-slate-700 dark:text-slate-200">
                {{ t('cert_bundleFile') }}
                <input
                  type="file"
                  accept=".p12,.pfx,application/x-pkcs12"
                  class="mt-2 block min-h-11 w-full cursor-pointer rounded-md border border-slate-300 bg-white px-3 py-2 text-xs text-slate-700 file:mr-3 file:cursor-pointer file:rounded file:border-0 file:bg-slate-100 file:px-3 file:py-1 file:text-xs file:font-semibold focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-slate-600 dark:bg-slate-950 dark:text-slate-200 dark:file:bg-slate-800"
                  @change="setImportFile('bundle', $event)"
                />
              </label>
              <label class="block text-xs font-semibold text-slate-700 dark:text-slate-200">
                {{ t('cert_bundlePassword') }}
                <input
                  v-model="importPassword"
                  type="password"
                  autocomplete="off"
                  class="mt-2 min-h-11 w-full rounded-md border border-slate-300 bg-white px-3 text-sm text-slate-800 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-slate-600 dark:bg-slate-950 dark:text-slate-100"
                  @input="clearImportValidation"
                />
              </label>
            </div>

            <div v-if="importValidation" class="rounded-md border border-emerald-200 bg-emerald-50/70 p-4 text-xs text-slate-700 dark:border-emerald-700/40 dark:bg-emerald-900/10 dark:text-slate-200">
              <div class="flex items-center gap-2 font-semibold text-emerald-700 dark:text-emerald-300">
                <span class="material-symbols-outlined text-[18px]">verified</span>
                {{ t('cert_validationPassed') }}
              </div>
              <dl class="mt-3 grid gap-x-4 gap-y-2 sm:grid-cols-[8rem_1fr]">
                <dt class="text-slate-500">{{ t('cert_subject') }}</dt><dd class="break-all">{{ importValidation.subject }}</dd>
                <dt class="text-slate-500">{{ t('cert_fingerprint') }}</dt><dd class="break-all font-mono">{{ importValidation.fingerprint }}</dd>
                <dt class="text-slate-500">{{ t('cert_keyAlgorithm') }}</dt><dd>{{ importValidation.key_algorithm }}</dd>
                <dt class="text-slate-500">{{ t('cert_systemTrust') }}</dt><dd>{{ importValidation.is_trusted ? t('cert_trusted') : t('cert_notTrusted') }}</dd>
              </dl>
              <ul v-if="importValidation.warnings?.length" class="mt-3 space-y-1 text-amber-700 dark:text-amber-300">
                <li v-for="warning in importValidation.warnings" :key="warning">{{ warningLabel(warning) }}</li>
              </ul>
            </div>

            <div class="flex flex-col gap-2 sm:flex-row sm:justify-end">
              <button
                type="button"
                :disabled="importBusy || !canValidateImport"
                class="min-h-11 cursor-pointer rounded-md border border-slate-300 px-4 text-xs font-semibold text-slate-700 transition-colors duration-200 hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-200 dark:hover:bg-slate-800"
                @click="validateImport"
              >
                {{ importBusy ? t('cert_validating') : t('cert_validatePair') }}
              </button>
              <button
                type="button"
                :disabled="importBusy || !importValidation"
                class="min-h-11 cursor-pointer rounded-md bg-primary px-4 text-xs font-semibold text-white transition-colors duration-200 hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
                @click="activateImport"
              >
                <span class="material-symbols-outlined mr-1 align-middle text-[16px]">publish</span>
                {{ t('cert_activateCustom') }}
              </button>
            </div>
          </div>
        </section>

        <!-- Generate Result -->
        <div
          v-if="generateResult"
          class="p-4 rounded-lg bg-amber-50/70 dark:bg-amber-900/10 border border-amber-200 dark:border-amber-700/40"
        >
          <div class="text-sm font-semibold text-amber-700 dark:text-amber-300 mb-2">{{ t('cert_generateResult') }}</div>
          <div class="text-xs text-slate-700 dark:text-slate-300 space-y-1">
            <div v-if="generateResult.cn"><strong>{{ t('cert_cn') }}</strong> {{ generateResult.cn }}</div>
            <div v-if="generateResult.issuer"><strong>{{ t('cert_issuerLabel') }}</strong> {{ generateResult.issuer }}</div>
            <div><strong>{{ t('cert_validYears') }}</strong> {{ generateResult.valid_years ?? '-' }}</div>
            <div v-if="generateResult.cert_path"><strong>{{ t('cert_certPath') }}</strong> <code class="break-all">{{ generateResult.cert_path }}</code></div>
            <div v-if="generateResult.key_path"><strong>{{ t('cert_keyPath') }}</strong> <code class="break-all">{{ generateResult.key_path }}</code></div>
          </div>
        </div>

        <!-- Reinstall Section -->
        <div class="p-4 rounded-lg bg-sky-50/70 dark:bg-sky-900/10 border border-sky-200 dark:border-sky-700/40 space-y-3">
          <div class="flex items-center justify-between gap-3">
            <div>
              <div class="text-sm font-semibold text-sky-700 dark:text-sky-300">{{ t('cert_reinstall') }}</div>
              <div class="text-xs text-slate-600 dark:text-slate-400">{{ t('cert_reinstallDesc') }}</div>
            </div>
            <button
              type="button"
              :disabled="operationBusy"
              class="min-h-9 flex items-center justify-center gap-1.5 px-2.5 py-1.5 border border-sky-300 dark:border-sky-600 rounded-md text-xs text-sky-700 dark:text-sky-300 hover:bg-sky-100/70 dark:hover:bg-sky-900/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              @click="startReinstall"
            >
              <span class="material-symbols-outlined text-[16px] leading-none text-sky-500">restart_alt</span>
              <span>{{ t('cert_startReinstall') }}</span>
            </button>
          </div>
        </div>

        <!-- Operation Audit -->
        <div class="p-4 rounded-lg bg-slate-50/70 dark:bg-slate-800/40 border border-slate-200 dark:border-slate-700">
          <div class="text-xs font-semibold text-slate-600 dark:text-slate-300 mb-2">{{ t('cert_lastOperation') }}</div>
          <div v-if="lastAudit" class="text-xs text-slate-500 dark:text-slate-400 space-y-0.5">
            <div><strong>{{ t('cert_operation') }}</strong> {{ lastAudit.operation }}</div>
            <div><strong>{{ t('cert_result') }}</strong> {{ lastAudit.ok ? t('cert_success') : t('cert_failed') }}</div>
            <div><strong>{{ t('cert_info') }}</strong> {{ lastAudit.message }}</div>
            <div><strong>{{ t('cert_time') }}</strong> {{ lastAudit.time }}</div>
          </div>
          <div v-else class="text-xs text-slate-500 dark:text-slate-400">{{ t('cert_noRecord') }}</div>
        </div>

        <!-- Feedback Message -->
        <div
          v-if="feedback"
          class="p-3 rounded border text-xs"
          :class="feedback.type === 'error'
            ? 'border-red-200 dark:border-red-700/40 bg-red-50/60 dark:bg-red-900/10 text-red-600 dark:text-red-300'
            : 'border-emerald-200 dark:border-emerald-700/40 bg-emerald-50/60 dark:bg-emerald-900/10 text-emerald-700 dark:text-emerald-300'"
        >
          {{ feedback.message }}
        </div>
      </div>

    </div>

    <!-- Reinstall Progress Overlay -->
    <div
      v-if="progress.visible"
      class="absolute inset-0 z-20 flex items-center justify-center bg-white/80 dark:bg-slate-900/80 backdrop-blur-sm rounded-xl"
    >
      <div class="w-full max-w-sm p-6 text-center space-y-4">
        <div class="inline-flex size-14 items-center justify-center rounded-full bg-sky-100 dark:bg-sky-900/30">
          <span
            class="size-7 border-[3px] border-sky-200 border-t-sky-600 rounded-full animate-spin"
          ></span>
        </div>
        <div>
          <div class="text-sm font-bold text-slate-700 dark:text-slate-200">{{ progress.title }}</div>
          <div class="text-xs text-slate-400 mt-1">{{ progress.detail }}</div>
        </div>
        <div class="space-y-2 text-left">
          <div
            v-for="(step, idx) in progress.steps"
            :key="idx"
            class="flex items-center gap-2.5 px-3 py-2 rounded-lg border text-xs"
            :class="stepStatusClass(step)"
          >
            <span
              v-if="step.state === 'running'"
              class="inline-block size-3.5 border-2 border-slate-300 border-t-primary rounded-full animate-spin shrink-0"
            ></span>
            <span v-else-if="step.state === 'done'" class="material-symbols-outlined text-[16px] text-emerald-500 shrink-0">check_circle</span>
            <span v-else-if="step.state === 'error'" class="material-symbols-outlined text-[16px] text-red-500 shrink-0">cancel</span>
            <span v-else class="inline-block size-3.5 border border-slate-200 rounded-full shrink-0"></span>
            <span :class="{ 'text-slate-400': step.state === 'pending' }">{{ step.label }}</span>
            <span v-if="step.message" class="ml-auto text-slate-400 truncate max-w-[140px]">{{ step.message }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref, onMounted, onUnmounted, watch } from 'vue';
import { useCertStatus } from '../composables/useCertStatus';
import { useI18n } from '../i18n';

const API_BASE = '/api';
const CERT_TYPE = 'mitm-ca';
const AUDIT_KEY = 'cert-operation-audit-v1';

const { t } = useI18n();

const props = defineProps({
  modelValue: { type: Boolean, default: false }
});
const emit = defineEmits(['update:modelValue']);

const { certStatus, loading: certLoading, error: statusError, fetchStatus, startPolling, stopPolling, invalidateCache } = useCertStatus();

const busy = ref(false);
const downloading = ref(false);
const generateResult = ref(null);
const feedback = ref(null);
const lastRefreshed = ref('-');
const lastAudit = ref(null);
const importMode = ref('pem');
const importPassword = ref('');
const importBusy = ref(false);
const importValidation = ref(null);
const importFiles = reactive({ certificate: null, privateKey: null, bundle: null });
const importModes = [
  { value: 'pem', labelKey: 'cert_formatPem' },
  { value: 'pkcs12', labelKey: 'cert_formatPkcs12' }
];

const canValidateImport = computed(() => importMode.value === 'pem'
  ? Boolean(importFiles.certificate && importFiles.privateKey)
  : Boolean(importFiles.bundle));
const operationBusy = computed(() => busy.value || importBusy.value);

const progress = reactive({
  visible: false,
  title: '',
  detail: '',
  steps: []
});

let feedbackTimer = null;
let reinstallPollTimer = null;

function loadAudit() {
  try {
    const raw = localStorage.getItem(AUDIT_KEY);
    if (raw) lastAudit.value = JSON.parse(raw);
  } catch (_) {}
}

function saveAudit(operation, ok, message) {
  const entry = { operation, ok, message, time: new Date().toLocaleString() };
  lastAudit.value = entry;
  try { localStorage.setItem(AUDIT_KEY, JSON.stringify(entry)); } catch (_) {}
}

function showFeedback(message, type = 'success') {
  if (feedbackTimer) clearTimeout(feedbackTimer);
  feedback.value = { message, type };
  feedbackTimer = setTimeout(() => { feedback.value = null; }, 6000);
}

function updateRefreshed() {
  lastRefreshed.value = new Date().toLocaleString();
}

function clearResults() {
  generateResult.value = null;
}

async function apiCall(method, path, body) {
  const opts = { method, headers: { 'Content-Type': 'application/json', 'X-Aliang-Local-Request': '1' } };
  if (body) opts.body = JSON.stringify(body);
  const res = await fetch(`${API_BASE}${path}`, opts);
  if (!res.ok) {
    let msg = t('cert_requestFailed', { status: res.status });
    try {
      const data = await res.json();
      msg = data?.data?.details?.error || data?.data?.error_msg || data?.msg || data?.message || msg;
    } catch (_) {}
    throw new Error(msg);
  }
  if (method === 'GET' && path.includes('/download')) return res;
  const json = await res.json();
  return json.data || json;
}

function sourceLabel(source) {
  const labels = {
    imported: t('cert_sourceImported'),
    generated: t('cert_sourceGenerated'),
    legacy: t('cert_sourceLegacy')
  };
  return labels[source] || source || '-';
}

function warningLabel(warning) {
  const labels = {
    expires_soon: t('cert_warningExpiresSoon'),
    weak_signature_algorithm: t('cert_warningWeakSignature'),
    name_constraints_present: t('cert_warningNameConstraints')
  };
  return labels[warning] || warning;
}

function clearImportValidation() {
  importValidation.value = null;
}

function setImportMode(mode) {
  importMode.value = mode;
  importPassword.value = '';
  importFiles.certificate = null;
  importFiles.privateKey = null;
  importFiles.bundle = null;
  clearImportValidation();
}

function setImportFile(field, event) {
  importFiles[field] = event.target.files?.[0] || null;
  clearImportValidation();
}

function buildImportFormData() {
  const form = new FormData();
  if (importMode.value === 'pem') {
    form.append('certificate', importFiles.certificate);
    form.append('private_key', importFiles.privateKey);
  } else {
    form.append('bundle', importFiles.bundle);
    form.append('password', importPassword.value);
  }
  return form;
}

async function importRequest(path) {
  const response = await fetch(`${API_BASE}${path}`, {
    method: 'POST',
    headers: { 'X-Aliang-Local-Request': '1' },
    body: buildImportFormData()
  });
  const payload = await response.json().catch(() => null);
  if (!response.ok || payload?.code !== 0) {
    throw new Error(payload?.data?.details?.error || payload?.data?.error_msg || payload?.msg || t('cert_importFailed'));
  }
  return payload.data || {};
}

async function validateImport() {
  if (!canValidateImport.value) return;
  importBusy.value = true;
  clearImportValidation();
  try {
    importValidation.value = await importRequest('/cert/import/validate');
    showFeedback(t('cert_validationPassed'));
  } catch (err) {
    showFeedback(t('cert_validationFailed') + ': ' + err.message, 'error');
  } finally {
    importBusy.value = false;
  }
}

async function activateImport() {
  if (!importValidation.value || !confirm(t('cert_confirmActivateCustom'))) return;
  importBusy.value = true;
  try {
    const result = await importRequest('/cert/import');
    showFeedback(result.is_trusted ? t('cert_importTrustedSuccess') : t('cert_importSuccess'));
    saveAudit(t('cert_importTitle'), true, result.fingerprint || '');
    importValidation.value = result;
    invalidateCache();
    await checkStatus();
  } catch (err) {
    showFeedback(t('cert_importFailed') + ': ' + err.message, 'error');
    saveAudit(t('cert_importTitle'), false, err.message);
  } finally {
    importBusy.value = false;
  }
}

async function rollbackCertificate() {
  if (!confirm(t('cert_confirmRollback'))) return;
  importBusy.value = true;
  try {
    const result = await apiCall('POST', '/cert/rollback', {});
    showFeedback(t('cert_rollbackSuccess'));
    saveAudit(t('cert_rollback'), true, result.fingerprint || '');
    clearImportValidation();
    invalidateCache();
    await checkStatus();
  } catch (err) {
    showFeedback(t('cert_rollbackFailed') + ': ' + err.message, 'error');
  } finally {
    importBusy.value = false;
  }
}

async function checkStatus() {
  await fetchStatus();
  updateRefreshed();
}

async function downloadCertFile() {
  downloading.value = true;
  try {
    const res = await fetch(`${API_BASE}/cert/download?cert_type=${encodeURIComponent(CERT_TYPE)}`);
    if (!res.ok) {
      let msg = t('cert_downloadFailed');
      const ct = res.headers.get('content-type') || '';
      if (ct.includes('application/json')) {
        const err = await res.json();
        msg = err?.msg || err?.message || msg;
      }
      throw new Error(msg);
    }
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `${CERT_TYPE}.pem`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
    showFeedback(t('cert_downloadSuccess'));
    saveAudit(t('cert_downloadPem'), true, `${CERT_TYPE}.pem`);
  } catch (err) {
    showFeedback(t('cert_downloadFailed') + ': ' + err.message, 'error');
    saveAudit(t('cert_downloadPem'), false, err.message);
  } finally {
    downloading.value = false;
  }
}

async function installCert() {
  busy.value = true;
  try {
    await apiCall('POST', '/cert/install', { cert_type: CERT_TYPE });
    showFeedback(t('cert_installSuccess'));
    saveAudit(t('cert_installToSystem'), true, CERT_TYPE);
    await checkStatus();
  } catch (err) {
    showFeedback(t('cert_installFailed') + ': ' + err.message, 'error');
    saveAudit(t('cert_installToSystem'), false, err.message);
  } finally {
    busy.value = false;
  }
}

async function removeCert() {
  if (!confirm(t('cert_confirmRemove'))) return;
  busy.value = true;
  try {
    await apiCall('POST', '/cert/remove', { cert_type: CERT_TYPE });
    showFeedback(t('cert_removed'));
    saveAudit(t('cert_removeCert'), true, CERT_TYPE);
    await checkStatus();
  } catch (err) {
    showFeedback(t('cert_removeFailed') + ': ' + err.message, 'error');
    saveAudit(t('cert_removeCert'), false, err.message);
  } finally {
    busy.value = false;
  }
}

async function generateCert() {
  if (!confirm(t('cert_confirmRegenerate'))) return;
  busy.value = true;
  clearResults();
  try {
    const data = await apiCall('POST', '/cert/generate', { cert_type: CERT_TYPE });
    generateResult.value = data;
    showFeedback(t('cert_generated'));
    saveAudit(t('cert_regenerateCert'), true, CERT_TYPE);
    await checkStatus();
  } catch (err) {
    showFeedback(t('cert_generateFailed') + ': ' + err.message, 'error');
    saveAudit(t('cert_regenerateCert'), false, err.message);
  } finally {
    busy.value = false;
  }
}

function stepStatusClass(step) {
  switch (step.state) {
    case 'running':
      return 'border-primary/40 bg-primary/5 dark:border-primary/30 dark:bg-primary/5';
    case 'done':
      return 'border-emerald-200 bg-emerald-50 dark:border-emerald-700/30 dark:bg-emerald-900/10';
    case 'error':
      return 'border-red-200 bg-red-50 dark:border-red-700/30 dark:bg-red-900/10';
    default:
      return 'border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/30';
  }
}

function setStepState(steps, index, state, message) {
  if (steps[index]) {
    steps[index].state = state;
    steps[index].message = message || '';
  }
}

function silentStatusPoll() {
  return fetch(`${API_BASE}/cert/status?cert_type=${encodeURIComponent(CERT_TYPE)}`)
    .then(r => r.ok ? r.json().then(j => j.data || j) : null)
    .catch(() => null);
}

function stopReinstallPoll() {
  if (reinstallPollTimer) { clearInterval(reinstallPollTimer); reinstallPollTimer = null; }
}

async function startReinstall() {
  busy.value = true;
  stopReinstallPoll();

  const steps = [
    { label: t('cert_stepRegenerate'), state: 'pending', message: '' },
    { label: t('cert_stepInstall'), state: 'pending', message: '' },
    { label: t('cert_stepRemoveOld'), state: 'pending', message: '' },
    { label: t('cert_stepVerify'), state: 'pending', message: '' }
  ];

  progress.visible = true;
  progress.title = t('cert_reinstallProgress');
  progress.detail = t('cert_localCert');
  progress.steps = steps;

  const finalStatus = { success: false };

  // Start background polling (separate from composable polling)
  reinstallPollTimer = setInterval(async () => {
    const status = await silentStatusPoll();
    if (status) {
      certStatus.value = status;
      invalidateCache();
    }
  }, 1500);

  // The backend performs trust-first rotation atomically: install and verify the
  // new CA, activate it, then remove the old CA by fingerprint.
  setStepState(steps, 0, 'running', t('cert_generating'));
  try {
    await apiCall('POST', '/cert/generate', { cert_type: CERT_TYPE });
    setStepState(steps, 0, 'done', t('cert_generateSuccess'));
    setStepState(steps, 1, 'done', t('cert_installSuccess2'));
    setStepState(steps, 2, 'done', t('cert_removedOrNotExist'));
  } catch (err) {
    setStepState(steps, 0, 'error', err.message);
    finalStatus.success = false;
    finishReinstall(steps, false, t('cert_generateCertFailed'));
    return;
  }

  // Verify the exact active certificate is both present and trusted.
  setStepState(steps, 3, 'running', t('cert_verifying'));
  try {
    const status = await apiCall('GET', `/cert/status?cert_type=${encodeURIComponent(CERT_TYPE)}`);
    certStatus.value = status;
    invalidateCache();
    updateRefreshed();
    if (status.is_installed && status.is_trusted) {
      setStepState(steps, 3, 'done', t('cert_installedAndTrusted'));
      finishReinstall(steps, true, t('cert_success'));
    } else {
      setStepState(steps, 3, 'error', t('cert_installedNotDetected'));
      finishReinstall(steps, false, t('cert_verifyFailed'));
    }
  } catch (err) {
    setStepState(steps, 3, 'error', err.message);
    finishReinstall(steps, false, t('cert_verifyFailed') + ': ' + err.message);
  }
}

function finishReinstall(steps, success, message) {
  stopReinstallPoll();

  if (success) {
    setTimeout(() => {
      progress.visible = false;
      busy.value = false;
      showFeedback(t('cert_reinstallComplete', { msg: message }));
      saveAudit(t('cert_reinstall'), true, message);
      checkStatus();
    }, 800);
  } else {
    for (let i = 0; i < steps.length; i++) {
      if (steps[i].state === 'running') {
        steps[i].state = 'pending';
        steps[i].message = '';
      }
    }
    progress.title = t('cert_reinstallFailed');
    progress.detail = t('cert_localCert');

    setTimeout(() => {
      progress.visible = false;
      busy.value = false;
      showFeedback(t('cert_reinstallFailedMsg', { msg: message }), 'error');
      saveAudit(t('cert_reinstall'), false, message);
      checkStatus();
    }, 2500);
  }
}

function open() {
  loadAudit();
  startPolling();
  emit('update:modelValue', true);
}

function close() {
  if (operationBusy.value) return;
  stopPolling();
  emit('update:modelValue', false);
}

function onKeydown(e) {
  if (e.key === 'Escape' && props.modelValue && !operationBusy.value) {
    close();
  }
}

watch(() => props.modelValue, (val) => {
  if (val) {
    loadAudit();
    startPolling();
  } else {
    if (!operationBusy.value) stopPolling();
  }
});

onMounted(() => {
  document.addEventListener('keydown', onKeydown);
});

onUnmounted(() => {
  stopReinstallPoll();
  if (feedbackTimer) clearTimeout(feedbackTimer);
  document.removeEventListener('keydown', onKeydown);
});

defineExpose({ open, close, startReinstall });
</script>
