<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <span class="material-symbols-outlined">dns</span>
        </div>
        <div class="min-w-0">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_mcp_registry_title') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('agent_mcp_registry_hint') }}
          </p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-11 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
        @click="openCreate"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        {{ t('agent_mcp_registry_add') }}
      </button>
    </div>

    <!-- 卡片级警示/错误条：codex 同步失败 / 行动失败（保存/启停/删除） -->
    <div
      v-if="codexSyncError"
      class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-xs leading-5 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300"
    >{{ codexSyncError }}</div>
    <div
      v-if="actionError"
      class="mt-4 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs leading-5 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ actionError }}</div>

    <div v-if="loading" class="mt-5 flex min-h-24 items-center justify-center text-xs text-slate-500 dark:text-slate-400">
      <span class="material-symbols-outlined mr-2 text-base motion-safe:animate-spin">progress_activity</span>
      {{ t('agent_integrations_loading') }}
    </div>
    <div
      v-else-if="loadError"
      class="mt-5 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ loadError }}</div>

    <!-- 注册表原文视角：enabled=false 的条目也展示（便于重新启用），不从集成清单派生 -->
    <template v-else>
      <ul v-if="rows.length" class="mt-5 divide-y divide-slate-100 dark:divide-slate-800">
        <li v-for="row in rows" :key="row.name" class="flex flex-wrap items-center gap-2 py-2.5">
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-1.5">
              <span class="truncate text-xs font-semibold text-slate-800 dark:text-slate-100">{{ row.name }}</span>
              <span class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold uppercase text-slate-500 dark:bg-slate-700/40 dark:text-slate-400">
                {{ row.entry.type }}
              </span>
              <span
                class="rounded px-1.5 py-0.5 text-[10px] font-bold"
                :class="row.sourceWarn
                  ? 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300'
                  : 'bg-primary/10 text-primary'"
              >{{ row.sourceLabel }}</span>
            </div>
            <p class="mt-1 break-all text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ row.endpoint }}</p>
            <p v-if="row.sourceWarn" class="mt-0.5 text-[10px] leading-4 text-amber-600 dark:text-amber-400">
              {{ t('agent_mcp_registry_shadow_warning') }}
            </p>
          </div>
          <label class="flex min-h-10 shrink-0 cursor-pointer items-center gap-2 py-2 text-xs font-semibold text-slate-600 dark:text-slate-300">
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
              :checked="row.enabled"
              @change="toggleEnabled(row, $event)"
            >
            {{ t('agent_mcp_registry_enabled') }}
          </label>
          <div class="flex shrink-0 items-center gap-1.5">
            <button
              type="button"
              class="inline-flex h-10 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              @click="openEdit(row)"
            >{{ t('agent_mcp_registry_edit') }}</button>
            <button
              type="button"
              class="inline-flex h-10 items-center rounded-lg border border-rose-200 px-2.5 text-xs font-semibold text-rose-600 transition-colors hover:bg-rose-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-300/40 dark:border-rose-500/30 dark:text-rose-300 dark:hover:bg-rose-500/10"
              @click="removeRow(row)"
            >{{ t('agent_mcp_registry_delete') }}</button>
          </div>
        </li>
      </ul>
      <p v-else class="mt-5 text-xs text-slate-400">{{ t('agent_mcp_registry_empty') }}</p>
    </template>

    <!-- 新增/编辑弹窗（对照 QuickSetupModal 二级弹窗形态） -->
    <div
      v-if="formOpen"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('agent_mcp_registry_title')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeForm"></div>
      <div class="relative z-10 max-h-[88dvh] w-full max-w-md overflow-y-auto rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('agent_mcp_registry_title') }}</h4>

        <div class="mt-4 space-y-3">
          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_name') }}</span>
            <input
              v-model.trim="form.name"
              type="text"
              data-field="name"
              class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              :disabled="editingName !== null"
            >
          </label>

          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_type') }}</span>
            <select
              v-model="form.type"
              data-field="type"
              class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            >
              <option value="stdio">stdio</option>
              <option value="http">http</option>
              <option value="sse">sse</option>
            </select>
          </label>

          <template v-if="form.type === 'stdio'">
            <label class="block">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_command') }}</span>
              <input
                v-model="form.command"
                type="text"
                data-field="command"
                class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              >
            </label>
            <label class="block">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_args') }}</span>
              <textarea
                v-model="form.argsText"
                rows="3"
                data-field="args"
                class="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              ></textarea>
            </label>
            <label class="block">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_env') }}</span>
              <textarea
                v-model="form.envText"
                rows="3"
                data-field="env"
                class="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              ></textarea>
            </label>
          </template>

          <template v-else>
            <label class="block">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_url') }}</span>
              <input
                v-model="form.url"
                type="text"
                data-field="url"
                class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              >
            </label>
            <label class="block">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_mcp_registry_headers') }}</span>
              <textarea
                v-model="form.headersText"
                rows="3"
                data-field="headers"
                class="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              ></textarea>
            </label>
          </template>

          <label class="flex min-h-10 cursor-pointer items-center gap-2 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300">
            <input
              v-model="form.enabled"
              type="checkbox"
              data-field="enabled"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
            >
            {{ t('agent_mcp_registry_enabled') }}
          </label>
        </div>

        <div
          v-if="formError"
          class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
        >{{ formError }}</div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg border border-slate-200 px-4 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
            @click="closeForm"
          >{{ t('agent_mcp_registry_cancel') }}</button>
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
            :disabled="saving"
            @click="saveForm"
          >{{ t('agent_mcp_registry_save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import {
  deleteAliangMCPServer,
  getAgentIntegrations,
  getAliangMCPRegistry,
  putAliangMCPServer,
} from '../../services/agentApi';

// 客户端校验镜像服务端规则：名称字符集/长度 + 禁 aliang- 前缀（保留段）。
const NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/;

function emptyForm() {
  return {
    name: '',
    type: 'stdio',
    command: '',
    argsText: '',
    envText: '',
    url: '',
    headersText: '',
    enabled: true,
  };
}

// 多行文本 → 对象：env 按「首个 =」拆 KEY=VALUE；headers 按「首个 :」拆 KEY: VALUE。
export function parseKeyValueLines(text, sep) {
  const out = {};
  String(text || '')
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .forEach((line) => {
      const idx = line.indexOf(sep);
      if (idx <= 0) return;
      const key = line.slice(0, idx).trim();
      const value = line.slice(idx + sep.length).trim();
      if (key) out[key] = value;
    });
  return out;
}

// 对象 → 多行文本（env 用 KEY=VALUE，headers 用 KEY: VALUE）。
export function formatKeyValueLines(obj, sep) {
  if (!obj || typeof obj !== 'object') return '';
  return Object.entries(obj)
    .filter(([, v]) => v !== undefined && v !== null)
    .map(([k, v]) => `${k}${sep}${v}`)
    .join('\n');
}

export default {
  name: 'AgentMCPRegistrySettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      servers: {},
      integrations: [],
      loading: false,
      loadError: '',
      formOpen: false,
      editingName: null,
      form: emptyForm(),
      formError: '',
      saving: false,
      codexSyncError: '',
      actionError: '',
    };
  },
  computed: {
    rows() {
      const servers = this.servers && typeof this.servers === 'object' ? this.servers : {};
      return Object.entries(servers).map(([name, entry]) => {
        const safeEntry = entry && typeof entry === 'object' ? entry : {};
        return {
          name,
          entry: safeEntry,
          enabled: safeEntry.enabled !== false,
          endpoint: this.endpointLabel(safeEntry),
          sourceLabel: this.sourceInfo(name).label,
          sourceWarn: this.sourceInfo(name).warn,
        };
      });
    },
  },
  mounted() {
    this.loadAll();
  },
  methods: {
    endpointLabel(entry) {
      if (entry.type === 'stdio') {
        const args = Array.isArray(entry.args) ? entry.args : [];
        return [entry.command, ...args].filter(Boolean).join(' ');
      }
      return entry.url || '';
    },
    // 来源列：注册表本体恒为 aliang；命中集成清单且 source（逗号拆分）
    // 除 aliang 外还有别的来源 → 标 aliang + 附加来源并出同名遮蔽警示。
    sourceInfo(name) {
      const hit = this.integrations.find((item) => item && item.name === name);
      if (!hit) return { label: 'aliang', warn: false };
      const parts = String(hit.source || '')
        .split(',')
        .map((part) => part.trim())
        .filter(Boolean);
      if (!parts.length) return { label: 'aliang', warn: false };
      const hasAliang = parts.includes('aliang');
      const others = parts.filter((part) => part !== 'aliang');
      if (hasAliang && others.length) return { label: ['aliang', ...others].join(' + '), warn: true };
      if (hasAliang) return { label: 'aliang', warn: false };
      return { label: parts.join(' + '), warn: false };
    },
    async loadAll() {
      this.loading = true;
      this.loadError = '';
      try {
        const [registry, integrations] = await Promise.all([
          getAliangMCPRegistry(),
          getAgentIntegrations(),
        ]);
        this.servers = registry && typeof registry.servers === 'object' && registry.servers ? registry.servers : {};
        this.integrations = Array.isArray(integrations?.mcp_servers) ? integrations.mcp_servers : [];
      } catch (_) {
        this.loadError = this.t('agent_integrations_loadFailed');
      } finally {
        this.loading = false;
      }
    },
    openCreate() {
      this.editingName = null;
      this.form = emptyForm();
      this.formError = '';
      this.formOpen = true;
    },
    openEdit(row) {
      this.editingName = row.name;
      const entry = row.entry;
      this.form = {
        name: row.name,
        type: entry.type === 'http' || entry.type === 'sse' ? entry.type : 'stdio',
        command: entry.command || '',
        argsText: Array.isArray(entry.args) ? entry.args.join('\n') : '',
        envText: formatKeyValueLines(entry.env, '='),
        url: entry.url || '',
        headersText: formatKeyValueLines(entry.headers, ': '),
        enabled: entry.enabled !== false,
      };
      this.formError = '';
      this.formOpen = true;
    },
    closeForm() {
      this.formOpen = false;
      this.editingName = null;
      this.formError = '';
    },
    // 客户端校验失败不发请求（镜像服务端 400 规则，提示统一走 name_invalid）。
    validateForm() {
      const name = String(this.form.name || '');
      if (!NAME_RE.test(name) || name.startsWith('aliang-')) {
        this.formError = this.t('agent_mcp_registry_name_invalid');
        return false;
      }
      if (this.form.type === 'stdio' && !String(this.form.command || '').trim()) {
        this.formError = this.t('agent_mcp_registry_name_invalid');
        return false;
      }
      if (this.form.type !== 'stdio' && !String(this.form.url || '').trim()) {
        this.formError = this.t('agent_mcp_registry_name_invalid');
        return false;
      }
      return true;
    },
    async saveForm() {
      if (!this.validateForm()) return;
      this.saving = true;
      this.formError = '';
      try {
        const entry = { type: this.form.type, enabled: this.form.enabled };
        if (this.form.type === 'stdio') {
          entry.command = this.form.command.trim();
          const args = this.form.argsText.split('\n').map((line) => line.trim()).filter(Boolean);
          if (args.length) entry.args = args;
          const env = parseKeyValueLines(this.form.envText, '=');
          if (Object.keys(env).length) entry.env = env;
        } else {
          entry.url = this.form.url.trim();
          const headers = parseKeyValueLines(this.form.headersText, ':');
          if (Object.keys(headers).length) entry.headers = headers;
        }
        const res = await putAliangMCPServer(this.form.name, entry);
        if (res?.codex_sync?.ok === false) {
          this.codexSyncError = this.t('agent_mcp_registry_codex_sync_failed', {
            error: res.codex_sync.error || '',
          });
        } else {
          this.codexSyncError = '';
        }
        this.actionError = '';
        this.formOpen = false;
        this.editingName = null;
        await this.loadAll();
      } catch (err) {
        this.formError = this.t('agent_mcp_registry_save_failed', { error: err?.message || '' });
      } finally {
        this.saving = false;
      }
    },
    async toggleEnabled(row, event) {
      const next = event.target.checked;
      this.actionError = '';
      try {
        const res = await putAliangMCPServer(row.name, { ...row.entry, enabled: next });
        if (res?.codex_sync?.ok === false) {
          this.codexSyncError = this.t('agent_mcp_registry_codex_sync_failed', {
            error: res.codex_sync.error || '',
          });
        } else {
          this.codexSyncError = '';
        }
      } catch (err) {
        this.actionError = this.t('agent_mcp_registry_save_failed', { error: err?.message || '' });
      } finally {
        // 失败回滚视觉态 / 成功取服务端真值：两路都重拉。
        await this.loadAll();
      }
    },
    async removeRow(row) {
      if (!window.confirm(this.t('agent_mcp_registry_delete_confirm', { name: row.name }))) return;
      this.actionError = '';
      try {
        const res = await deleteAliangMCPServer(row.name);
        if (res?.codex_sync?.ok === false) {
          this.codexSyncError = this.t('agent_mcp_registry_codex_sync_failed', {
            error: res.codex_sync.error || '',
          });
        } else {
          this.codexSyncError = '';
        }
      } catch (err) {
        // 删除 404 = 条目已不存在，良性：刷新列表、不报错。
        // request 封装抛泛型 Error（无 status）：带 status 用 status，
        // 否则按泛型 404 消息形态（HTTP 404 子串）识别，不做全等匹配。
        if (!this.isMissingError(err)) {
          this.actionError = this.t('agent_mcp_registry_save_failed', { error: err?.message || '' });
        }
      } finally {
        await this.loadAll();
      }
    },
    isMissingError(err) {
      if (err && typeof err.status === 'number') return err.status === 404;
      return /HTTP 404\b/.test(String(err?.message || ''));
    },
  },
};
</script>
