<template>
  <!-- agentcore 记忆层管理卡（2026-11-09 记忆批次 Task 7）。开关走
       customer.agentcore.memory_enabled（三态 select，变更即 emit save 走父页
       两段式持久化）；项目/条目 CRUD 直连 /api/agent/memory*（core 进程
       handler 与 runtime 共用 store 单例，无 proxy 面）。交互骨架与 confirm
       沿 AgentMCPRegistrySettings 先例。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <span class="material-symbols-outlined">psychology</span>
        </div>
        <div class="min-w-0">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_memory_title') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('agent_memory_hint') }}
          </p>
        </div>
      </div>
      <button
        type="button"
        data-testid="memory-add"
        class="inline-flex h-11 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
        :disabled="!selectedProject"
        @click="openCreate"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        {{ t('agent_memory_add') }}
      </button>
    </div>

    <!-- 三态总开关：nil=跟随默认（开启）/true/false。变更即 emit save——
         载荷沿 AgentSkillsGlobalSettings 同段写法（带原 agentcore 段），「跟随默认」
         显式剔除键（缺键不进 patch，归一层三态契约）。 -->
    <label class="mt-5 flex min-h-11 items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span class="block text-xs font-semibold">{{ t('agent_memory_switch') }}</span>
      <select
        v-model="memoryChoice"
        data-testid="memory-switch"
        class="h-10 w-44 shrink-0 rounded-lg border border-slate-200 bg-white px-2 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
        :disabled="loading || saving"
        @change="onSwitchChange"
      >
        <option value="follow">{{ t('agent_memory_switch_follow') }}</option>
        <option value="on">{{ t('agent_memory_switch_on') }}</option>
        <option value="off">{{ t('agent_memory_switch_off') }}</option>
      </select>
    </label>
    <p class="mt-2 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('agent_memory_switch_hint') }}</p>

    <!-- 项目下拉 + 清空（清空整项目必须 confirm） -->
    <div class="mt-4 flex flex-wrap items-center gap-2">
      <label class="flex min-w-0 flex-1 items-center gap-2">
        <span class="shrink-0 text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_memory_project') }}</span>
        <select
          v-model="selectedProject"
          data-testid="memory-project"
          class="h-10 min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-2 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
          :disabled="working"
          @change="loadMemories"
        >
          <option v-for="project in projects" :key="project.project" :value="project.project">
            {{ project.project }}
          </option>
        </select>
      </label>
      <span
        v-if="selectedProject"
        class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700/40 dark:text-slate-400"
        data-testid="memory-project-count"
      >{{ t('agent_memory_count', { count: selectedProjectCount }) }}</span>
      <button
        v-if="memories.length"
        type="button"
        data-testid="memory-clear"
        class="inline-flex h-10 shrink-0 items-center rounded-lg border border-rose-200 px-2.5 text-xs font-semibold text-rose-600 transition-colors hover:bg-rose-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-300/40 disabled:opacity-60 dark:border-rose-500/30 dark:text-rose-300 dark:hover:bg-rose-500/10"
        :disabled="working"
        @click="clearProject"
      >{{ t('agent_memory_clear') }}</button>
    </div>

    <div
      v-if="actionError"
      class="mt-4 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs leading-5 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ actionError }}</div>

    <div v-if="listLoading" class="mt-5 flex min-h-24 items-center justify-center text-xs text-slate-500 dark:text-slate-400">
      <span class="material-symbols-outlined mr-2 text-base motion-safe:animate-spin">progress_activity</span>
      {{ t('agent_integrations_loading') }}
    </div>
    <div
      v-else-if="loadError"
      class="mt-5 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ loadError }}</div>

    <template v-else-if="selectedProject">
      <ul v-if="memories.length" class="mt-4 divide-y divide-slate-100 dark:divide-slate-800">
        <li v-for="row in memories" :key="row.id" class="py-3" data-testid="memory-row">
          <template v-if="editingId === row.id">
            <!-- 行内编辑：与新增同一 500 字符上限与计数 -->
            <textarea
              :value="editContent"
              rows="3"
              :maxlength="contentCap"
              data-field="memory-edit-content"
              class="w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              @input="onEditContentInput"
            ></textarea>
            <div class="mt-1 flex items-center justify-between gap-2">
              <span class="text-[10px] text-slate-400">{{ editContent.length }} / {{ contentCap }}</span>
              <div class="flex items-center gap-1.5">
                <button
                  type="button"
                  class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
                  @click="cancelEdit"
                >{{ t('agent_memory_cancel') }}</button>
                <button
                  type="button"
                  class="inline-flex h-9 items-center rounded-lg bg-primary px-2.5 text-xs font-bold text-white transition-colors hover:bg-primary/90 disabled:opacity-60"
                  :disabled="working"
                  @click="saveEdit"
                >{{ t('agent_memory_save') }}</button>
              </div>
            </div>
            <div
              v-if="editError"
              class="mt-2 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
            >{{ editError }}</div>
          </template>
          <template v-else>
            <p class="break-all text-xs leading-5 text-slate-800 dark:text-slate-100">{{ row.content }}</p>
            <div class="mt-1 flex flex-wrap items-center gap-2">
              <span class="text-[10px] text-slate-400">{{ formatMemoryTime(row.created_at) }}</span>
              <span
                class="rounded px-1.5 py-0.5 text-[10px] font-bold"
                :class="row.source === 'extract'
                  ? 'bg-primary/10 text-primary'
                  : 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400'"
              >{{ sourceLabel(row.source) }}</span>
              <div class="ml-auto flex items-center gap-1.5">
                <button
                  type="button"
                  class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
                  :disabled="working"
                  @click="startEdit(row)"
                >{{ t('agent_memory_edit') }}</button>
                <button
                  type="button"
                  class="inline-flex h-9 items-center rounded-lg border border-rose-200 px-2.5 text-xs font-semibold text-rose-600 transition-colors hover:bg-rose-50 disabled:opacity-60 dark:border-rose-500/30 dark:text-rose-300 dark:hover:bg-rose-500/10"
                  :disabled="working"
                  @click="removeRow(row)"
                >{{ t('agent_memory_delete') }}</button>
              </div>
            </div>
          </template>
        </li>
      </ul>
      <p v-else class="mt-4 text-xs text-slate-400">{{ t('agent_memory_empty') }}</p>
    </template>
    <p v-else class="mt-4 text-xs text-slate-400">{{ t('agent_memory_projects_empty') }}</p>

    <div v-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <!-- 新增弹窗（骨架沿 AgentMCPRegistrySettings 二级弹窗形态） -->
    <div
      v-if="formOpen"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('agent_memory_add')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeForm"></div>
      <div class="relative z-10 max-h-[88dvh] w-full max-w-md overflow-y-auto rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('agent_memory_add') }}</h4>
        <p class="mt-1 text-[11px] text-slate-500 dark:text-slate-400">{{ selectedProject }}</p>

        <label class="mt-4 block">
          <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_memory_content') }}</span>
          <textarea
            :value="form.content"
            rows="4"
            :maxlength="contentCap"
            data-field="memory-content"
            class="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            @input="onFormContentInput"
          ></textarea>
        </label>
        <p class="mt-1 text-right text-[10px] text-slate-400" data-testid="memory-content-counter">{{ form.content.length }} / {{ contentCap }}</p>

        <div
          v-if="formError"
          class="mt-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
        >{{ formError }}</div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg border border-slate-200 px-4 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
            @click="closeForm"
          >{{ t('agent_memory_cancel') }}</button>
          <button
            type="button"
            data-testid="memory-create-save"
            class="inline-flex h-10 items-center rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
            :disabled="working"
            @click="saveCreate"
          >{{ t('agent_memory_save') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

// 与后端 agentCoreMemoryContentCap（services/agent_ai_agentcore_memory.go）
// 同源：500 字符（Go 按 rune 计，前端 String.length 按 UTF-16 码元计，CJK
// 场景一致；emoji 差异由后端权威闸兜底）。maxlength 属性拦键盘输入，input
// 处理器拦程序化赋值（jsdom 直接设 value 绕过 maxlength，两端都要截）。
export const MEMORY_CONTENT_CAP = 500;

export function capMemoryContent(value) {
  return String(value ?? '').slice(0, MEMORY_CONTENT_CAP);
}

// 三态映射：显式布尔才映射 on/off，其余（缺键/非布尔）一律跟随默认。
export function memoryChoiceFromConfig(raw) {
  if (raw === true) return 'on';
  if (raw === false) return 'off';
  return 'follow';
}

// 与 services/agentApi.request 同构的 common 信封解法：{code,msg,data}，
// code!==0 抛 Error(msg)。记忆端点是纯本机 CRUD，不需要 agentApi 的鉴权
// 失败联动（handleAuthenticationFailure），故不新增 agentApi 导出。
export async function memoryApiRequest(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });
  const payload = await response.json().catch(() => null);
  const body = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : null;
  if (!response.ok || !body || body.code !== 0) {
    const msg = body && typeof body.msg === 'string' && body.msg
      ? body.msg
      : `Request failed with HTTP ${response.status}`;
    const error = new Error(msg);
    // 信封解析成功(common 信封必有数字 code)才挂传输层 status:删除通道据此
    // 按 status===404 识别「条目已不存在」的良性竞态(双写者下删陈旧行真实
    // 可达)。非 JSON 体(典型=路由漏注册返回 404 page not found,本仓 handler
    // 文件头明文双注册纪律:缺任一侧=404 老坑)不带 status——isMissingError
    // 判否,错误显性暴露,不做静默掩盖。
    if (body && typeof body.code === 'number') {
      error.status = response.status;
    }
    throw error;
  }
  return body.data;
}

// RFC3339 → 本地时间展示；解析失败原样透出（手改文件的畸形时间不吞）。
export function formatMemoryTime(value) {
  const raw = String(value ?? '');
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) return raw;
  return parsed.toLocaleString();
}

export default {
  name: 'AgentMemorySettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' },
  },
  emits: ['save'],
  data() {
    return {
      memoryChoice: memoryChoiceFromConfig(this.config?.agentcore?.memory_enabled),
      projects: [],
      selectedProject: '',
      memories: [],
      listLoading: false,
      loadError: '',
      formOpen: false,
      form: { content: '' },
      formError: '',
      editingId: null,
      editContent: '',
      editError: '',
      working: false,
      actionError: '',
    };
  },
  computed: {
    contentCap() {
      return MEMORY_CONTENT_CAP;
    },
    selectedProjectCount() {
      const hit = this.projects.find((project) => project && project.project === this.selectedProject);
      // 下拉清单与列表分别拉取，短暂不一致时以列表实长兜底。
      return hit ? hit.count : this.memories.length;
    },
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.memoryChoice = memoryChoiceFromConfig(nextConfig?.agentcore?.memory_enabled);
      },
    },
  },
  mounted() {
    this.loadAll();
  },
  methods: {
    // 模板上下文挂载：script 具名导出（供单测直测）不会自动进渲染作用域。
    formatMemoryTime,
    capMemoryContent,
    sourceLabel(source) {
      return source === 'extract' ? this.t('agent_memory_source_extract') : this.t('agent_memory_source_manual');
    },
    async loadAll() {
      this.listLoading = true;
      this.loadError = '';
      try {
        const data = await memoryApiRequest('/api/agent/memory/projects', { method: 'GET' });
        this.projects = Array.isArray(data?.projects) ? data.projects : [];
        if (!this.selectedProject && this.projects.length) {
          this.selectedProject = String(this.projects[0].project || '');
        }
        if (this.selectedProject) {
          await this.loadMemories();
        }
      } catch (err) {
        this.loadError = this.t('agent_memory_load_failed', { error: err?.message || '' });
      } finally {
        this.listLoading = false;
      }
    },
    async loadMemories() {
      if (!this.selectedProject) {
        this.memories = [];
        return;
      }
      this.listLoading = true;
      this.loadError = '';
      try {
        const data = await memoryApiRequest(
          `/api/agent/memory?project=${encodeURIComponent(this.selectedProject)}`,
          { method: 'GET' },
        );
        this.memories = Array.isArray(data?.memories) ? data.memories : [];
      } catch (err) {
        this.loadError = this.t('agent_memory_load_failed', { error: err?.message || '' });
      } finally {
        this.listLoading = false;
      }
    },
    onSwitchChange() {
      const nextAgentcore = { ...(this.config?.agentcore || {}) };
      if (this.memoryChoice === 'follow') {
        // 缺键不进 patch（归一层三态契约）；后端 deep-merge 对 null 跳过，
        // 已落盘显式值经 UI 无法清除——开关 hint 文案已如实声明。
        delete nextAgentcore.memory_enabled;
      } else {
        nextAgentcore.memory_enabled = this.memoryChoice === 'on';
      }
      this.$emit('save', { ...(this.config || {}), agentcore: nextAgentcore });
    },
    openCreate() {
      this.form = { content: '' };
      this.formError = '';
      this.formOpen = true;
    },
    closeForm() {
      this.formOpen = false;
      this.formError = '';
    },
    onFormContentInput(event) {
      this.form.content = capMemoryContent(event.target.value);
      // maxlength 拦不住程序化赋值：回写 DOM 同步截断后的真值。
      event.target.value = this.form.content;
    },
    onEditContentInput(event) {
      this.editContent = capMemoryContent(event.target.value);
      event.target.value = this.editContent;
    },
    async saveCreate() {
      const content = String(this.form.content || '').trim();
      if (!content) {
        this.formError = this.t('agent_memory_content_required');
        return;
      }
      this.working = true;
      this.formError = '';
      try {
        await memoryApiRequest('/api/agent/memory', {
          method: 'POST',
          body: JSON.stringify({ project: this.selectedProject, content }),
        });
        this.closeForm();
        await this.loadAll();
      } catch (err) {
        this.formError = this.t('agent_memory_save_failed', { error: err?.message || '' });
      } finally {
        this.working = false;
      }
    },
    startEdit(row) {
      this.editingId = row.id;
      this.editContent = String(row.content ?? '');
      this.editError = '';
    },
    cancelEdit() {
      this.editingId = null;
      this.editContent = '';
      this.editError = '';
    },
    async saveEdit() {
      const content = String(this.editContent || '').trim();
      if (!content) {
        this.editError = this.t('agent_memory_content_required');
        return;
      }
      this.working = true;
      this.editError = '';
      try {
        await memoryApiRequest(`/api/agent/memory/${encodeURIComponent(this.editingId)}`, {
          method: 'PUT',
          body: JSON.stringify({ project: this.selectedProject, content }),
        });
        this.cancelEdit();
        await this.loadAll();
      } catch (err) {
        this.editError = this.t('agent_memory_save_failed', { error: err?.message || '' });
      } finally {
        this.working = false;
      }
    },
    // 删除 404 = 条目已不存在，良性：刷新列表、不报错（双写者下删陈旧行
    // 竞态真实可达）。只认 memoryApiRequest 挂上的 status===404——无 status
    // 的错（含非 JSON 体 404=路由漏注册配置事故）一律按真错显性暴露。
    isMissingError(err) {
      return Boolean(err) && err.status === 404;
    },
    async removeRow(row) {
      if (!window.confirm(this.t('agent_memory_delete_confirm', { project: this.selectedProject }))) return;
      this.working = true;
      this.actionError = '';
      try {
        await memoryApiRequest(
          `/api/agent/memory/${encodeURIComponent(row.id)}?project=${encodeURIComponent(this.selectedProject)}`,
          { method: 'DELETE' },
        );
      } catch (err) {
        if (!this.isMissingError(err)) {
          // 删除/清空走 action_failed：与保存通道文案可区分（评审 NIT③）。
          this.actionError = this.t('agent_memory_action_failed', { error: err?.message || '' });
        }
      } finally {
        await this.loadAll();
        this.working = false;
      }
    },
    async clearProject() {
      if (!window.confirm(this.t('agent_memory_clear_confirm', {
        project: this.selectedProject,
        count: this.memories.length,
      }))) return;
      this.working = true;
      this.actionError = '';
      try {
        await memoryApiRequest(
          `/api/agent/memory?project=${encodeURIComponent(this.selectedProject)}`,
          { method: 'DELETE' },
        );
      } catch (err) {
        this.actionError = this.t('agent_memory_action_failed', { error: err?.message || '' });
      } finally {
        await this.loadAll();
        this.working = false;
      }
    },
  },
};
</script>
