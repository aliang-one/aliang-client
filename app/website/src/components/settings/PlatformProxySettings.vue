<template>
  <!-- 平台流量代理卡（2026-11-15 自 mesh 组网卡「出口链路」区块挪入代理设置
       子 tab;语义改名=平台流量代理,内部 i18n 键/线格式 customer.mesh.fallback
       均不动——配置键迁移有 deep-merge 兼容成本,UI 先行）。
       语义:开关开=本机收到的平台流量（含组网里其他节点转发来的）常驻转交
       下一跳出网;下一跳也开着就继续前链,直到某台未启用的设备就地出网。
       保存载荷 = 整配置透传 + mesh.fallback 字面量替换（本卡是 fallback 的
       唯一 UI 写者;mesh 卡片已剥离并省缺该键,后端 deep-merge 缺键 preserve）。 -->
  <div class="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark" data-platform-proxy>
    <div class="flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">alt_route</span>
      <div>
        <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('mesh_fallbackTitle') }}</h3>
        <p class="text-xs text-slate-500">{{ t('mesh_fallbackDesc') }}</p>
      </div>
    </div>

    <p
      v-if="successMessage"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300"
      role="status"
    >
      {{ successMessage }}
    </p>

    <!-- 依赖降级提示（MeshSettings sshIngressOff 同口径）:mesh 有效启用而
         SSH 入口未开 → 代理不会生效（MeshEffectivelyEnabled 运行期降级同源）,
         只提示不静默改写——启用入口在「入口与组网」。 -->
    <div
      v-if="sshIngressOff"
      class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      data-platform-proxy-dependency
    >
      {{ t('mesh_fallbackDependencyHint') }}
    </div>

    <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
      <label
        class="inline-flex cursor-pointer items-center justify-between gap-3 rounded-full border px-3 py-2 text-sm font-medium shadow-sm transition md:min-w-[210px]"
        :class="form.fallbackEnabled
          ? 'border-primary/20 bg-white text-slate-700 dark:border-primary/30 dark:bg-slate-900 dark:text-slate-200'
          : 'border-slate-200 bg-white/70 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300'"
      >
        <span>{{ t('mesh_fallbackEnable') }}</span>
        <span class="relative">
          <input v-model="form.fallbackEnabled" class="peer sr-only" data-field="meshFallbackEnable" :disabled="loading || saving" type="checkbox" />
          <span class="relative block h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
        </span>
      </label>
    </div>

    <!-- 下一跳 = peers 中已配置的节点（后端 Validate:enable 时 next_hop 必填、
         须指向 peers 中的节点且已钉扎 host_key;地址与指纹从 peers 条目继承,
         不再单输）。未钉扎的行标注提示,选中后端也会拒。节点配置入口在
         「入口与组网」的组网卡——本卡只消费现值。 -->
    <label class="space-y-2">
      <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_fallbackNextHop') }}</span>
      <select
        v-model="form.fallbackNextHop"
        class="w-full rounded-lg border px-3 py-2 text-sm shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
        :class="validationError
          ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
          : 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900'"
        data-field="meshFallbackNextHop"
        :disabled="loading || saving || !form.peerRows.length"
        @keydown.enter.prevent
      >
        <option value="">{{ form.peerRows.length ? t('mesh_fallbackNextHopPlaceholder') : t('mesh_fallbackNextHopEmpty') }}</option>
        <option v-for="peer in form.peerRows" :key="peer.device_id" :value="peer.device_id">
          {{ peer.device_id }}{{ peer.host_key ? '' : ' · ' + t('mesh_fallbackNextHopUnpinned') }}
        </option>
      </select>
      <p class="text-xs text-slate-500">{{ t('mesh_fallbackNextHopHint') }}</p>
    </label>

    <p v-if="validationError" class="text-xs text-red-500" role="alert">{{ validationError }}</p>

    <div class="flex items-center justify-end gap-3">
      <button
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        data-platform-proxy-save
        type="button"
        :disabled="loading || saving || !!validationError"
        @click="save"
      >{{ saving ? t('mesh_saving') : t('mesh_save') }}</button>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { showErrorDialog } from '../../composables/useErrorDialog';
import { normalizeMeshConfig } from './MeshSettings.js';

export default {
  name: 'PlatformProxySettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  data() {
    return {
      form: this.formFromConfig(this.config)
    };
  },
  computed: {
    // 保存态 mesh(选项源=归一化 peers 真值;本卡不编辑 peers,行内标注只读)
    configMesh() {
      return normalizeMeshConfig(this.config?.mesh);
    },
    // 依赖降级（MeshSettings sshIngressOff 同款口径）:mesh 有效启用
    // （enable!==false,后端三态 nil=true）而 ssh_ingress.enable 非 true。
    sshIngressOff() {
      return this.configMesh.enable !== false && this.config?.ssh_ingress?.enable !== true;
    },
    // 校验与后端 MeshConfig.Validate「fallback.enable 时 next_hop 必填」对齐;
    // 文案是提示层,后端保存时仍是权威(还校验 next_hop 指向已钉扎 peer)。
    validationError() {
      if (this.form.fallbackEnabled && !String(this.form.fallbackNextHop || '').trim()) {
        return this.t('mesh_validationFallbackNextHop');
      }
      return '';
    }
  },
  watch: {
    // 保存失败等动作结果类报错走全局弹窗(2026-11-17 定案);行内红条删除
    error: {
      immediate: true,
      handler(next) {
        if (next) {
          showErrorDialog(next);
        }
      }
    },
    // 深度跟随父层配置(保存回读/其他卡片改写)重建表单——MeshSettings
    // 既有模式;本卡无行内编辑,重建无丢输入风险。
    config: {
      deep: true,
      handler() {
        this.form = this.formFromConfig(this.config);
      }
    }
  },
  methods: {
    formFromConfig(config) {
      const mesh = normalizeMeshConfig(config?.mesh);
      return {
        fallbackEnabled: mesh.fallback.enable,
        fallbackNextHop: mesh.fallback.next_hop || '',
        peerRows: mesh.peers.map((peer) => ({ ...peer, host_key: peer.host_key || '' }))
      };
    },
    // 提交载荷:整配置透传 + mesh 覆盖（ModelMappingSettings/SshIngressSettings
    // 同款）。peers/密钥等未编辑字段经归一化规整透传,本卡不丢配置现值;
    // fallback 是本卡唯一写者的字面量整体替换——旧版字段(classes/addr/
    // host_key)随替换一并清除,后端结构体已不认这些键。
    save() {
      if (this.validationError) {
        return;
      }
      const mesh = normalizeMeshConfig({
        ...(this.config?.mesh && typeof this.config.mesh === 'object' ? this.config.mesh : {}),
        fallback: {
          enable: !!this.form.fallbackEnabled,
          next_hop: String(this.form.fallbackNextHop || '').trim()
        }
      });
      this.$emit('save', {
        ...(this.config || {}),
        mesh
      });
    }
  }
};
</script>
