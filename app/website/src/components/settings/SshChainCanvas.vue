<template>
  <section data-ssh-chain-canvas class="space-y-2">
    <div
      v-if="emptyReason"
      class="rounded-xl border border-dashed border-slate-300 bg-slate-50/60 px-6 py-8 text-center text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40"
      data-ssh-chain-empty
    >
      {{ emptyReason }}
    </div>
    <svg
      v-else
      class="w-full rounded-xl border border-slate-200 bg-slate-50/40 dark:border-slate-800 dark:bg-slate-900/40"
      role="img"
      :viewBox="viewBox"
      data-ssh-chain-svg
    >
      <g data-ssh-chain-edges>
        <line
          v-for="(edge, i) in edges"
          :key="`edge-${i}`"
          :x1="edge.x1"
          :y1="42"
          :x2="edge.x2"
          :y2="42"
          stroke="#94a3b8"
          stroke-width="2"
        />
      </g>
      <g data-ssh-chain-nodes>
        <g v-for="(node, i) in nodeLayout" :key="`node-${i}`">
          <rect
            :x="node.x"
            y="16"
            :width="node.width"
            height="52"
            rx="10"
            fill="#f8fafc"
            stroke="#cbd5e1"
            stroke-width="1.5"
          />
          <text :x="node.cx" :y="node.addr ? 36 : 46" text-anchor="middle" font-size="13" font-weight="600" fill="#0f172a">
            {{ node.label }}
          </text>
          <text v-if="node.addr" :x="node.cx" y="56" text-anchor="middle" font-size="11" fill="#64748b">
            {{ node.addr }}
          </text>
        </g>
      </g>
      <g data-ssh-chain-lanes>
        <g v-for="(lane, i) in lanes" :key="`lane-${i}`">
          <circle :cx="26" :cy="laneCy(i)" r="5" :fill="lane.color" />
          <text :x="40" :y="laneCy(i) + 4" font-size="12" font-weight="500" fill="#334155">{{ lane.label }}</text>
          <text x="200" :y="laneCy(i) + 4" font-size="12" :fill="lane.color">{{ lane.stateText }}</text>
          <text v-if="lane.activeText" x="614" :y="laneCy(i) + 4" text-anchor="end" font-size="11" fill="#64748b">
            {{ lane.activeText }}
          </text>
        </g>
      </g>
    </svg>
  </section>
</template>

<script>
import { useI18n } from '../../i18n';

// 语义色常量:SVG 属性不吃 Tailwind dark: 变体(mesh 画布 §9.2 同款先例)。
const LANE_COLOR_BACKOFF = '#f59e0b';
const LANE_COLOR_ALIVE = '#22c55e';
const LANE_COLOR_IDLE = '#94a3b8';

export default {
  name: 'SshChainCanvas',
  props: {
    // /api/status/summary 的 data.ssh 整体:{ ok, addr, jumps, jump_addrs?, chains }
    ssh: { type: Object, default: null }
  },
  setup() {
    const { t } = useI18n();
    return { t };
  },
  computed: {
    emptyReason() {
      if (!this.ssh || !this.ssh.ok) {
        return this.t('status_sshNotConfigured');
      }
      if (!Array.isArray(this.ssh.chains) || this.ssh.chains.length === 0) {
        return this.t('rules_sshChainIdleHint');
      }
      return '';
    },
    nodes() {
      const ssh = this.ssh || {};
      const list = [{ label: this.t('status_sshNodeLocal'), addr: '' }];
      const addrs = Array.isArray(ssh.jump_addrs) ? ssh.jump_addrs : [];
      const raw = Number(ssh.jumps);
      const jumpCount = Number.isFinite(raw) && raw > 0 ? raw : addrs.length;
      for (let i = 0; i < jumpCount; i += 1) {
        list.push({ label: this.t('status_sshNodeJump', { index: i + 1 }), addr: addrs[i] || '' });
      }
      list.push({ label: this.t('status_sshNodeExit'), addr: ssh.addr || '' });
      return list;
    },
    nodeLayout() {
      const count = this.nodes.length;
      const maxWidth = 130;
      const margin = 20;
      // 盒宽随节点数收缩(间距 - 8px 保底缝),≥3 跳板不再重叠;count 恒 ≥2(本机+出口),无除零
      const width = Math.min(maxWidth, Math.floor((640 - margin * 2 - maxWidth) / (count - 1)) - 8);
      const span = 640 - margin * 2 - width;
      return this.nodes.map((node, i) => {
        const x = margin + (span * i) / (count - 1);
        return { ...node, x, width, cx: x + width / 2 };
      });
    },
    edges() {
      return this.nodeLayout.slice(1).map((node, i) => {
        const prev = this.nodeLayout[i];
        return { x1: prev.x + prev.width, x2: node.x };
      });
    },
    // 泳道推导单份实现(原 RulesSettings/LogsSettings 页级副本已删,
    // 见 spec §5.2 收敛)。
    lanes() {
      const chains = Array.isArray(this.ssh?.chains) ? this.ssh.chains : [];
      return chains.map((chain) => {
        const index = Number(chain.index || 0) + 1;
        const alive = Boolean(chain.alive);
        const backoff = Boolean(chain.backoff);
        const remaining = Math.max(1, Math.round(Number(chain.backoffRemainingMs || 0) / 1000));
        const activeCount = Number(chain.activeChannels || 0);
        let stateText;
        let color;
        if (backoff) {
          stateText = this.t('status_sshChainBackoffIn', { seconds: remaining });
          color = LANE_COLOR_BACKOFF;
        } else if (alive) {
          stateText = this.t('status_sshChainAlive');
          color = LANE_COLOR_ALIVE;
        } else {
          stateText = this.t('status_sshChainIdle');
          color = LANE_COLOR_IDLE;
        }
        return {
          label: this.t('status_sshChainLabel', { index }),
          color,
          stateText,
          activeText: activeCount > 0 ? this.t('status_sshChainActive', { count: activeCount }) : ''
        };
      });
    },
    viewHeight() {
      return 100 + this.lanes.length * 30 + 6;
    },
    viewBox() {
      return `0 0 640 ${this.viewHeight}`;
    }
  },
  methods: {
    laneCy(i) {
      return 100 + i * 30;
    }
  }
};
</script>
