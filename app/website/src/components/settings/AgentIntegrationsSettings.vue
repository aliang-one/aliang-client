<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <span class="material-symbols-outlined">extension</span>
        </div>
        <div class="min-w-0">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_integrations_title') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('agent_integrations_desc') }}
          </p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-11 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
        :disabled="loading"
        @click="load"
      >
        <span class="material-symbols-outlined text-sm">refresh</span>
        {{ t('agent_integrations_refresh') }}
      </button>
    </div>

    <div v-if="loading" class="mt-5 flex min-h-24 items-center justify-center text-xs text-slate-500 dark:text-slate-400">
      <span class="material-symbols-outlined mr-2 text-base motion-safe:animate-spin">progress_activity</span>
      {{ t('agent_integrations_loading') }}
    </div>
    <div
      v-else-if="error"
      class="mt-5 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ error }}</div>

    <template v-else>
      <!-- MCP 服务 -->
      <section class="mt-5">
        <h4 class="flex items-center gap-1.5 text-xs font-bold uppercase tracking-wide text-slate-500 dark:text-slate-400">
          <span class="material-symbols-outlined text-sm">hub</span>
          {{ t('agent_integrations_mcpTitle', { count: servers.length }) }}
        </h4>
        <p v-if="!servers.length" class="mt-2 text-xs text-slate-400">{{ t('agent_integrations_mcpEmpty') }}</p>
        <template v-else>
          <ul class="mt-2 divide-y divide-slate-100 dark:divide-slate-800">
            <li v-for="server in pagedServers" :key="server.name" class="flex flex-wrap items-start gap-2 py-2.5">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="truncate text-xs font-semibold text-slate-800 dark:text-slate-100">{{ server.name }}</span>
                  <span class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold uppercase text-slate-500 dark:bg-slate-700/40 dark:text-slate-400">
                    {{ server.transport }}
                  </span>
                  <span
                    v-for="label in sourceLabels(server.source)"
                    :key="label"
                    class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary"
                  >{{ label }}</span>
                </div>
                <p class="mt-1 break-all text-[11px] leading-4 text-slate-500 dark:text-slate-400">
                  {{ mcpEndpointLabel(server) }}
                </p>
                <p v-if="server.tool_prefix" class="mt-0.5 break-all text-[10px] text-slate-400">
                  {{ t('agent_integrations_toolPrefix', { prefix: server.tool_prefix }) }}
                </p>
              </div>
              <span
                class="shrink-0 rounded px-2 py-0.5 text-[10px] font-bold"
                :class="kernelStateBadge(server.kernel_state).className"
              >{{ t(kernelStateBadge(server.kernel_state).key) }}</span>
            </li>
          </ul>
          <!-- 紧凑分页（页大小 20；标题计数始终展示全量） -->
          <nav
            v-if="serverTotalPages > 1"
            class="mt-2 flex items-center justify-center gap-2 text-[11px]"
            :aria-label="t('agent_integrations_paginationLabel')"
          >
            <button
              type="button"
              class="inline-flex h-9 items-center gap-0.5 rounded-lg border border-slate-200 px-2.5 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="serverPage <= 1"
              :aria-label="t('agent_integrations_prev')"
              @click="goToServerPage(serverPage - 1)"
            >
              <span class="material-symbols-outlined text-sm">chevron_left</span>
            </button>
            <span class="text-slate-400">{{ t('agent_integrations_pageInfo', { page: serverPage, total: serverTotalPages, count: servers.length }) }}</span>
            <button
              type="button"
              class="inline-flex h-9 items-center gap-0.5 rounded-lg border border-slate-200 px-2.5 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="serverPage >= serverTotalPages"
              :aria-label="t('agent_integrations_next')"
              @click="goToServerPage(serverPage + 1)"
            >
              <span class="material-symbols-outlined text-sm">chevron_right</span>
            </button>
          </nav>
        </template>
      </section>

      <!-- 技能 -->
      <section class="mt-6">
        <h4 class="flex items-center gap-1.5 text-xs font-bold uppercase tracking-wide text-slate-500 dark:text-slate-400">
          <span class="material-symbols-outlined text-sm">school</span>
          {{ t('agent_integrations_skillTitle', { count: skills.length }) }}
        </h4>
        <p v-if="!skills.length" class="mt-2 text-xs text-slate-400">{{ t('agent_integrations_skillEmpty') }}</p>
        <template v-else>
          <ul class="mt-2 divide-y divide-slate-100 dark:divide-slate-800">
            <li v-for="skill in pagedSkills" :key="skill.scope + ':' + skill.name" class="flex flex-wrap items-start gap-2 py-2.5">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-1.5">
                  <span class="truncate text-xs font-semibold text-slate-800 dark:text-slate-100">{{ skill.name }}</span>
                  <span class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold uppercase text-slate-500 dark:bg-slate-700/40 dark:text-slate-400">
                    {{ skill.scope }}
                  </span>
                </div>
                <p v-if="skill.description" class="mt-1 text-[11px] leading-4 text-slate-500 dark:text-slate-400">
                  {{ skill.description }}
                </p>
              </div>
              <span
                v-if="!skill.kernel_supported"
                class="shrink-0 rounded bg-amber-100 px-2 py-0.5 text-[10px] font-bold text-amber-700 dark:bg-amber-500/15 dark:text-amber-300"
              >{{ t('agent_integrations_skillUnsupported') }}</span>
            </li>
          </ul>
          <nav
            v-if="skillTotalPages > 1"
            class="mt-2 flex items-center justify-center gap-2 text-[11px]"
            :aria-label="t('agent_integrations_paginationLabel')"
          >
            <button
              type="button"
              class="inline-flex h-9 items-center gap-0.5 rounded-lg border border-slate-200 px-2.5 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="skillPage <= 1"
              :aria-label="t('agent_integrations_prev')"
              @click="goToSkillPage(skillPage - 1)"
            >
              <span class="material-symbols-outlined text-sm">chevron_left</span>
            </button>
            <span class="text-slate-400">{{ t('agent_integrations_pageInfo', { page: skillPage, total: skillTotalPages, count: skills.length }) }}</span>
            <button
              type="button"
              class="inline-flex h-9 items-center gap-0.5 rounded-lg border border-slate-200 px-2.5 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="skillPage >= skillTotalPages"
              :aria-label="t('agent_integrations_next')"
              @click="goToSkillPage(skillPage + 1)"
            >
              <span class="material-symbols-outlined text-sm">chevron_right</span>
            </button>
          </nav>
        </template>
      </section>

      <!-- 边界说明（后端固定 Notes）：让用户看见限制而不是猜 -->
      <ul v-if="notes.length" class="mt-5 space-y-1 rounded-lg bg-slate-50 px-3 py-2.5 text-[10px] leading-4 text-slate-500 dark:bg-slate-800/40 dark:text-slate-400">
        <li v-for="(note, i) in notes" :key="i" class="flex gap-1.5">
          <span class="shrink-0">•</span>
          <span>{{ note }}</span>
        </li>
      </ul>
    </template>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { getAgentIntegrations } from '../../services/agentApi';
import { kernelStateBadge, mcpEndpointLabel, sourceLabels } from '../../utils/integrations';

// MCP / 技能列表分页大小（2026-11-04 用户要求支持分页；客户端分页，
// 数据本身来自 /api/agent/integrations 全量快照）。
const INTEGRATIONS_PAGE_SIZE = 20;

// 导出纯函数供单测（RulesSettings 范式）；页码越界返空、<1 归一，不抛错。
export function paginateItems(items, page, pageSize) {
  const list = Array.isArray(items) ? items : [];
  const safePage = Math.max(1, Number(page) || 1);
  const start = (safePage - 1) * pageSize;
  return list.slice(start, start + pageSize);
}

export default {
  name: 'AgentIntegrationsSettings',
  setup() {
    const { t } = useI18n();
    return { t, kernelStateBadge, mcpEndpointLabel, sourceLabels };
  },
  data() {
    return {
      servers: [],
      skills: [],
      notes: [],
      serverPage: 1,
      skillPage: 1,
      loading: false,
      error: '',
    };
  },
  computed: {
    serverTotalPages() {
      return Math.max(1, Math.ceil(this.servers.length / INTEGRATIONS_PAGE_SIZE));
    },
    pagedServers() {
      return paginateItems(this.servers, this.serverPage, INTEGRATIONS_PAGE_SIZE);
    },
    skillTotalPages() {
      return Math.max(1, Math.ceil(this.skills.length / INTEGRATIONS_PAGE_SIZE));
    },
    pagedSkills() {
      return paginateItems(this.skills, this.skillPage, INTEGRATIONS_PAGE_SIZE);
    },
  },
  mounted() {
    this.load();
  },
  methods: {
    async load() {
      this.loading = true;
      this.error = '';
      try {
        const data = await getAgentIntegrations();
        this.servers = Array.isArray(data?.mcp_servers) ? data.mcp_servers : [];
        this.skills = Array.isArray(data?.skills) ? data.skills : [];
        this.notes = Array.isArray(data?.notes) ? data.notes : [];
        // 新数据重置页码：列表缩短后旧页码可能越界
        this.serverPage = 1;
        this.skillPage = 1;
      } catch (_) {
        // 代理失败/离线：给可读错误，不留白屏
        this.error = this.t('agent_integrations_loadFailed');
      } finally {
        this.loading = false;
      }
    },
    goToServerPage(page) {
      this.serverPage = Math.min(Math.max(1, page), this.serverTotalPages);
    },
    goToSkillPage(page) {
      this.skillPage = Math.min(Math.max(1, page), this.skillTotalPages);
    },
  },
};
</script>
