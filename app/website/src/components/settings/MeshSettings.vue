<template>
  <!-- mesh 组网卡（design §9;代理族下 SettingsPage ①代理设置区相邻挂载）。
       本任务=骨架:顶部状态条 + 离线兜底常驻卡（§9.4,在线时即可配置）;
       Task 3 填 SVG 拓扑画布与节点对详情。线格式 customer.mesh;提交载荷 =
       整配置透传 + mesh 覆盖（父层按顶层段浅合并,卡片不丢别的段）。
       补充只读数据三路（agentApi）:runtime 在线态、mesh 状态聚合、LAN 候选——
       fetch 内联仅限只读补充（ssh_ingress hostkey 先例）,配置只经 props。 -->
  <div class="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
    <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-primary">hub</span>
        <div>
          <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('mesh_title') }}</h3>
          <p class="text-xs text-slate-500">{{ t('mesh_description') }}</p>
        </div>
      </div>

      <!-- 状态条（§9.1 全局状态条同款三态+降级）:在线绿/离线琥珀/未配置灰;
           离线数据源=/api/agent/status runtime.online（AgentSettings runtimeOnline
           既有模式）;mesh status 拉取失败=降级琥珀,区别于确认离线。 -->
      <div
        class="inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-xs font-semibold"
        :class="statusPillClass"
        data-mesh-status-pill
      >
        <span class="material-symbols-outlined text-sm">{{ statusIcon }}</span>
        {{ statusLabel }}
      </div>
    </div>

    <p v-if="meshState === 'degraded'" class="text-xs text-amber-600 dark:text-amber-400">
      {{ t('mesh_statusDegradedHint') }}
    </p>

    <div
      v-if="error"
      class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      role="alert"
    >
      {{ error }}
    </div>
    <div
      v-else-if="successMessage"
      class="rounded-lg border border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300"
      role="status"
    >
      {{ successMessage }}
    </div>

    <!-- —— 离线兜底常驻卡（§9.4;兜底式非覆盖式,在线时即可预配置）—— -->
    <section class="space-y-3 rounded-xl border border-dashed border-slate-300 bg-slate-50/60 p-4 dark:border-slate-700 dark:bg-slate-900/40" data-mesh-fallback>
      <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div>
          <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('mesh_fallbackTitle') }}</h4>
          <p class="text-xs text-slate-500">{{ t('mesh_fallbackDesc') }}</p>
        </div>
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

      <!-- Addr 手输 + lan_peers 候选点选（beacon 候选无源 IP:点选只补全端口,
           host 必须手输——无 host 不产出不可拨号的半截地址） -->
      <div class="grid gap-4 md:grid-cols-[minmax(0,1fr)_240px]">
        <label class="space-y-2">
          <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_fallbackAddr') }}</span>
          <input
            v-model="form.fallbackAddr"
            class="w-full rounded-lg border px-3 py-2 font-mono text-sm shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
            :class="validationError
              ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
              : 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900'"
            data-field="meshFallbackAddr"
            :disabled="loading || saving"
            :placeholder="t('mesh_fallbackAddrPlaceholder')"
            type="text"
            @keydown.enter.prevent
          />
        </label>
        <label class="space-y-2">
          <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_fallbackCandidate') }}</span>
          <select
            class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            data-field="meshFallbackCandidate"
            :disabled="loading || saving || !lanPeers.length"
            :value="candidateChoice"
            @change="onCandidateChange"
          >
            <option value="">{{ lanPeers.length ? t('mesh_fallbackCandidatePlaceholder') : t('mesh_lanPeersEmpty') }}</option>
            <option v-for="peer in lanPeers" :key="peer.device_id" :value="peer.device_id">
              {{ peer.device_id }} · :{{ peer.mesh_port }}
            </option>
          </select>
        </label>
      </div>

      <!-- Classes 三勾选（后端 meshproto ClassAI/ClassSelf/ClassCustom 子集） -->
      <fieldset class="space-y-2">
        <legend class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_fallbackClasses') }}</legend>
        <div class="flex flex-wrap gap-4">
          <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
            <input
              v-model="form.classes.ai"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-800"
              data-field="meshClassAi"
              :disabled="loading || saving"
              type="checkbox"
            />
            {{ t('mesh_fallbackClassAI') }}
          </label>
          <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
            <input
              v-model="form.classes.self"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-800"
              data-field="meshClassSelf"
              :disabled="loading || saving"
              type="checkbox"
            />
            {{ t('mesh_fallbackClassSelf') }}
          </label>
          <label class="inline-flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-300">
            <input
              v-model="form.classes.custom"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-800"
              data-field="meshClassCustom"
              :disabled="loading || saving"
              type="checkbox"
            />
            {{ t('mesh_fallbackClassCustom') }}
          </label>
        </div>
      </fieldset>
    </section>

    <p v-if="validationError" class="text-xs text-red-500" role="alert">{{ validationError }}</p>

    <div class="flex items-center justify-end gap-3">
      <button
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        data-mesh-save
        type="button"
        :disabled="loading || saving || !!validationError"
        @click="save"
      >
        <span class="material-symbols-outlined text-sm">save</span>
        {{ saving ? t('mesh_saving') : t('mesh_save') }}
      </button>
    </div>
  </div>
</template>

<script>
import { getAgentStatus, getMeshLanPeers, getMeshStatus } from '../../services/agentApi';
import { useI18n } from '../../i18n';
import { MESH_FALLBACK_CLASSES, normalizeMeshConfig } from './MeshSettings.js';

export default {
  name: 'MeshSettings',
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
      form: this.formFromConfig(this.config),
      // 离线判定数据源=/api/agent/status runtime.online;默认乐观 true
      // （!status?.runtime → true,useAgentStatus runtimeOnline 同语义）
      runtimeOnline: true,
      meshStatus: null,
      statusLoading: true,
      statusFailed: false,
      lanPeers: [],
      candidateChoice: ''
    };
  },
  computed: {
    // 保存态 mesh（状态条只认已落盘的兜底,不信表单未保存的编辑值）
    configMesh() {
      return normalizeMeshConfig(this.config?.mesh);
    },
    // 状态条状态机:降级 > 加载 > 未配置 > 离线(兜底/无兜底) > 在线。
    // 「未装配」优先于离线——peers 空+不转发+无兜底时没有 mesh 可言,
    // 离线琥珀会误导用户去配兜底（§9.4 兜底是 mesh 出口的最后分支）。
    meshState() {
      if (this.statusFailed) {
        return 'degraded';
      }
      if (this.statusLoading || this.meshStatus === null) {
        return 'loading';
      }
      const peers = Array.isArray(this.meshStatus?.peers) ? this.meshStatus.peers : [];
      const fallbackArmed = this.configMesh.fallback.enable && Boolean(this.configMesh.fallback.addr);
      const unconfigured = peers.length === 0 && !this.meshStatus.forward_enable && !this.configMesh.fallback.enable;
      if (unconfigured) {
        return 'idle';
      }
      if (!this.runtimeOnline) {
        return fallbackArmed ? 'offlineFallback' : 'offline';
      }
      return 'online';
    },
    statusLabel() {
      const map = {
        online: 'mesh_statusOnline',
        offlineFallback: 'mesh_statusOfflineFallback',
        offline: 'mesh_statusOffline',
        degraded: 'mesh_statusDegraded',
        idle: 'mesh_statusIdle',
        loading: 'mesh_loading'
      };
      return this.t(map[this.meshState] || 'mesh_loading');
    },
    statusIcon() {
      const map = {
        online: 'check_circle',
        offlineFallback: 'cloud_off',
        offline: 'cloud_off',
        degraded: 'error',
        idle: 'radio_button_unchecked',
        loading: 'progress_activity'
      };
      return map[this.meshState] || 'progress_activity';
    },
    statusPillClass() {
      if (this.meshState === 'online') {
        return 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300';
      }
      if (this.meshState === 'offlineFallback' || this.meshState === 'offline' || this.meshState === 'degraded') {
        return 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300';
      }
      return 'border-slate-200 bg-slate-50 text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400';
    },
    // 校验与后端 MeshConfig.Validate「fallback.enable 时 addr 必填」对齐;
    // 文案是提示层,后端保存时仍是权威（还校验 host:port 形态）。
    validationError() {
      if (this.form.fallbackEnabled && !String(this.form.fallbackAddr || '').trim()) {
        return this.t('mesh_validationFallbackAddr');
      }
      return '';
    }
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = this.formFromConfig(nextConfig);
      }
    },
    // 保存成功后重拉状态两路（幂等 GET）:开关/兜底现值与 LAN 候选随之刷新,
    // 「启用并保存后状态条当场过渡」同 ssh_ingress hostkey 先例。
    successMessage(message) {
      if (message) {
        this.loadMeshStatus();
        this.loadLanPeers();
      }
    }
  },
  mounted() {
    this.loadRuntime();
    this.loadMeshStatus();
    this.loadLanPeers();
  },
  methods: {
    formFromConfig(config) {
      const mesh = normalizeMeshConfig(config?.mesh);
      return {
        fallbackEnabled: mesh.fallback.enable,
        fallbackAddr: mesh.fallback.addr,
        classes: {
          ai: mesh.fallback.classes.includes('ai'),
          self: mesh.fallback.classes.includes('self'),
          custom: mesh.fallback.classes.includes('custom')
        }
      };
    },
    // —— runtime 在线态（GET /api/agent/status;runtimeOnline 既有模式）——
    // 拉取失败保持乐观 true:若本地 API 整体不可达,mesh status 也会失败,
    // 降级态优先呈现,不会误报「确认离线」。
    async loadRuntime() {
      try {
        const status = await getAgentStatus();
        this.runtimeOnline = !status?.runtime ? true : Boolean(status.runtime.online);
      } catch {
        this.runtimeOnline = true;
      }
    },
    // —— mesh 状态聚合（GET /api/mesh/status;失败=降级态琥珀+提示）——
    async loadMeshStatus() {
      this.statusLoading = true;
      this.statusFailed = false;
      try {
        this.meshStatus = await getMeshStatus();
      } catch {
        this.meshStatus = null;
        this.statusFailed = true;
      } finally {
        this.statusLoading = false;
      }
    },
    // —— LAN beacon 候选（GET /api/mesh/lan_peers;离线可用,失败静默空表）——
    async loadLanPeers() {
      try {
        const data = await getMeshLanPeers();
        const peers = Array.isArray(data?.peers) ? data.peers : [];
        this.lanPeers = peers
          .filter((peer) => peer && typeof peer === 'object' && peer.device_id && peer.mesh_port > 0)
          .map((peer) => ({
            device_id: String(peer.device_id),
            mesh_port: peer.mesh_port,
            cert_fingerprint: String(peer.cert_fingerprint || ''),
            last_seen_unix: typeof peer.last_seen_unix === 'number' ? peer.last_seen_unix : 0
          }));
      } catch {
        this.lanPeers = [];
      }
    },
    // 候选点选:beacon 候选只带 device_id+mesh_port(无源 IP)——保留已输 host
    // 补全/替换端口;无 host 不产出不可拨号的半截地址（后端 SplitHostPort 对
    // "dev-b:56441" 这类形态并不设防,错值只会在拨号期才暴露）。
    onCandidateChange(event) {
      const deviceId = event.target.value;
      const peer = this.lanPeers.find((item) => item.device_id === deviceId);
      this.candidateChoice = '';
      if (!peer) {
        return;
      }
      const raw = String(this.form.fallbackAddr || '').trim();
      const host = (raw.includes(':') ? raw.slice(0, raw.lastIndexOf(':')) : raw).trim();
      this.form.fallbackAddr = host ? `${host}:${peer.mesh_port}` : '';
    },
    // 提交载荷:整配置透传 + mesh 覆盖（ModelMappingSettings/SshIngressSettings
    // 同款）。恒产出显式布尔（normalizeMeshConfig 三态→显式,防保存 200 但
    // 永不落盘）;peers/max_hops/开关等未编辑字段经归一化规整透传（Task 3
    // 画布接管编辑前,卡片不丢配置现值）。
    save() {
      if (this.validationError) {
        return;
      }
      const mesh = normalizeMeshConfig({
        ...(this.config?.mesh && typeof this.config.mesh === 'object' ? this.config.mesh : {}),
        fallback: {
          enable: !!this.form.fallbackEnabled,
          addr: String(this.form.fallbackAddr || '').trim(),
          classes: MESH_FALLBACK_CLASSES.filter((cls) => this.form.classes[cls])
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
