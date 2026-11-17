<template>
  <!-- 规则分流实际出口卡(2026-11-16 toSocks 孤儿开关事故的 UI 修复):
       customer.proxy_rules 只是配置意图;实际出口由路由 apply store
       (canonical,/api/config/routing)的 egress.toSocks.enabled 编译决定——
       开关关时同样的规则编译成 target=direct,数据面直连(0.196 事故根因)。
       本卡是 canonical 的唯一 UI 写者:读活体编译结果诚实展示每条规则当前
       实际出口 + 提供 toSocks 开关;保存=GET 整份 canonical→改开关与
       proxy_rule_* target→POST 整份(DisallowUnknownFields,绝不可混入
       customer 配置字段)。走独立 fetch 链,不进父层 customer 保存链。 -->
  <div class="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark" data-egress-target>
    <div class="flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">call_split</span>
      <div>
        <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('egress_title') }}</h3>
        <p class="text-xs text-slate-500">{{ t('egress_desc') }}</p>
      </div>
    </div>

    <p
      v-if="error"
      class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      role="alert"
    >
      {{ error }}
    </p>
    <p
      v-else-if="successMessage"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300"
      role="status"
    >
      {{ successMessage }}
    </p>

    <!-- 依赖降级提示:客户代理未启用/未配服务器/类型不在支持范围 → 后端
         编译层 hasToSocks=false,开了也会在下次 customer 保存被静默翻回
         关——必须挡在 UI,只提示不静默改写。 -->
    <div
      v-if="guardMessage"
      class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      data-egress-target-guard
    >
      {{ guardMessage }}
    </div>

    <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
      <label
        class="inline-flex cursor-pointer items-center justify-between gap-3 rounded-full border px-3 py-2 text-sm font-medium shadow-sm transition md:min-w-[210px]"
        :class="egressEnabled
          ? 'border-primary/20 bg-white text-slate-700 dark:border-primary/30 dark:bg-slate-900 dark:text-slate-200'
          : 'border-slate-200 bg-white/70 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300'"
      >
        <span>{{ t('egress_switchLabel') }}</span>
        <span class="relative">
          <input v-model="egressEnabled" class="peer sr-only" data-field="egressTargetEnabled" :disabled="loading || saving || !!guardMessage" type="checkbox" />
          <span class="relative block h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
        </span>
      </label>
    </div>

    <!-- 诚实展示:活体 canonical 编译出的 proxy 规则与各自实际出口。
         与文本域(意图态)不同源——开关关时这里如实标「直连」。 -->
    <div class="space-y-2" data-egress-target-rules>
      <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('egress_rulesHeading') }}</span>
      <p v-if="loading" class="text-xs text-slate-500">{{ t('egress_loading') }}</p>
      <p v-else-if="!compiledProxyRules.length" class="text-xs text-slate-500">{{ t('egress_rulesEmpty') }}</p>
      <ul v-else class="divide-y divide-slate-100 rounded-lg border border-slate-200 dark:divide-slate-800 dark:border-slate-700">
        <li
          v-for="rule in compiledProxyRules"
          :key="rule.id"
          class="flex items-center justify-between gap-3 px-3 py-2 text-sm"
        >
          <span class="truncate text-slate-700 dark:text-slate-200">{{ rule.condition }}</span>
          <span
            class="shrink-0 rounded-full px-2 py-0.5 text-xs font-medium"
            :class="rule.target === 'toSocks'
              ? 'bg-primary/10 text-primary dark:bg-primary/20'
              : 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400'"
            :data-egress-rule-target="rule.condition"
          >{{ targetLabel(rule.target) }}</span>
        </li>
      </ul>
    </div>

    <div class="flex items-center justify-end gap-3">
      <button
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        data-egress-target-save
        type="button"
        :disabled="loading || saving"
        @click="save"
      >{{ saving ? t('egress_saving') : t('egress_save') }}</button>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { getRoutingConfig, saveRoutingConfig } from '../../services/agentApi';

// mapProxyUpstreamType 镜像后端 resolveToSocksUpstreamType
// (processor/routing/snapshot.go)的 customer.proxy.type → canonical
// upstream.type 映射;空串=不在支持范围(守卫禁开)。
export function mapProxyUpstreamType(type) {
  switch (String(type || '').trim().toLowerCase()) {
    case 'socks5':
      return 'socks';
    case 'http':
      return 'http';
    case 'ssh':
      return 'ssh';
    default:
      return '';
  }
}

// applyEgressTargetToRules 开关落载:只动 id 前缀 proxy_rule_ 且 target 为
// 字面 direct/toSocks 的规则;ai_allowlist_*(恒 toAliang)与 toNode:*(mesh
// 尾)一概不碰——两向都必须改写,否则开=静默无效(target 仍 direct,POST 照
// 样成功)、关=后端 Validate「disabled_target_branch」整份 400。
export function applyEgressTargetToRules(rules, enabled) {
  return (rules || []).map((rule) => {
    if (!rule || typeof rule.id !== 'string' || !rule.id.startsWith('proxy_rule_')) {
      return rule;
    }
    const target = String(rule.target || '');
    if (enabled && target === 'direct') {
      return { ...rule, target: 'toSocks' };
    }
    if (!enabled && target === 'toSocks') {
      return { ...rule, target: 'direct' };
    }
    return rule;
  });
}

export default {
  name: 'EgressTargetSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    // customer 配置现值:只读 proxy 段做守卫与 upstream.type 映射,本卡
    // 不经父层保存链写 customer。
    config: { type: Object, default: () => ({}) }
  },
  data() {
    return {
      loading: false,
      saving: false,
      error: '',
      successMessage: '',
      egressEnabled: false,
      canonical: null
    };
  },
  computed: {
    compiledProxyRules() {
      const rules = this.canonical?.routing?.rules;
      return Array.isArray(rules) ? rules.filter((rule) => rule && typeof rule.id === 'string' && rule.id.startsWith('proxy_rule_')) : [];
    },
    // 客户代理可用性守卫(与后端 hasToSocks 同口径:enable + server 非空 +
    // type 可映射)。
    upstreamType() {
      const proxy = this.config?.proxy;
      if (!proxy || proxy.enable !== true || !String(proxy.server || '').trim()) {
        return '';
      }
      return mapProxyUpstreamType(proxy.type);
    },
    guardMessage() {
      const proxy = this.config?.proxy;
      if (!proxy || proxy.enable !== true || !String(proxy.server || '').trim()) {
        return this.t('egress_guardProxyMissing');
      }
      if (!this.upstreamType) {
        return this.t('egress_guardTypeUnsupported');
      }
      return '';
    }
  },
  mounted() {
    this.load();
  },
  methods: {
    targetLabel(target) {
      const value = String(target || '');
      if (value === 'toSocks') return this.t('egress_target_socks');
      if (value === 'direct') return this.t('egress_target_direct');
      if (value === 'toAliang') return this.t('egress_target_aliang');
      if (value.startsWith('toNode:')) return this.t('egress_target_mesh');
      return value || this.t('egress_target_unknown');
    },
    async load() {
      this.loading = true;
      this.error = '';
      try {
        const canonical = await getRoutingConfig();
        this.canonical = canonical && typeof canonical === 'object' ? canonical : {};
        this.egressEnabled = this.canonical?.egress?.toSocks?.enabled === true;
      } catch (err) {
        this.error = `${this.t('egress_loadFailed')}: ${err?.message || err}`;
      } finally {
        this.loading = false;
      }
    },
    async save() {
      if (!this.canonical) {
        return;
      }
      this.saving = true;
      this.error = '';
      this.successMessage = '';
      try {
        // 整份深拷贝→只改目标字段→POST 原样(canonical 四键之外多一个键即 400)。
        const payload = JSON.parse(JSON.stringify(this.canonical));
        const wantEnabled = !!this.egressEnabled;
        payload.egress = payload.egress && typeof payload.egress === 'object' ? payload.egress : {};
        payload.egress.toSocks = {
          enabled: wantEnabled,
          upstream: { type: wantEnabled ? this.upstreamType : '' }
        };
        payload.routing = payload.routing && typeof payload.routing === 'object' ? payload.routing : {};
        payload.routing.rules = applyEgressTargetToRules(payload.routing.rules || [], wantEnabled);
        await saveRoutingConfig(payload);
        // 读改写竞态收敛:保存后重 GET 回显活体结果(customer 保存链可能
        // 交错重编译过 canonical)。
        const canonical = await getRoutingConfig();
        this.canonical = canonical && typeof canonical === 'object' ? canonical : {};
        this.egressEnabled = this.canonical?.egress?.toSocks?.enabled === true;
        this.successMessage = this.t('egress_saveSuccess');
      } catch (err) {
        this.error = `${this.t('egress_saveFailed')}: ${err?.message || err}`;
      } finally {
        this.saving = false;
      }
    }
  }
};
</script>
