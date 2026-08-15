<template>
  <div class="settings-pane" data-pane="config-sync">
    <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
      <div class="flex items-center justify-between mb-4 gap-3 flex-wrap">
        <h3 class="mb-0 flex items-center gap-2 font-bold">
          <span class="material-symbols-outlined text-primary">cloud_sync</span>
          {{ t('sync_title') }}
        </h3>
        <div class="flex items-center gap-2">
          <button type="button" class="settings-btn-outline" @click="loadConfigs">{{ t('sync_refresh') }}</button>
          <button type="button" class="settings-btn-primary" :disabled="pushing" @click="pushSelectedToCloud">
            {{ pushing ? t('sync_pushing') : t('sync_pushSelected') }}
          </button>
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
        <input
          v-model="filters.software"
          class="rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          type="text"
          :placeholder="t('sync_softwarePh')"
          @keydown.enter.prevent="loadConfigs"
        />
        <div class="flex gap-2">
          <input
            v-model="cloud.cloudUrl"
            class="flex-1 rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            type="text"
            :placeholder="t('sync_cloudUrlPh')"
          />
          <button type="button" class="settings-btn-outline" :disabled="comparing" @click="compareWithCloud">
            {{ comparing ? t('sync_comparing') : t('sync_compareBtn') }}
          </button>
        </div>
        <input
          v-model="cloud.authToken"
          class="rounded border border-slate-300 px-3 py-2 md:col-span-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          type="text"
          :placeholder="t('sync_cloudTokenPh')"
        />
      </div>

      <div class="overflow-auto rounded-lg border border-slate-200 dark:border-slate-800">
        <table class="w-full text-sm">
          <thead class="bg-slate-50 dark:bg-slate-800/50">
            <tr>
              <th class="text-left p-2">{{ t('sync_colSelect') }}</th>
              <th class="text-left p-2">{{ t('sync_colSoftware') }}</th>
              <th class="text-left p-2">{{ t('sync_colConfigName') }}</th>
              <th class="text-left p-2">{{ t('sync_colPath') }}</th>
              <th class="text-left p-2">{{ t('sync_colVersion') }}</th>
              <th class="text-left p-2">{{ t('sync_colUpdated') }}</th>
              <th class="text-left p-2">{{ t('sync_colCompare') }}</th>
              <th class="text-left p-2">{{ t('sync_colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in items" :key="item.uuid" class="border-t border-slate-100 dark:border-slate-800">
              <td class="p-2">
                <input
                  type="checkbox"
                  :checked="!!item.selected"
                  @change="toggleSelect(item, $event.target.checked)"
                />
              </td>
              <td class="p-2">{{ item.software }}</td>
              <td class="p-2">{{ item.name }}</td>
              <td class="p-2 truncate max-w-[240px]" :title="item.file_path">{{ item.file_path }}</td>
              <td class="p-2">{{ item.version || '-' }}</td>
              <td class="p-2">{{ item.updated_at || '-' }}</td>
              <td class="p-2">
                <span
                  class="text-xs px-2 py-0.5 rounded"
                  :class="freshnessClass(item.freshness_status)"
                >
                  {{ freshnessLabel(item.freshness_status) }}
                </span>
              </td>
              <td class="p-2">
                <div class="flex gap-2 flex-wrap">
                  <button type="button" class="settings-btn-outline !py-1 !px-2" @click="applyItem(item)">{{ t('sync_apply') }}</button>
                  <button type="button" class="settings-btn-outline !py-1 !px-2" @click="copyContent(item)">{{ t('sync_copy') }}</button>
                  <button type="button" class="settings-btn-outline !py-1 !px-2" @click="removeConfig(item)">{{ t('sync_delete') }}</button>
                </div>
              </td>
            </tr>
            <tr v-if="!items.length">
              <td class="p-4 text-slate-500 dark:text-slate-400" colspan="8">{{ t('sync_noData') }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="mt-4 grid grid-cols-1 md:grid-cols-2 gap-3">
        <input v-model="editor.software" class="rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100" :placeholder="t('sync_softwareNamePh')" type="text" />
        <input v-model="editor.name" class="rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100" :placeholder="t('sync_configNamePh')" type="text" />
        <input v-model="editor.filePath" class="rounded border border-slate-300 px-3 py-2 md:col-span-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100" :placeholder="t('sync_configPathPh')" type="text" />
        <input v-model="editor.version" class="rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100" :placeholder="t('sync_versionPh')" type="text" />
        <select v-model="editor.format" class="rounded border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100">
          <option value="json">json</option>
          <option value="yaml">yaml</option>
        </select>
        <div class="md:col-span-2 overflow-hidden rounded border border-slate-300 dark:border-slate-700">
          <CodeMirror
            v-model="editor.content"
            class="text-sm"
            :extensions="editorExtensions"
            :style="{ minHeight: '220px' }"
            :basic-setup="basicSetup"
          />
        </div>
      </div>

      <div class="mt-3 flex justify-end gap-2">
        <button type="button" class="settings-btn-secondary" @click="resetEditor">{{ t('sync_reset') }}</button>
        <button type="button" class="settings-btn-primary" :disabled="saving" @click="saveConfig">
          {{ saving ? t('sync_saving') : t('sync_saveConfig') }}
        </button>
      </div>

      <div class="mt-3 text-sm text-slate-500 dark:text-slate-400">{{ status }}</div>
    </div>
  </div>
</template>

<script setup>
import { computed, reactive, ref } from 'vue';
import { Codemirror as CodeMirror } from 'vue-codemirror';
import { json } from '@codemirror/lang-json';
import { yaml } from '@codemirror/lang-yaml';
import { oneDark } from '@codemirror/theme-one-dark';
import { useI18n } from '../../i18n';

const { t } = useI18n();

const items = ref([]);
const status = ref(t('sync_ready'));
const pushing = ref(false);
const saving = ref(false);
const comparing = ref(false);

const filters = reactive({ software: '' });
const cloud = reactive({ cloudUrl: '', authToken: '' });
const editor = reactive({
  uuid: '',
  software: '',
  name: '',
  filePath: '',
  version: 'v1',
  format: 'json',
  content: '{}',
});

const basicSetup = {
  lineNumbers: true,
  foldGutter: true,
  highlightActiveLineGutter: true,
  highlightActiveLine: true,
};

const editorExtensions = computed(() => {
  const modeExtension = editor.format === 'yaml' ? yaml() : json();
  return [modeExtension, oneDark];
});

function normalizeApi(payload) {
  return payload?.data ?? payload;
}

async function request(path, options = {}) {
  const resp = await fetch(`/api${path}`, {
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
    ...options,
  });
  const payload = await resp.json();
  if (!resp.ok || payload.code !== 0) {
    throw new Error(payload.msg || t('sync_requestFailed'));
  }
  return normalizeApi(payload);
}

function resetEditor() {
  editor.uuid = '';
  editor.software = filters.software || '';
  editor.name = '';
  editor.filePath = '';
  editor.version = 'v1';
  editor.format = 'json';
  editor.content = '{}';
}

function freshnessClass(statusValue) {
  switch (statusValue) {
    case 'local_newer':
      return 'bg-emerald-100 text-emerald-700';
    case 'cloud_newer':
      return 'bg-amber-100 text-amber-700';
    case 'same':
      return 'bg-slate-100 text-slate-700';
    case 'local_only':
      return 'bg-blue-100 text-blue-700';
    case 'cloud_only':
      return 'bg-purple-100 text-purple-700';
    default:
      return 'bg-slate-100 text-slate-500';
  }
}

function freshnessLabel(statusValue) {
  switch (statusValue) {
    case 'local_newer':
      return t('sync_localNewer');
    case 'cloud_newer':
      return t('sync_cloudNewer');
    case 'same':
      return t('sync_same');
    case 'local_only':
      return t('sync_localOnly');
    case 'cloud_only':
      return t('sync_cloudOnly');
    default:
      return '-';
  }
}

async function loadConfigs() {
  const query = filters.software.trim()
    ? `?software=${encodeURIComponent(filters.software.trim())}`
    : '';
  const data = await request(`/software-config/list${query}`, { method: 'GET' });
  items.value = (data.items || []).map((item) => ({ ...item, freshness_status: item.freshness_status || '' }));
  status.value = t('sync_loaded', { count: items.value.length });
}

function fillEditor(item) {
  editor.uuid = item.uuid || '';
  editor.software = item.software || '';
  editor.name = item.name || '';
  editor.filePath = item.file_path || '';
  editor.version = item.version || 'v1';
  editor.format = item.format || 'json';
  editor.content = item.content || '{}';
}

async function saveConfig() {
  if (!editor.software.trim() || !editor.name.trim() || !editor.filePath.trim() || !editor.content.trim()) {
    status.value = t('sync_fieldsRequired');
    return;
  }

  saving.value = true;
  try {
    const data = await request('/software-config/save', {
      method: 'POST',
      body: JSON.stringify({
        uuid: editor.uuid,
        software: editor.software.trim(),
        name: editor.name.trim(),
        file_path: editor.filePath.trim(),
        version: editor.version.trim(),
        format: editor.format,
        content: editor.content,
      }),
    });
    editor.uuid = data.uuid;
    await request('/software-config/log', {
      method: 'POST',
      body: JSON.stringify({
        action: 'frontend_save',
        software: data.software,
        config_uuid: data.uuid,
        config_name: data.name,
        detail: 'saved from config sync settings',
      }),
    });
    await loadConfigs();
    status.value = t('sync_saved');
  } catch (error) {
    status.value = t('sync_saveFailed', { msg: error.message });
  } finally {
    saving.value = false;
  }
}

async function toggleSelect(item, selected) {
  try {
    await request('/software-config/select', {
      method: 'POST',
      body: JSON.stringify({ uuid: item.uuid, selected }),
    });
    item.selected = selected;
    await request('/software-config/log', {
      method: 'POST',
      body: JSON.stringify({
        action: 'frontend_select',
        software: item.software,
        config_uuid: item.uuid,
        config_name: item.name,
        detail: `selected=${selected}`,
      }),
    });
  } catch (error) {
    status.value = t('sync_selectFailed', { msg: error.message });
  }
}

async function copyContent(item) {
  try {
    await navigator.clipboard.writeText(item.content || '');
    await request('/software-config/log', {
      method: 'POST',
      body: JSON.stringify({
        action: 'frontend_copy',
        software: item.software,
        config_uuid: item.uuid,
        config_name: item.name,
        detail: 'copied config content',
      }),
    });
    fillEditor(item);
    status.value = t('sync_copied', { name: item.name });
  } catch (error) {
    status.value = t('sync_copyFailed', { msg: error.message });
  }
}

async function applyItem(item) {
  try {
    await request('/software-config/activate', {
      method: 'POST',
      body: JSON.stringify({
        uuid: item.uuid,
        software: item.software,
        name: item.name,
        file_path: item.file_path,
        version: item.version,
        format: item.format,
        content: item.content,
      }),
    });
    await request('/software-config/log', {
      method: 'POST',
      body: JSON.stringify({
        action: 'frontend_apply',
        software: item.software,
        config_uuid: item.uuid,
        config_name: item.name,
        detail: `applied to ${item.file_path}`,
      }),
    });
    status.value = t('sync_applied', { name: item.name });
    await loadConfigs();
  } catch (error) {
    status.value = t('sync_applyFailed', { msg: error.message });
  }
}

async function removeConfig(item) {
  try {
    await request('/software-config/delete', {
      method: 'POST',
      body: JSON.stringify({ uuid: item.uuid }),
    });
    await loadConfigs();
    status.value = t('sync_deleted', { name: item.name });
  } catch (error) {
    status.value = t('sync_deleteFailed', { msg: error.message });
  }
}

async function compareWithCloud() {
  if (!cloud.cloudUrl.trim()) {
    status.value = t('sync_cloudUrlRequired');
    return;
  }
  comparing.value = true;
  try {
    const data = await request('/software-config/compare', {
      method: 'POST',
      body: JSON.stringify({
        cloud_url: cloud.cloudUrl.trim(),
        auth_token: cloud.authToken.trim(),
      }),
    });
    const map = new Map();
    (data.items || []).forEach((it) => {
      map.set(it.uuid, it.status);
    });
    items.value = items.value.map((it) => ({
      ...it,
      freshness_status: map.get(it.uuid) || 'local_only',
    }));
    status.value = t('sync_compareComplete', { count: data.items?.length || 0 });
  } catch (error) {
    status.value = t('sync_compareFailed', { msg: error.message });
  } finally {
    comparing.value = false;
  }
}

async function pushSelectedToCloud() {
  if (!cloud.cloudUrl.trim()) {
    status.value = t('sync_cloudUrlRequired');
    return;
  }
  pushing.value = true;
  try {
    const data = await request('/software-config/cloud/push-selected', {
      method: 'POST',
      body: JSON.stringify({
        cloud_url: cloud.cloudUrl.trim(),
        auth_token: cloud.authToken.trim(),
      }),
    });
    status.value = t('sync_pushedCount', { count: data.synced_count || 0, time: data.last_synced_at || '' });
  } catch (error) {
    status.value = t('sync_pushFailed', { msg: error.message });
  } finally {
    pushing.value = false;
  }
}

loadConfigs().catch((error) => {
  status.value = t('sync_initFailed', { msg: error.message });
});
</script>
