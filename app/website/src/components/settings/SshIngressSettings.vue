<template>
  <!-- SSH 入口服务面卡片（spec §7；代理族下,SettingsPage 主栏 RulesSettings 相邻挂载）。
       线格式 customer.ssh_ingress{enable,port,authorized_keys[],mesh_authorized_keys[]}。
       可选专用口(spec §6.4 修订)：port 留空=跟随统一口 56432;显式端口=另开
       常驻专用监听口。旧默认 56442 是哨兵,显示为空、保存发 0(翻译层归零)。
       提交载荷 = 整配置透传 + ssh_ingress 覆盖（父层按顶层段浅合并,卡片不丢别的段）。 -->
  <div class="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
    <div class="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-primary">login</span>
        <div>
          <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('sshIngress_title') }}</h3>
          <p class="text-xs text-slate-500">{{ t('sshIngress_description') }}</p>
        </div>
      </div>

      <!-- 总开关：三态指针在 UI 上是普通布尔,默认关（nil 与 false 行为等价） -->
      <label
        class="inline-flex cursor-pointer items-center justify-between gap-3 rounded-full border px-3 py-2 text-sm font-medium shadow-sm transition md:min-w-[210px]"
        :class="form.enabled
          ? 'border-primary/20 bg-white text-slate-700 dark:border-primary/30 dark:bg-slate-900 dark:text-slate-200'
          : 'border-slate-200 bg-white/70 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300'"
      >
        <span>{{ t('sshIngress_enable') }}</span>
        <span class="relative">
          <input v-model="form.enabled" class="peer sr-only" data-field="sshIngressEnable" type="checkbox" />
          <span class="relative block h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
        </span>
      </label>
    </div>

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

    <div class="grid gap-4 md:grid-cols-[200px_minmax(0,1fr)]">
      <!-- 端口(可选专用口,spec §6.4 修订):留空=跟随统一口 56432;显式端口
           另开常驻专用监听口。旧默认 56442 是哨兵,显示为空(翻译层归零)。 -->
      <label class="space-y-2">
        <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('sshIngress_port') }}</span>
        <input
          v-model="form.portText"
          class="w-full rounded-lg border px-3 py-2 text-sm shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
          :class="portError
            ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
            : 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900'"
          data-field="sshIngressPort"
          :disabled="loading || saving"
          max="65535"
          min="1"
          :placeholder="t('sshIngress_portPlaceholder')"
          type="number"
          @keydown.enter.prevent
        />
        <p v-if="portError" class="text-xs text-red-500">{{ portError }}</p>
        <p class="text-xs text-slate-500">{{ t('sshIngress_portHint') }}</p>
      </label>

      <!-- host key 指纹区块：只读展示 + 复制;404=入口未启动（生成职责归 Server.Start,
           handler 只读）→ 引导文案;其余失败静默降级 amber,不阻塞主表单 -->
      <div data-ssh-hostkey class="space-y-2 rounded-xl border border-dashed border-slate-300 bg-slate-50/60 p-4 dark:border-slate-700 dark:bg-slate-900/40">
        <div>
          <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('sshIngress_hostkeyTitle') }}</h4>
          <p class="text-xs text-slate-500">{{ t('sshIngress_hostkeyDesc') }}</p>
        </div>

        <p v-if="hostkey.loading" class="text-xs text-slate-500">{{ t('sshIngress_loading') }}</p>
        <div
          v-else-if="hostkey.info"
          class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900"
        >
          <span class="text-slate-500 dark:text-slate-400">{{ hostkey.info.algorithm }}</span>
          <code class="break-all font-mono text-[11px] text-slate-600 dark:text-slate-300">{{ hostkey.info.fingerprint }}</code>
          <span class="text-slate-400">{{ hostkey.info.generatedAt }}</span>
          <button
            type="button"
            class="ml-auto inline-flex items-center gap-1 rounded border border-slate-200 px-2 py-1 font-medium text-slate-600 transition hover:border-primary/30 hover:text-primary dark:border-slate-700 dark:text-slate-300"
            data-ssh-hostkey-copy
            :disabled="saving"
            @click="copyFingerprint"
          >
            <span class="material-symbols-outlined text-sm">content_copy</span>
            {{ copied ? t('sshIngress_hostkeyCopied') : t('sshIngress_hostkeyCopy') }}
          </button>
        </div>
        <p v-else-if="hostkey.notStarted" class="text-xs text-slate-500">{{ t('sshIngress_hostkeyNotStarted') }}</p>
        <p v-else class="text-xs text-amber-600 dark:text-amber-400">{{ t('sshIngress_hostkeyLoadFailed') }}</p>
      </div>
    </div>

    <!-- 人用授权公钥：粘贴添加（逐行）+ 删除 -->
    <section class="space-y-2">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('sshIngress_authorizedKeysTitle') }}</h4>
        <p class="text-xs text-slate-500">{{ t('sshIngress_authorizedKeysDesc') }}</p>
      </div>
      <div class="flex flex-col gap-2 sm:flex-row">
        <textarea
          v-model="keyDraft"
          class="min-h-16 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-xs text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          data-field="sshIngressKeyDraft"
          :disabled="loading || saving"
          :placeholder="t('sshIngress_authorizedKeysPlaceholder')"
          @keydown.enter.prevent
        ></textarea>
        <button
          type="button"
          class="inline-flex h-11 shrink-0 items-center gap-1.5 rounded-lg border border-slate-200 px-3 text-xs font-semibold text-slate-600 transition hover:border-primary/30 hover:text-primary disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
          data-ssh-key-add
          :disabled="loading || saving"
          @click="addKeys"
        >
          <span class="material-symbols-outlined text-sm">add</span>
          {{ t('sshIngress_authorizedKeysAdd') }}
        </button>
      </div>
      <p v-if="!form.authorizedKeys.length" class="text-xs text-slate-500">{{ t('sshIngress_authorizedKeysEmpty') }}</p>
      <ul v-else class="space-y-1.5">
        <li
          v-for="(key, index) in form.authorizedKeys"
          :key="`${index}:${key}`"
          data-ssh-key-row
          class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900"
        >
          <code class="break-all font-mono text-[11px] text-slate-600 dark:text-slate-300" :title="key">{{ truncateKey(key) }}</code>
          <button
            type="button"
            class="ml-auto text-slate-400 transition hover:text-red-600 dark:hover:text-red-300"
            data-ssh-key-remove
            :aria-label="t('sshIngress_authorizedKeysRemove')"
            :disabled="saving"
            @click="removeKey(index)"
          >
            <span class="material-symbols-outlined text-base">delete</span>
          </button>
        </li>
      </ul>
    </section>

    <!-- mesh 互连公钥：只读展示。编辑入口归组网页（与 mesh 分支的前端集成缝——
         后端字段与准入已就绪,组网页配对时生成密钥对并分发进本列表;此处不做
         编辑 UI,避免两处写同一字段漂移）。 -->
    <section class="space-y-2">
      <div>
        <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('sshIngress_meshKeysTitle') }}</h4>
        <p class="text-xs text-slate-500">{{ t('sshIngress_meshKeysDesc') }}</p>
      </div>
      <p v-if="!form.meshKeys.length" class="text-xs text-slate-500">{{ t('sshIngress_meshKeysEmpty') }}</p>
      <ul v-else class="space-y-1.5">
        <li
          v-for="(key, index) in form.meshKeys"
          :key="`mesh-${index}:${key}`"
          data-ssh-mesh-row
          class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900/60"
        >
          <span class="material-symbols-outlined text-sm text-slate-400">lock</span>
          <code class="break-all font-mono text-[11px] text-slate-600 dark:text-slate-300" :title="key">{{ truncateKey(key) }}</code>
        </li>
      </ul>
    </section>

    <!-- Bind host 语义提示：默认仅监听 127.0.0.1 是安全默认,不是缺陷 -->
    <p class="text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('sshIngress_bindHostHint') }}</p>

    <p v-if="validationError" class="text-xs text-red-500" role="alert">{{ validationError }}</p>

    <div class="flex items-center justify-end gap-3">
      <button
        id="sshIngressSaveBtn"
        class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        data-ssh-save
        type="button"
                :disabled="loading || saving || !!validationError || !!portError"
        @click="save"
      >
        <span class="material-symbols-outlined text-sm">save</span>
        {{ saving ? t('sshIngress_saving') : t('sshIngress_save') }}
      </button>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

function normalizeStringList(items) {
  return Array.isArray(items)
    ? items.map((item) => String(item).trim()).filter(Boolean)
    : [];
}

// 线格式归一（customer.ssh_ingress）。具名导出两处共用（RulesSettings.vue 具名导出
// 先例）：SettingsPage 的 createDefaultCustomerConfig/normalizeCustomerConfig 链,与
// SshIngressSettings.test.js。口径与后端 SSHIngressConfig 对齐：enable 三态指针
// nil=false（读侧非布尔一律 false）。port 语义(可选专用口,spec §6.4 修订)：
// 0=跟随统一口 56432;显式端口=另开常驻专用监听口;56442 是旧默认哨兵——
// 读侧归一保留,显示层映射为空、保存发 0(翻译层归零)。
export function normalizeSSHIngressConfig(payload) {
  const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
  const rawPort = source.port;
  return {
    enable: typeof source.enable === 'boolean' ? source.enable : false,
    port: typeof rawPort === 'number' && Number.isFinite(rawPort) && rawPort > 0 && rawPort <= 65535
      ? Math.floor(rawPort)
      : 0,
    authorized_keys: normalizeStringList(source.authorized_keys),
    mesh_authorized_keys: normalizeStringList(source.mesh_authorized_keys)
  };
}

// http 局域网直连（56431 无 TLS）下 navigator.clipboard 不存在,退 execCommand
// （ChatMarkdown.vue copyText 同款;安全上下文的判定即 clipboard 的存在性本身,
// 拒绝已由 then 的第二参兜底）;两者都尽力而为,不抛错。
function copyText(text) {
  if (typeof navigator !== 'undefined' && navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    return navigator.clipboard.writeText(text).then(() => true, () => false);
  }
  if (typeof document === 'undefined') {
    return Promise.resolve(false);
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (_) { /* 尽力而为 */ }
  ta.remove();
  return Promise.resolve(ok);
}

export default {
  name: 'SshIngressSettings',
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
      keyDraft: '',
      hostkey: { loading: false, failed: false, notStarted: false, info: null },
      copied: false,
      copiedTimer: null
    };
  },
  computed: {
    // 校验与后端 SSHIngressConfig.Validate「enable 时两列表合计非空」对齐;
    // 文案是提示层,后端保存时仍是权威。
    validationError() {
      if (!this.form.enabled) {
        return '';
      }
      if (!this.form.authorizedKeys.length && !this.form.meshKeys.length) {
        return this.t('sshIngress_validationNeedKey');
      }
      return '';
    },
    portError() {
      const raw = String(this.form.portText ?? '').trim();
      if (!raw) {
        return '';
      }
      const port = Number(raw);
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        return this.t('sshIngress_portInvalid');
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
    // 保存成功后入口可能刚被启用——host key 由 Server.Start 生成（reconcile 随
    // 配置提交启停）,重拉一次让「启用并保存后自动生成」引导当场过渡为真指纹。
    // 其他卡片保存的成功消息也会触发,重拉是幂等 GET,无害。
    successMessage(message) {
      if (message) {
        this.loadHostkey();
      }
    }
  },
  mounted() {
    this.loadHostkey();
  },
  beforeUnmount() {
    if (this.copiedTimer) {
      clearTimeout(this.copiedTimer);
    }
  },
  methods: {
    formFromConfig(config) {
      const normalized = normalizeSSHIngressConfig(config?.ssh_ingress);
      return {
        enabled: normalized.enable,
        // 0=未设置、56442=旧默认哨兵:输入框都显示空(保存发 0,翻译层归零=
        // 跟随统一口 56432);显式端口原样回显。
        portText: normalized.port > 0 && normalized.port !== 56442 ? String(normalized.port) : '',
        authorizedKeys: [...normalized.authorized_keys],
        meshKeys: [...normalized.mesh_authorized_keys]
      };
    },
    // —— host key 指纹（GET /api/proxy/ssh-ingress/hostkey,读 data.hostkey 信封）——
    // 404=入口未启动（生成归 Server.Start,handler 只读）→ 引导文案;其余失败静默
    // 降级 amber,不阻塞主表单。
    async loadHostkey() {
      this.hostkey = { loading: true, failed: false, notStarted: false, info: null };
      try {
        const res = await fetch('/api/proxy/ssh-ingress/hostkey', { credentials: 'same-origin' });
        if (res.status === 404) {
          this.hostkey = { loading: false, failed: false, notStarted: true, info: null };
          return;
        }
        if (!res.ok) {
          throw new Error(`HTTP ${res.status}`);
        }
        const json = await res.json();
        const info = json?.data?.hostkey;
        if (!info?.fingerprint) {
          throw new Error('empty hostkey envelope');
        }
        this.hostkey = {
          loading: false,
          failed: false,
          notStarted: false,
          info: {
            fingerprint: String(info.fingerprint),
            algorithm: String(info.algorithm || ''),
            generatedAt: String(info.generated_at || '')
          }
        };
      } catch {
        this.hostkey = { loading: false, failed: true, notStarted: false, info: null };
      }
    },
    async copyFingerprint() {
      const fingerprint = this.hostkey.info?.fingerprint;
      if (!fingerprint) {
        return;
      }
      const ok = await copyText(fingerprint);
      if (!ok) {
        return;
      }
      this.copied = true;
      if (this.copiedTimer) {
        clearTimeout(this.copiedTimer);
      }
      this.copiedTimer = setTimeout(() => {
        this.copied = false;
      }, 1500);
    },
    // —— 人用授权公钥：粘贴逐行添加（空行剔除、与既有键去重）——
    addKeys() {
      const lines = String(this.keyDraft || '')
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean);
      if (!lines.length) {
        return;
      }
      // 去重覆盖两处：既有列表 + 同批粘贴内（同一批两把同键只入一次）
      const seen = new Set(this.form.authorizedKeys);
      const added = [];
      for (const line of lines) {
        if (!seen.has(line)) {
          seen.add(line);
          added.push(line);
        }
      }
      if (added.length) {
        this.form.authorizedKeys.push(...added);
      }
      this.keyDraft = '';
    },
    removeKey(index) {
      this.form.authorizedKeys.splice(index, 1);
    },
    truncateKey(key) {
      const value = String(key || '');
      return value.length > 72 ? `${value.slice(0, 72)}…` : value;
    },
    // 提交载荷：整配置透传 + ssh_ingress 覆盖（ModelMappingSettings/PeerMCPSettings
    // 同款）。端口取输入:留空发 0(=跟随统一口 56432),显式端口原样(后端翻译
    // 层把 56442 哨兵归零);disable 置零口径：enable:false + 其余字段照发——
    // enable=false 时后端 Validate 跳过钥匙校验,照发不会误拦。
    save() {
      if (this.validationError || this.portError) {
        return;
      }
      const rawPort = String(this.form.portText ?? '').trim();
      const port = rawPort ? Number(rawPort) : 0;
      this.$emit('save', {
        ...(this.config || {}),
        ssh_ingress: {
          enable: !!this.form.enabled,
          port,
          authorized_keys: [...this.form.authorizedKeys],
          mesh_authorized_keys: [...this.form.meshKeys]
        }
      });
    }
  }
};
</script>
