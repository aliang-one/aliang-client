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

    <!-- —— 拓扑画布（§9.2;零依赖 SVG,本机居中+peers 环形布局）——
         拖拽位置 localStorage 持久（后端无位置字段,属前端视觉状态）;
         连线=节点对容器,徽标数字=该 peer 的 mesh 规则条数（数据源
         customer.proxy_rules 真值,非 status 聚合）;保存期环路错误经
         卡片 error prop 呈现（拖拽红牌不适配——画布无拖拽连线操作）。 -->
    <section class="space-y-3" data-mesh-canvas-section>
      <div class="flex items-center justify-between">
        <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('mesh_canvasTitle') }}</h4>
        <p class="text-xs text-slate-500">{{ t('mesh_canvasHint') }}</p>
      </div>

      <div
        v-if="!canvasHasEdges && !canvasPeerIds.length"
        class="rounded-xl border border-dashed border-slate-300 bg-slate-50/60 px-6 py-8 text-center text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40"
        data-mesh-canvas-empty
      >
        {{ t('mesh_unconfiguredHint') }}
      </div>

      <div v-else class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_260px]">
        <svg
          class="w-full rounded-xl border border-slate-200 bg-slate-50/40 dark:border-slate-800 dark:bg-slate-900/40"
          data-mesh-canvas
          role="img"
          viewBox="0 0 640 360"
        >
          <!-- 连线（节点对容器;仅对该 peer 有 mesh 规则时绘制）。
               命中区与可见线分开画:2px 可见线太细点不中,且纯轴向边(peer 在正
               上/正下方)y1==y2 包围盒高度为 0——加一条 14px 透明命中线解决点击
               可达性（SVG 命中测试按 stroke 宽度算,透明描边照样命中）。 -->
          <g data-mesh-edges>
            <line
              v-for="edge in canvasEdges"
              :key="`edge-hit-${edge.peerId}`"
              :x1="edge.x1"
              :x2="edge.x2"
              :y1="edge.y1"
              :y2="edge.y2"
              :data-mesh-edge="edge.peerId"
              class="cursor-pointer"
              stroke="transparent"
              stroke-width="14"
              @click="selectPair(edge.peerId)"
            />
            <line
              v-for="edge in canvasEdges"
              :key="`edge-${edge.peerId}`"
              :x1="edge.x1"
              :x2="edge.x2"
              :y1="edge.y1"
              :y2="edge.y2"
              :class="selectedPair === edge.peerId ? 'stroke-primary' : 'stroke-slate-400 dark:stroke-slate-600'"
              :data-mesh-edge-line="edge.peerId"
              pointer-events="none"
              stroke-width="2"
            />
            <g v-for="edge in canvasEdges" :key="`badge-${edge.peerId}`" :data-mesh-badge="edge.peerId" class="cursor-pointer" @click="selectPair(edge.peerId)">
              <circle
                :cx="edge.badgeX"
                :cy="edge.badgeY"
                fill="#3b82f6"
                r="10"
              />
              <text
                :x="edge.badgeX"
                :y="edge.badgeY + 3.5"
                fill="#ffffff"
                font-size="10"
                font-weight="600"
                text-anchor="middle"
              >{{ edge.count }}</text>
            </g>
          </g>

          <!-- 节点卡（状态灯 + device_id；本机额外显示 LAN 监听口） -->
          <g
            v-for="node in canvasNodes"
            :key="`node-${node.deviceId}`"
            :data-mesh-node="node.deviceId"
            class="cursor-move"
            @click="selectNode(node.deviceId)"
            @pointerdown="beginDrag(node.deviceId, $event)"
          >
            <rect
              :height="48"
              :width="120"
              :x="node.x - 60"
              :y="node.y - 24"
              :class="selectedNode === node.deviceId
                ? 'fill-white stroke-primary dark:fill-slate-800'
                : 'fill-white stroke-slate-300 dark:fill-slate-800 dark:stroke-slate-600'"
              :stroke-width="selectedNode === node.deviceId ? 2 : 1.5"
              rx="8"
            />
            <circle
              :cx="node.x - 46"
              :cy="node.y - 10"
              :data-mesh-node-light="node.deviceId"
              :fill="node.stateColor"
              r="4"
            />
            <text
              :x="node.x - 36"
              :y="node.y - 6"
              fill="#334155"
              font-size="10"
              font-weight="600"
            >{{ node.label }}</text>
            <text
              v-if="node.portLabel"
              :x="node.x - 46"
              :y="node.y + 12"
              fill="#94a3b8"
              font-size="9"
            >{{ node.portLabel }}</text>
          </g>
        </svg>

        <!-- 右栏详情（节点对 / 节点;§9.2 点击连线看规则,点击节点看详情） -->
        <aside class="space-y-2 rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900/60" data-mesh-detail>
          <template v-if="selectedPair">
            <h5 class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_pairDetailTitle') }}</h5>
            <p class="font-mono text-sm text-slate-800 dark:text-slate-200">{{ t('mesh_nodeSelf') }} → {{ selectedPair }}</p>
            <div class="flex flex-wrap gap-1.5">
              <span
                v-for="rule in selectedPairRules"
                :key="rule"
                class="rounded bg-slate-100 px-2 py-1 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
                data-mesh-rule-chip
              >{{ rule }}</span>
              <span v-if="!selectedPairRules.length" class="text-xs text-slate-400">{{ t('mesh_ruleChipsEmpty') }}</span>
            </div>
            <p class="text-[11px] text-slate-400">{{ t('mesh_editInRules') }}</p>
          </template>
          <template v-else-if="selectedNode">
            <h5 class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('mesh_nodeDetailTitle') }}</h5>
            <p class="font-mono text-sm text-slate-800 dark:text-slate-200">{{ selectedNode }}</p>
            <p class="text-xs text-slate-500">{{ t('mesh_dependedBy') }}: {{ selectedNodeDependedBy }}</p>
            <div class="flex flex-wrap gap-1.5">
              <span
                v-for="rule in selectedNodeRules"
                :key="rule"
                class="rounded bg-slate-100 px-2 py-1 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
                data-mesh-rule-chip
              >{{ rule }}</span>
              <span v-if="!selectedNodeRules.length" class="text-xs text-slate-400">{{ t('mesh_ruleChipsEmpty') }}</span>
            </div>
          </template>
          <p v-else class="text-xs text-slate-400">{{ t('mesh_detailPlaceholder') }}</p>
        </aside>
      </div>
    </section>

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
import {
  MESH_FALLBACK_CLASSES,
  listMeshRulesByPeer,
  normalizeMeshConfig,
  parseMeshRuleTargetCount
} from './MeshSettings.js';

// 节点坐标画布几何（viewBox 0 0 640 360）。
const CANVAS_WIDTH = 640;
const CANVAS_HEIGHT = 360;
const NODE_HALF_W = 60;
const NODE_HALF_H = 24;
// 状态灯语义色（SVG 属性不吃 Tailwind dark: 变体;取 design §9.2 语义色常量）
const NODE_STATE_COLORS = {
  online: '#22c55e',
  offlineFallback: '#f59e0b',
  offline: '#f59e0b',
  idle: '#94a3b8'
};
const NODE_POSITIONS_STORAGE_KEY = 'meshNodePositions';
// 本机节点在画布上的占位 id（mesh status 未到达/降级时用）与它的位置键——
// 位置按「本机」而非 device_id 存,status 到达前后拖拽结果连续。
const SELF_PLACEHOLDER_ID = '__self__';
const SELF_POSITION_KEY = '__self__';

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
      candidateChoice: '',
      // 画布:选中态与节点位置（device_id → {x,y};localStorage 持久）
      selectedPair: '',
      selectedNode: '',
      nodePositions: {},
      dragging: null
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
    },
    // —— 画布数据（§9.2）——
    // 连线徽标数据源 = customer.proxy_rules 真值（非 status 聚合）,
    // 与后端 splitMeshTail 同口径解析（MeshSettings.js 复刻）。
    meshRuleCounts() {
      return parseMeshRuleTargetCount(this.config?.proxy_rules);
    },
    meshRulesByPeer() {
      return listMeshRulesByPeer(this.config?.proxy_rules);
    },
    // 本机 device_id:status 聚合值缺失（加载中/降级）时退回占位 id——
    // 中心节点必须渲染,否则离线场景（正是最需要看拓扑的时候）画布只剩
    // 一圈孤立 peer、连线全丢。占位 id 不参与 canvasPeerIds 也不透传后端。
    selfDeviceId() {
      return String(this.meshStatus?.device_id || '').trim() || SELF_PLACEHOLDER_ID;
    },
    selfDeviceLabel() {
      const raw = String(this.meshStatus?.device_id || '').trim();
      return raw || this.t('mesh_nodeSelf');
    },
    // peers:status 聚合（实时可达性）与配置 peers 取并集——配置有但对端未上报
    // 时仍应显示节点（灰灯）,不能因 aggregat 缺项而「配置了却看不见」。
    canvasPeerIds() {
      const ids = [];
      const push = (id) => {
        const value = String(id || '').trim();
        if (value && value !== this.selfDeviceId && !ids.includes(value)) {
          ids.push(value);
        }
      };
      const statusPeers = Array.isArray(this.meshStatus?.peers) ? this.meshStatus.peers : [];
      statusPeers.forEach((peer) => push(peer?.device_id));
      this.configMesh.peers.forEach((peer) => push(peer.device_id));
      return ids;
    },
    // 节点位置:持久化值优先,否则自动布局（本机居中 + peers 环形等分）
    canvasNodes() {
      const nodes = [];
      if (this.selfDeviceId) {
        // 本机位置固定存 SELF_POSITION_KEY:占位 id → 真 device_id 过渡时
        // 用户拖出来的位置不能丢（status 到达前后是同一张卡）。
        const pos = this.nodePositionOf(SELF_POSITION_KEY, CANVAS_WIDTH / 2, CANVAS_HEIGHT / 2);
        nodes.push({
          deviceId: this.selfDeviceId,
          label: this.selfDeviceLabel,
          x: pos.x,
          y: pos.y,
          stateColor: NODE_STATE_COLORS[this.meshState] || NODE_STATE_COLORS.idle,
          portLabel: this.meshStatus?.lan_listen_port ? `:${this.meshStatus.lan_listen_port}` : ''
        });
      }
      const peers = this.canvasPeerIds;
      const radius = peers.length <= 4 ? 110 : 140;
      peers.forEach((peerId, index) => {
        const angle = -Math.PI / 2 + (2 * Math.PI * index) / Math.max(peers.length, 1);
        const autoX = CANVAS_WIDTH / 2 + radius * Math.cos(angle);
        const autoY = CANVAS_HEIGHT / 2 + radius * Math.sin(angle);
        const pos = this.nodePositionOf(peerId, autoX, autoY);
        nodes.push({
          deviceId: peerId,
          label: peerId,
          x: pos.x,
          y: pos.y,
          // peer 状态:report 了=在线,否则灰（对端未上报）
          stateColor: this.canvasPeerIds.includes(peerId) && this.peerReported(peerId)
            ? NODE_STATE_COLORS.online
            : NODE_STATE_COLORS.idle,
          portLabel: ''
        });
      });
      return nodes;
    },
    // 连线:仅对「有 mesh 规则的 peer」绘制（规则数>0）
    canvasEdges() {
      const self = this.canvasNodes.find((node) => node.deviceId === this.selfDeviceId);
      if (!self) {
        return [];
      }
      const counts = this.meshRuleCounts;
      const edges = [];
      this.canvasNodes.forEach((node) => {
        if (node.deviceId === this.selfDeviceId) {
          return;
        }
        const count = counts[node.deviceId] || 0;
        if (!count) {
          return;
        }
        edges.push({
          peerId: node.deviceId,
          count,
          x1: self.x,
          y1: self.y,
          x2: node.x,
          y2: node.y,
          badgeX: (self.x + node.x) / 2,
          badgeY: (self.y + node.y) / 2
        });
      });
      return edges;
    },
    canvasHasEdges() {
      return this.canvasEdges.length > 0;
    },
    selectedPairRules() {
      if (!this.selectedPair) {
        return [];
      }
      return this.meshRulesByPeer.get(this.selectedPair) || [];
    },
    selectedNodeRules() {
      if (!this.selectedNode) {
        return [];
      }
      return this.meshRulesByPeer.get(this.selectedNode) || [];
    },
    // 节点被依赖计数:自环=入向引用数（规则目标==本机,即对端指回来的数）
    selectedNodeDependedBy() {
      if (!this.selectedNode) {
        return 0;
      }
      return this.meshRuleCounts[this.selectedNode] || 0;
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
    this.restoreNodePositions();
    // 全局 pointermove/up:拖拽跨出节点矩形仍跟手;卸载时移除（避免泄漏）
    this.boundPointerMove = (event) => this.onDragMove(event);
    this.boundPointerUp = () => this.endDrag();
    window.addEventListener('pointermove', this.boundPointerMove);
    window.addEventListener('pointerup', this.boundPointerUp);
  },
  beforeUnmount() {
    window.removeEventListener('pointermove', this.boundPointerMove);
    window.removeEventListener('pointerup', this.boundPointerUp);
  },
  methods: {
    // —— 画布位置持久（localStorage:device_id → {x,y}）——
    restoreNodePositions() {
      try {
        const raw = window.localStorage.getItem(NODE_POSITIONS_STORAGE_KEY);
        if (!raw) {
          return;
        }
        const parsed = JSON.parse(raw);
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          this.nodePositions = parsed;
        }
      } catch {
        // 解析失败/存储不可用:退回自动布局（不阻断渲染）
      }
    },
    persistNodePositions() {
      try {
        window.localStorage.setItem(NODE_POSITIONS_STORAGE_KEY, JSON.stringify(this.nodePositions));
      } catch {
        // 存储配额/隐私模式:位置不持久,不影响本次会话内拖拽
      }
    },
    nodePositionOf(deviceId, autoX, autoY) {
      const saved = this.nodePositions[deviceId];
      if (
        saved &&
        typeof saved === 'object' &&
        Number.isFinite(saved.x) &&
        Number.isFinite(saved.y)
      ) {
        return {
          x: this.clampX(saved.x),
          y: this.clampY(saved.y)
        };
      }
      return { x: this.clampX(autoX), y: this.clampY(autoY) };
    },
    // 收口:夹进画布可视区 + 取整（自动布局的三角值会带浮点尾巴,进 DOM 属性
    // 与 localStorage 都该是整数;NaN/非数一律退回左上角边界）
    clampX(x) {
      const value = Number(x);
      const safe = Number.isFinite(value) ? value : NODE_HALF_W + 4;
      return Math.round(Math.min(Math.max(safe, NODE_HALF_W + 4), CANVAS_WIDTH - NODE_HALF_W - 4));
    },
    clampY(y) {
      const value = Number(y);
      const safe = Number.isFinite(value) ? value : NODE_HALF_H + 4;
      return Math.round(Math.min(Math.max(safe, NODE_HALF_H + 4), CANVAS_HEIGHT - NODE_HALF_H - 4));
    },
    // 具名方法供测试直接调用（jsdom 不合成 pointer 事件）
    setNodePosition(deviceId, x, y) {
      const raw = String(deviceId || '').trim();
      if (!raw) {
        return;
      }
      // 本机节点（含占位 id 过渡态）统一落 SELF_POSITION_KEY
      const key = raw === this.selfDeviceId ? SELF_POSITION_KEY : raw;
      this.nodePositions = {
        ...this.nodePositions,
        [key]: { x: this.clampX(Number(x)), y: this.clampY(Number(y)) }
      };
      this.persistNodePositions();
    },
    // —— 拖拽（pointer 事件 → viewBox 坐标）——
    beginDrag(deviceId, event) {
      this.dragging = deviceId;
      // 阻止默认拖拽/文本选择,避免与点击选中撞车
      if (event && typeof event.preventDefault === 'function') {
        event.preventDefault();
      }
    },
    onDragMove(event) {
      if (!this.dragging) {
        return;
      }
      const svg = this.$el?.querySelector('[data-mesh-canvas]');
      if (!svg || typeof svg.getBoundingClientRect !== 'function') {
        return;
      }
      const rect = svg.getBoundingClientRect();
      if (!rect || !rect.width || !rect.height) {
        return; // jsdom 无布局:拖拽在测试外无意义,直接返回
      }
      const x = ((event.clientX - rect.left) / rect.width) * CANVAS_WIDTH;
      const y = ((event.clientY - rect.top) / rect.height) * CANVAS_HEIGHT;
      this.setNodePosition(this.dragging, x, y);
    },
    endDrag() {
      this.dragging = null;
    },
    // —— 选中（点连线=节点对详情;点节点=节点详情;互斥）——
    selectPair(peerId) {
      this.selectedPair = this.selectedPair === peerId ? '' : peerId;
      this.selectedNode = '';
    },
    selectNode(deviceId) {
      this.selectedNode = this.selectedNode === deviceId ? '' : deviceId;
      this.selectedPair = '';
    },
    peerReported(deviceId) {
      const statusPeers = Array.isArray(this.meshStatus?.peers) ? this.meshStatus.peers : [];
      return statusPeers.some((peer) => String(peer?.device_id || '') === deviceId);
    },
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
