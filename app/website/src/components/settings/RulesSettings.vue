<template>
  <div class="settings-pane flex min-h-[calc(100vh-14rem)] flex-1 flex-col" data-pane="rules">
    <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
      <div class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div>
          <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('rules_title') }}</h2>
          <p class="text-sm text-slate-500">
            {{ t('rules_description') }}
          </p>
        </div>
        <div class="flex flex-col items-start gap-2 md:items-end">
          <span class="rounded bg-primary/10 px-3 py-1 text-[11px] font-bold uppercase tracking-wide text-primary">
            {{ loading ? t('rules_loading') : t('rules_customerOnly') }}
          </span>
          <span v-if="version" class="text-[11px] text-slate-400">{{ t('rules_version', { version }) }}</span>
        </div>
      </div>

      <div
        v-if="error || submitError"
        class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
        role="alert"
      >
        {{ error || submitError }}
      </div>

      <!-- 软警告（不阻断保存）：AI 规则与代理规则条件重叠，AI 恒优先（HIGH#3） -->
      <div
        v-if="!loading && overlapWarning"
        class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
        role="status"
      >
        {{ overlapWarning }}
      </div>

      <div v-if="loading" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-8 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
        {{ t('rules_loadingConfig') }}
      </div>

      <form v-else class="space-y-6" @submit.prevent="handleSubmit">
        <section
          class="rounded-xl border p-4 transition-all"
          :class="form.proxy.enable
            ? 'border-slate-200 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-900/40'
            : 'border-slate-200 bg-slate-100/90 opacity-70 saturate-[0.8] dark:border-slate-800 dark:bg-slate-900/70'"
        >
          <div class="mb-4 flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
            <div class="flex items-center gap-2">
              <span class="material-symbols-outlined text-primary">vpn_key</span>
              <div>
                <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_customerProxy') }}</h3>
                <p class="text-xs text-slate-500">{{ t('rules_proxyDesc') }}</p>
              </div>
            </div>

            <label
              class="inline-flex cursor-pointer items-center justify-between gap-3 rounded-full border px-3 py-2 text-sm font-medium shadow-sm transition md:min-w-[210px]"
              :class="form.proxy.enable
                ? 'border-primary/20 bg-white text-slate-700 dark:border-primary/30 dark:bg-slate-900 dark:text-slate-200'
                : 'border-slate-200 bg-white/70 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300'"
            >
              <span>{{ t('rules_enableProxy') }}</span>
              <span class="relative">
                <input v-model="form.proxy.enable" class="peer sr-only" type="checkbox" />
                <span class="relative block h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
              </span>
            </label>
          </div>

          <div class="grid gap-4 md:grid-cols-[180px_minmax(0,1fr)]">
            <label class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyType') }}</span>
              <select
                v-model="form.proxy.type"
                data-field="proxyType"
                :disabled="!form.proxy.enable"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
              >
                <option value="socks5">SOCKS5</option>
                <option value="http">HTTP</option>
                <option value="ssh">SSH</option>
              </select>
            </label>

            <label class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_server') }}</span>
              <input
                v-model.trim="form.proxy.server"
                :disabled="!form.proxy.enable"
                class="w-full rounded-lg border px-3 py-2 text-sm shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
                :class="serverFieldClass"
                placeholder="127.0.0.1:1080"
                type="text"
                @keydown.enter.prevent
              />
              <p v-if="serverError" class="text-xs text-red-500">{{ serverError }}</p>
            </label>

            <label class="space-y-2 md:col-start-1">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyUsername') }}</span>
              <input
                v-model.trim="form.proxy.username"
                data-field="proxyUsername"
                :disabled="!form.proxy.enable"
                autocomplete="username"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                :placeholder="isSshProxy ? t('rules_proxyUsernamePlaceholderSsh') : t('rules_proxyUsernamePlaceholder')"
                type="text"
                @keydown.enter.prevent
              />
            </label>

            <!-- 密码输入仅 socks5/http 走共享网格；SSH 的密码收进下方「认证方式」tab（与私钥二选一） -->
            <label v-if="!isSshProxy" class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyPassword') }}</span>
              <input
                v-model="form.proxy.password"
                data-field="proxyPassword"
                :disabled="!form.proxy.enable"
                autocomplete="current-password"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                :placeholder="t('rules_proxyPasswordPlaceholder')"
                type="password"
                @keydown.enter.prevent
              />
            </label>
          </div>

          <!-- SSH 专属字段（type==='ssh' 时可见；字段值随类型切换保留，仅提交载荷按类型置零） -->
          <div v-if="isSshProxy" data-ssh-fields class="mt-2 space-y-4 border-t border-slate-200 pt-4 dark:border-slate-800">
            <p v-if="sshValidationError" class="text-xs text-red-500" role="alert">{{ sshValidationError }}</p>

            <!-- 认证方式 tab：密码/私钥二选一。切换仅改显示面，两侧已填的值都保留在表单
                 状态里不丢；提交载荷只带当前 tab 侧凭据、另一侧显式置零（父层对空串凭据
                 字段显式发空串让后端 deep-merge 清旧值）。 -->
            <div data-ssh-auth-tabs role="tablist" class="inline-flex rounded-lg border border-slate-200 bg-slate-100 p-1 dark:border-slate-700 dark:bg-slate-900">
              <button
                type="button"
                data-ssh-auth-tab-password
                role="tab"
                :aria-selected="sshAuthMode === 'password'"
                :disabled="!form.proxy.enable"
                class="inline-flex items-center gap-1 rounded-md px-3 py-1.5 text-xs font-medium transition"
                :class="sshAuthMode === 'password'
                  ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-800 dark:text-white'
                  : 'text-slate-500 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-60 dark:text-slate-400 dark:hover:text-slate-200'"
                @click="setSshAuthMode('password')"
              >
                <span class="material-symbols-outlined text-sm">password</span>
                {{ t('rules_sshAuthModePassword') }}
              </button>
              <button
                type="button"
                data-ssh-auth-tab-key
                role="tab"
                :aria-selected="sshAuthMode === 'key'"
                :disabled="!form.proxy.enable"
                class="inline-flex items-center gap-1 rounded-md px-3 py-1.5 text-xs font-medium transition"
                :class="sshAuthMode === 'key'
                  ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-800 dark:text-white'
                  : 'text-slate-500 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-60 dark:text-slate-400 dark:hover:text-slate-200'"
                @click="setSshAuthMode('key')"
              >
                <span class="material-symbols-outlined text-sm">key</span>
                {{ t('rules_sshAuthModeKey') }}
              </button>
            </div>

            <!-- 密码登录面板 -->
            <label v-if="sshAuthMode === 'password'" data-ssh-auth-password class="block space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyPassword') }}</span>
              <input
                v-model="form.proxy.password"
                data-field="proxyPassword"
                :disabled="!form.proxy.enable"
                autocomplete="current-password"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                :placeholder="t('rules_proxyPasswordPlaceholderSsh')"
                type="password"
                @keydown.enter.prevent
              />
            </label>

            <!-- 私钥登录面板：私钥 + 口令 -->
            <div v-else data-ssh-auth-key class="space-y-4">
              <label class="block space-y-2">
                <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyPrivateKey') }}</span>
                <textarea
                  v-model="form.proxy.private_key"
                  data-field="proxyPrivateKey"
                  :disabled="!form.proxy.enable"
                  autocomplete="off"
                  spellcheck="false"
                  class="min-h-28 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-xs text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                  :placeholder="t('rules_proxyPrivateKeyPlaceholder')"
                ></textarea>
              </label>

              <label class="block space-y-2">
                <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyKeyPassphrase') }}</span>
                <input
                  v-model="form.proxy.key_passphrase"
                  data-field="proxyKeyPassphrase"
                  :disabled="!form.proxy.enable"
                  autocomplete="new-password"
                  class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                  :placeholder="t('rules_proxyKeyPassphrasePlaceholder')"
                  type="password"
                  @keydown.enter.prevent
                />
              </label>
            </div>

            <div class="grid gap-4">
              <label class="space-y-2">
                <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyPoolSize') }}</span>
                <input
                  v-model.number="form.proxy.pool_size"
                  data-field="proxyPoolSize"
                  :disabled="!form.proxy.enable"
                  class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                  :placeholder="t('rules_proxyPoolSizePlaceholder')"
                  type="number"
                  min="1"
                  max="16"
                  @keydown.enter.prevent
                />
              </label>
            </div>

            <!-- 跳板链：server 是单串（host:port 或 ssh://user:pass@host:port，线格式契约
                 无独立 port 字段，UI 不拆 host/port 两个输入）；私钥经「展开」细节面板折叠，
                 面板含私钥全文与口令两个输入，控制行高。 -->
            <div class="space-y-2">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('rules_proxyJumpTitle') }}</h4>
                  <p class="text-xs text-slate-500">{{ t('rules_proxyJumpDesc') }}</p>
                </div>
                <button
                  type="button"
                  data-jump-add
                  :disabled="!form.proxy.enable"
                  class="inline-flex items-center gap-1 rounded-lg border border-slate-200 bg-white px-3 py-1.5 text-xs font-medium text-slate-600 shadow-sm transition hover:border-primary/30 hover:text-primary disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
                  @click="addJumpServer"
                >
                  <span class="material-symbols-outlined text-sm">add</span>
                  {{ t('rules_proxyJumpAdd') }}
                </button>
              </div>

              <div
                v-for="(jump, index) in form.proxy.jump_servers"
                :key="`jump-${index}`"
                data-jump-row
                class="space-y-2 rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-900/60"
              >
                <div class="grid gap-2 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_auto] md:items-center">
                  <input
                    v-model.trim="jump.server"
                    data-field="jumpServer"
                    :disabled="!form.proxy.enable"
                    autocomplete="off"
                    spellcheck="false"
                    class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-xs text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    :placeholder="t('rules_proxyJumpServerPlaceholder')"
                    type="text"
                    @keydown.enter.prevent
                  />
                  <input
                    v-model.trim="jump.username"
                    data-field="jumpUsername"
                    :disabled="!form.proxy.enable"
                    autocomplete="off"
                    class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    :placeholder="t('rules_proxyUsername')"
                    type="text"
                    @keydown.enter.prevent
                  />
                  <div v-if="jumpAuthMode(jump) === 'password'" class="flex items-center gap-1">
                    <input
                      v-model="jump.password"
                      data-field="jumpPassword"
                      :disabled="!form.proxy.enable"
                      autocomplete="new-password"
                      class="w-full min-w-0 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                      :placeholder="t('rules_proxyPassword')"
                      type="password"
                      @keydown.enter.prevent
                    />
                    <button
                      type="button"
                      data-jump-switch-to-key
                      :aria-label="t('rules_sshUseKeyAuth')"
                      :title="t('rules_sshUseKeyAuth')"
                      :disabled="!form.proxy.enable"
                      class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-lg text-slate-400 transition hover:bg-primary/5 hover:text-primary disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-primary/10 dark:hover:text-primary"
                      @click="setJumpAuthMode(jump, 'key')"
                    >
                      <span class="material-symbols-outlined text-base">key</span>
                    </button>
                  </div>
                  <button
                    v-else
                    type="button"
                    data-jump-switch-to-password
                    :disabled="!form.proxy.enable"
                    class="inline-flex items-center justify-center gap-1 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-medium text-slate-500 shadow-sm transition hover:border-primary/30 hover:text-primary disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
                    @click="setJumpAuthMode(jump, 'password')"
                  >
                    <span class="material-symbols-outlined text-sm">password</span>
                    {{ t('rules_sshUsePasswordAuth') }}
                  </button>
                  <button
                    type="button"
                    data-jump-remove
                    :aria-label="t('rules_proxyJumpRemove')"
                    :disabled="!form.proxy.enable"
                    class="inline-flex h-9 w-9 items-center justify-center rounded-lg text-slate-400 transition hover:bg-red-50 hover:text-red-600 disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-red-500/10 dark:hover:text-red-300"
                    @click="removeJumpServer(index)"
                  >
                    <span class="material-symbols-outlined text-base">delete</span>
                  </button>
                </div>

                <!-- 私钥面板：登录方式为私钥时替换密码输入（私钥全文 + 可选口令）。
                     跳板行密码/私钥互斥，payload 按当前侧收敛；切回密码登录用上方格子按钮。 -->
                <div v-if="jumpAuthMode(jump) === 'key'" data-jump-key-panel class="space-y-2 rounded-lg bg-slate-50 p-3 dark:bg-slate-900/70">
                  <textarea
                    v-model="jump.private_key"
                    data-field="jumpPrivateKey"
                    :disabled="!form.proxy.enable"
                    autocomplete="off"
                    spellcheck="false"
                    class="min-h-24 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-xs text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    :placeholder="t('rules_proxyPrivateKeyPlaceholder')"
                  ></textarea>
                  <input
                    v-model="jump.key_passphrase"
                    data-field="jumpKeyPassphrase"
                    :disabled="!form.proxy.enable"
                    autocomplete="new-password"
                    class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    :placeholder="t('rules_proxyKeyPassphrase')"
                    type="password"
                    @keydown.enter.prevent
                  />
                </div>
              </div>
            </div>

            <!-- 连接测试 + 链路拓扑画布（客户端连接状态反馈）：测试按钮做一次性
                 主动验证（当前表单凭据,未保存也可测）；拓扑画布做持续监控（已保存
                 并注册的出站链池,来自 /api/status/summary 的 ssh 字段）。两者
                 互补：表单值 ≠ 运行态,按钮测「我现在填的能不能连」,画布看
                 「当前生效的链路健康如何」。 -->
            <div data-ssh-connectivity class="space-y-3 rounded-xl border border-dashed border-slate-300 bg-slate-50/60 p-4 dark:border-slate-700 dark:bg-slate-900/40">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('rules_sshTestTitle') }}</h4>
                  <p class="text-xs text-slate-500">{{ t('rules_sshTestDesc') }}</p>
                </div>
                <button
                  type="button"
                  data-ssh-test-button
                  :disabled="!form.proxy.enable || sshTest.loading || !!sshValidationError"
                  class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-600 shadow-sm transition hover:border-primary/30 hover:text-primary disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
                  @click="runSshTest"
                >
                  <span class="material-symbols-outlined text-sm">{{ sshTest.loading ? 'hourglass_top' : 'network_check' }}</span>
                  {{ sshTest.loading ? t('rules_sshTestRunning') : t('rules_sshTestButton') }}
                </button>
              </div>

              <!-- 测试结果条：成功（延迟+指纹）/失败（阶段文案+原始错误） -->
              <p
                v-if="sshTest.result"
                data-ssh-test-result
                role="status"
                class="rounded-lg px-3 py-2 text-xs"
                :class="sshTest.result.ok
                  ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
                  : 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'"
              >
                {{ sshTestResultText }}
              </p>

              <!-- 链路拓扑画布：吃 summary.ssh 整体快照；未配置/未运行/瞬空
                   的空态由组件内自渲染（SshChainCanvas 单份泳道推导）。 -->
              <SshChainCanvas :ssh="sshStatusRaw" />
            </div>

            <!-- 服务器指纹（TOFU 库管理）：加载失败静默降级，不阻塞主表单 -->
            <div data-known-hosts class="space-y-3 rounded-xl border border-dashed border-slate-300 bg-slate-50/60 p-4 dark:border-slate-700 dark:bg-slate-900/40">
              <div>
                <h4 class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('rules_knownHostsTitle') }}</h4>
                <p class="text-xs text-slate-500">{{ t('rules_knownHostsDesc') }}</p>
              </div>

              <p v-if="knownHosts.loading" class="text-xs text-slate-500">{{ t('rules_loading') }}</p>
              <p v-else-if="knownHosts.failed" class="text-xs text-amber-600 dark:text-amber-400">{{ t('rules_knownHostsLoadFailed') }}</p>
              <p v-else-if="!knownHosts.items.length" class="text-xs text-slate-500">{{ t('rules_knownHostsEmpty') }}</p>
              <ul v-else class="space-y-2">
                <li
                  v-for="entry in knownHosts.items"
                  :key="`${entry.host}:${entry.port}:${entry.fingerprint}`"
                  :data-known-host="`${entry.host}:${entry.port}`"
                  class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900"
                >
                  <span class="font-mono font-semibold text-slate-700 dark:text-slate-200">{{ entry.host }}:{{ entry.port }}</span>
                  <span class="text-slate-500 dark:text-slate-400">{{ entry.algorithm }}</span>
                  <code
                    class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
                    :title="entry.fingerprint"
                  >{{ truncateFingerprint(entry.fingerprint) }}</code>
                  <span class="text-slate-400">{{ entry.added_at }}</span>
                  <button
                    type="button"
                    data-known-host-remove
                    :aria-label="t('rules_knownHostsRemove') + '：' + entry.host + ':' + entry.port"
                    class="ml-auto text-slate-400 transition hover:text-red-600 dark:hover:text-red-300"
                    @click="removeKnownHost(entry)"
                  >
                    <span class="material-symbols-outlined text-base">delete</span>
                  </button>
                </li>
              </ul>

              <button
                v-if="knownHosts.items.length"
                type="button"
                class="text-xs font-medium text-red-500 transition hover:text-red-600 dark:text-red-400"
                @click="clearKnownHosts"
              >
                {{ t('rules_knownHostsClear') }}
              </button>
            </div>
          </div>
        </section>

        <section class="rounded-xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-800 dark:bg-slate-900/40">
          <div class="mb-4 flex items-center gap-2">
            <span class="material-symbols-outlined text-primary">robot_2</span>
            <div>
              <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_aiRules') }}</h3>
              <p class="text-xs text-slate-500">{{ t('rules_aiRulesDesc') }}</p>
            </div>
          </div>

          <div v-if="!providerOrder.length" class="rounded-lg border border-dashed border-slate-300 bg-white px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-background-dark dark:text-slate-400">
            {{ t('rules_noProviders') }}
          </div>

          <div v-else class="grid gap-4 xl:grid-cols-2">
            <template v-for="provider in providerOrder" :key="provider">
            <article
              v-if="form.ai_rules[provider]"
              class="relative rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-background-dark"
            >
              <div class="mb-3 flex items-center justify-between gap-3">
                <div>
                  <h4 class="font-semibold text-slate-900 dark:text-white">{{ providerLabel(provider, presetProviders) }}</h4>
                  <p class="text-xs text-slate-500">{{ provider }}</p>
                </div>
                <label class="inline-flex min-h-9 cursor-pointer items-center gap-2 text-xs font-semibold text-slate-600 dark:text-slate-200 md:min-h-0">
                  <!-- LOW#17：可访问名组入 provider 名，避免 N 个开关同名（aria-label 优先于包裹 label 文本） -->
                  <input v-model="form.ai_rules[provider].enble" class="peer sr-only" type="checkbox" :aria-label="t('rules_enableAiRule') + '：' + providerLabel(provider, presetProviders)" />
                  <span class="sr-only">{{ t('rules_enableAiRule') }}：{{ providerLabel(provider, presetProviders) }}</span>
                  <span class="relative h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
                </label>
              </div>

              <div v-if="providerEditable(provider)" class="relative" :data-provider-editor-root="provider">
                <button
                  type="button"
                  class="flex w-full items-center justify-between gap-3 rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-left transition hover:border-primary/20 hover:bg-primary/5 dark:border-slate-800 dark:bg-slate-900/60 dark:hover:border-primary/30 dark:hover:bg-primary/10"
                  :aria-expanded="isProviderEditorOpen(provider)"
                  @click.stop="toggleProviderEditor(provider)"
                >
                  <span class="flex min-w-0 flex-col items-start gap-0.5">
                    <span class="text-xs font-normal text-slate-500 dark:text-slate-400">{{ t('rules_includeDomains') }}</span>
                    <!-- LOW#16：收起态副文案——空配置提示 / 单条目预览 / first+N more -->
                    <span v-if="!isProviderEditorOpen(provider)" class="max-w-full truncate text-xs text-slate-400 dark:text-slate-500">
                      {{ providerIncludeSummary(provider) }}
                    </span>
                  </span>
                  <span class="inline-flex shrink-0 items-center rounded-full bg-white px-2 py-0.5 text-[11px] font-medium text-slate-500 shadow-sm ring-1 ring-slate-200 dark:bg-slate-950 dark:text-slate-400 dark:ring-slate-800">
                    {{ isProviderEditorOpen(provider) ? t('rules_includeDomainsClose') : t('rules_includeDomainsEdit') }}
                  </span>
                </button>

                <div
                  v-if="isProviderEditorOpen(provider)"
                  class="mt-2 rounded-2xl border border-slate-200 bg-white p-4 shadow-2xl md:absolute md:inset-x-0 md:top-full md:z-20 dark:border-slate-700 dark:bg-slate-950"
                  @click.stop
                >
                  <div class="mb-3 flex items-start justify-between gap-3">
                    <h5 class="text-xs font-medium text-slate-600 dark:text-slate-300">
                      {{ t('rules_includeDomainsDialogTitle', { provider: providerLabel(provider, presetProviders) }) }}
                      <!-- LOW#16：草稿实时条目数（与保存按钮同口径的 sanitizeList 计数） -->
                      <span class="ml-2 font-normal text-slate-400">{{ t('rules_includeDomainsCount', { count: providerDraftCount() }) }}</span>
                    </h5>
                    <button
                      type="button"
                      class="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-300"
                      @click="cancelProviderEditor"
                    >
                      <span class="material-symbols-outlined text-base">close</span>
                    </button>
                  </div>

                  <textarea
                    :ref="`providerEditor-${provider}`"
                    v-model="providerEditor.draft"
                    class="min-h-32 w-full rounded-xl border px-3 py-2 font-mono text-sm text-slate-700 shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
                    :class="providerEditorError
                      ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
                      : 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900'"
                    :placeholder="t('rules_includeDomainsPlaceholder')"
                  ></textarea>

                  <p v-if="providerEditorError" class="mt-2 text-xs text-red-500">{{ providerEditorError }}</p>

                  <p class="mt-2 text-xs text-slate-500">{{ t('rules_includeDomainsDialogHint') }}</p>

                  <div class="mt-4 flex items-center justify-end gap-2">
                    <button
                      type="button"
                      class="rounded-lg border border-slate-200 px-3 py-2 text-xs md:px-2.5 md:py-1.5 font-normal text-slate-500 transition hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
                      @click="cancelProviderEditor"
                    >
                      {{ t('rules_includeDomainsCancel') }}
                    </button>
                    <button
                      type="button"
                      class="rounded-lg bg-primary px-3 py-2 text-xs md:px-2.5 md:py-1.5 font-normal text-white transition hover:bg-primary/90"
                      @click="saveProviderEditor"
                    >
                      {{ t('rules_includeDomainsSave') }}
                    </button>
                  </div>
                </div>
              </div>
            </article>
            </template>
          </div>
        </section>

        <section class="rounded-xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-800 dark:bg-slate-900/40">
          <div class="mb-4 flex items-center gap-2">
            <span class="material-symbols-outlined text-primary">rule_settings</span>
            <div>
              <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_proxyRules') }}</h3>
              <p class="text-xs text-slate-500">{{ t('rules_proxyRulesDesc') }}</p>
              <!-- HIGH#5：语法摘要句——glob「只命中子域不命中裸域」等反直觉语义在 UI 上可发现 -->
              <p class="text-xs text-slate-500">{{ t('rules_proxyRulesDescExt') }}</p>
            </div>
            <!-- 复用 SettingsPage 页脚 openTutorialDocs('usage-guide') 先例：模块级共享状态，App.vue 挂载弹窗 -->
            <a
              class="ml-auto min-h-11 whitespace-nowrap text-xs font-medium text-primary hover:underline"
              href="javascript:void(0)"
              @click.prevent="openRulesSyntaxDocs"
            >{{ t('rules_docsLink') }}</a>
          </div>

          <label class="space-y-2">
            <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_rulesList') }}</span>
            <textarea
              :value="_proxyRulesText"
              class="min-h-40 w-full rounded-lg border px-3 py-2 font-mono text-sm text-slate-700 shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
              :class="proxyRulesError
                ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
                : 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900'"
              :placeholder="t('rules_proxyRulesPlaceholder')"
              @input="_proxyRulesText = $event.target.value"
            ></textarea>
            <p v-if="proxyRulesError" class="text-xs text-red-500">{{ proxyRulesError }}</p>
            <!-- 软提示（不阻断保存）：非关键字首段的逗号多域名行按兼容语义仅保留末段（HIGH#4）；
                 红色错误优先展示（先修错误，塌缩提示随后顶上） -->
            <p v-if="!proxyRulesError && proxyRulesCollapseHint" class="text-xs text-amber-600 dark:text-amber-400">{{ proxyRulesCollapseHint }}</p>
          </label>
        </section>

        <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-background-dark md:flex-row md:items-center md:justify-between">
          <div class="text-xs text-slate-500">
            {{ t('rules_formHint') }}
          </div>
          <button
            id="rulesConfigSaveBtn"
            :disabled="saving || !!serverError || !!proxyRulesError || !!sshValidationError"
            class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
            type="submit"
          >
            <span class="material-symbols-outlined text-sm">save</span>
            {{ saving ? t('rules_saving') : t('rules_saveConfig') }}
          </button>
        </div>
      </form>
    </div>

    <div
      v-if="_showSuccessDialog"
      class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/45 p-4 backdrop-blur-sm"
      @click.self="hideSuccessDialog"
    >
      <div class="relative w-full max-w-sm rounded-2xl border border-emerald-200 bg-white p-5 shadow-2xl dark:border-emerald-500/30 dark:bg-slate-900">
        <div class="flex items-start gap-3">
          <div class="flex h-11 w-11 items-center justify-center rounded-full bg-emerald-100 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-300">
            <span class="material-symbols-outlined">check_circle</span>
          </div>
          <div class="min-w-0 flex-1">
            <h3 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('rules_configSaved') }}</h3>
            <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ successMessage }}</p>
          </div>
          <button
            type="button"
            aria-label="Close"
            class="relative inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-500 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            @click="hideSuccessDialog"
          >
            <svg class="pointer-events-none absolute inset-0 h-full w-full -rotate-90" viewBox="0 0 36 36" aria-hidden="true">
              <circle
                cx="18" cy="18" r="15.5"
                fill="none"
                stroke="currentColor"
                stroke-width="2.75"
                class="text-emerald-100 dark:text-emerald-900/60"
              />
              <circle
                cx="18" cy="18" r="15.5"
                fill="none"
                stroke="currentColor"
                stroke-width="2.75"
                stroke-linecap="round"
                :stroke-dasharray="successRingCircumference"
                :stroke-dashoffset="successRingDashoffset"
                class="text-emerald-500 transition-none"
              />
            </svg>
            <span class="material-symbols-outlined relative z-10 text-lg">close</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { openTutorialDocs } from '../../composables/useTutorialDocs';
import { getStatusSummary, testSshConnection } from '../../services/statusApi';
import SshChainCanvas from './SshChainCanvas.vue';

function defaultConfig() {
  return {
    proxy: {
      enable: true,
      type: 'socks5',
      server: '',
      username: '',
      password: '',
      // SSH 专属字段（线格式 customer.proxy；非 ssh 类型提交时置零值，见 handleSubmit）
      private_key: '',
      key_passphrase: '',
      pool_size: '',
      jump_servers: []
    },
    ai_rules: {},
    proxy_rules: []
  };
}

function normalizeStringList(items) {
  return Array.isArray(items)
    ? items.map((entry) => String(entry).trim()).filter(Boolean)
    : [];
}

// 具名导出仅供同目录 RulesSettings.test.js 做分词器回归测试使用
export function sanitizeList(value) {
  return value
    .split(/[\n,;]+/)
    .map((entry) => normalizeIncludeEntry(entry))
    .filter(Boolean);
}

function normalizeIncludeEntry(entry) {
  const trimmed = String(entry || '').trim();
  if (!trimmed) {
    return '';
  }

  if (trimmed.startsWith('//')) {
    try {
      return new URL(`https:${trimmed}`).hostname || trimmed;
    } catch {
      return trimmed;
    }
  }

  if (/^[a-zA-Z][a-zA-Z\d+\-.]*:\/\//.test(trimmed)) {
    try {
      return new URL(trimmed).hostname || trimmed;
    } catch {
      return trimmed;
    }
  }

  return trimmed;
}

function sanitizeLines(value) {
  return value
    .split('\n')
    .map((entry) => entry.trim())
    .filter(Boolean);
}

// —— 与后端 processor/match ClassifyEntry 对齐的前端校验（提示层，后端保存时仍是权威）——
// 已知刻意放宽：区间数值顺序（from ≤ to）由后端强校验，此处不比对。
const ENTRY_CHARS_RE = /^[a-z0-9.*-]+$/;
const IPV4_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
// 逗号兜底分支的尾段动作词集合——与后端 processor/match legacyActionTail 严格一致
// （大小写不敏感：parts 已统一 toLowerCase；仅剥一层尾段）。
// 红线：前端从不拒绝后端接受的输入，两边集合必须同步维护。
const ACTION_TAIL_WORDS = new Set(['proxy', 'direct', 'reject', 'no-resolve']);

function parseIPFamily(s) {
  const m = String(s).match(IPV4_RE);
  if (m && m.slice(1).every((o) => Number(o) <= 255)) return 4;
  if (String(s).includes(':') && /^[0-9a-fA-F:.]+$/.test(s)) return 6;
  return 0;
}

function isValidIPCond(cond) {
  if (cond.includes('/')) {
    const slash = cond.indexOf('/');
    const fam = parseIPFamily(cond.slice(0, slash));
    const bits = cond.slice(slash + 1);
    if (!/^\d{1,3}$/.test(bits)) return false;
    const max = fam === 4 ? 32 : 128;
    return fam > 0 && Number(bits) <= max;
  }
  if (cond.includes('-')) {
    const dash = cond.indexOf('-');
    // 对齐后端 validateIPCond：区间两端 TrimSpace（"a - b" 合法）
    const from = parseIPFamily(cond.slice(0, dash).trim());
    return from > 0 && from === parseIPFamily(cond.slice(dash + 1).trim());
  }
  return parseIPFamily(cond) > 0;
}

// 逗号兜底段提取（非关键字首段）：取尾段；尾段为动作词（ACTION_TAIL_WORDS）时取
// 倒数第二段（仅剥一层，中间段动作词不参与）——与后端 ClassifyEntry 兜底分支一致。
// validateRuleEntry 的结构校验与 extractProxyRuleDomain 的条件提取共用，避免两套逻辑漂移。
function commaFallbackPart(parts) {
  return ACTION_TAIL_WORDS.has(parts[parts.length - 1].trim())
    ? (parts[parts.length - 2] || '')
    : parts[parts.length - 1];
}

// —— mesh 转发尾段（对齐后端 processor/routing splitMeshTail）——
// 语法：任一逗号段以 `mesh:` 开头（大小写不敏感）即 mesh 条目；mesh: 段必须带非空
// deviceID、base 非空，其后至多一个策略段 direct|deny|fallback:<id>（关键字大小写
// 不敏感、fallback 目标非空原样保留）。后端**在 ClassifyEntry 之前**剥离尾段，故前端
// 校验/提取也必须在任何逗号分支之前剥离——否则 `ip-cidr:1.2.3.0/24,mesh:dev-b` 会被
// `ip-cidr:` 冒号分支当 IP 条件整串拒绝、`geoip:cn,mesh:dev-b` 会被判成逗号多国，
// 都是「前端拒绝后端接受的输入」（红线）。
// 形态非法 → state='invalid'：后端整条静默跳过（不回落常规分类，防把 `x.com,mesh:`
// 静默错编译成普通代理规则），前端据此本地报错而不是放行。
const MESH_TAIL_PREFIX = 'mesh:';
const MESH_POLICY_WORDS = new Set(['direct', 'deny']);
const MESH_POLICY_FALLBACK = 'fallback';

// 策略段归一：直通词归一小写；fallback:<id> 关键字忽略大小写、目标 TrimSpace 保留
// 原样；未知段/空目标 → null（后端 normalizeMeshPolicySegment ok=false 同口径）。
function normalizeMeshPolicySegment(seg) {
  const s = String(seg || '').trim();
  if (!s) return null;
  const lower = s.toLowerCase();
  if (MESH_POLICY_WORDS.has(lower)) return lower;
  const colon = s.indexOf(':');
  if (colon > 0 && s.slice(0, colon).toLowerCase() === MESH_POLICY_FALLBACK) {
    const target = s.slice(colon + 1).trim();
    return target ? `${MESH_POLICY_FALLBACK}:${target}` : null;
  }
  return null;
}

// 返回 { base, deviceId, policy, state }；state ∈ 'none'（非 mesh 条目）/
// 'parsed' / 'invalid'（含 mesh: 段但形态不可解析）。具名导出仅供同目录测试。
export function splitMeshRuleTail(raw) {
  const text = String(raw || '');
  const parts = text.split(',');
  let meshIndex = -1;
  for (let i = 0; i < parts.length; i += 1) {
    if (parts[i].trim().toLowerCase().startsWith(MESH_TAIL_PREFIX)) {
      meshIndex = i;
      break;
    }
  }
  if (meshIndex < 0) {
    return { base: text, deviceId: '', policy: '', state: 'none' };
  }
  const tailParts = parts.slice(meshIndex + 1);
  if (tailParts.length > 1) {
    return { base: '', deviceId: '', policy: '', state: 'invalid' };
  }
  let policy = '';
  if (tailParts.length === 1) {
    const normalized = normalizeMeshPolicySegment(tailParts[0]);
    if (normalized === null) {
      return { base: '', deviceId: '', policy: '', state: 'invalid' };
    }
    policy = normalized;
  }
  const deviceId = parts[meshIndex].trim().slice(MESH_TAIL_PREFIX.length).trim();
  if (!deviceId) {
    return { base: '', deviceId: '', policy: '', state: 'invalid' };
  }
  const base = parts.slice(0, meshIndex).join(',').trim();
  if (!base) {
    return { base: '', deviceId: '', policy: '', state: 'invalid' };
  }
  return { base, deviceId, policy, state: 'parsed' };
}

// URL/host:port 的宽容归一提取 + 剥 [] 包裹（对齐后端 classifyDomainEntry 提取段；
// host:port 对齐 net.SplitHostPort，端口可空、不做数字校验；URL 解析失败时保留原串
// 交由字符校验报错）。尾点不剥离——尾点属结构非法，交由空标签检查拒绝。
// validateRuleEntry 与 extractProxyRuleDomain 共用，保证提取语义单一来源。
function normalizeConditionHost(s) {
  if (s.includes('://') || s.startsWith('//')) {
    try {
      s = new URL(s.includes('://') ? s : `https:${s}`).hostname || s;
    } catch {
      // URL 解析失败时保留原串，交给字符校验报错（与后端一致）
    }
  } else {
    // host:port 宽容提取（对齐后端 net.SplitHostPort；端口可空、不做数字校验）
    const hostPort = /^(\[[^\]]+\]|[^:]+):([^:]*)$/.exec(s);
    if (hostPort) {
      s = hostPort[1];
    }
  }
  // 与后端一致：剥 [] 包裹
  return String(s).trim().replace(/^\[(.+)\]$/, '$1');
}

// 返回 '' 表示合法，否则返回 i18n 错误文案。
// 调用面：proxy_rules 文本域按行拆分（逗号原样保留）逐条调用；include 面 sanitizeList
// 先按逗号拆分再调用（条目必无逗号，且 geoip 逗号形态已被 detectGeoipCommaIssue 拆分前拦截）。
// 因此本函数内的 geoip 逗号多国文案只会在 proxy_rules 面露出 → 用不带「拆坏」表述的
// rules_geoipCommaMultiProxy（后端按格式拒绝，非拆坏）；include 面「会被拆坏」文案归 detectGeoipCommaIssue。
// 具名导出仅供同目录 RulesSettings.test.js 做 geoip 形态回归测试使用（与 sanitizeList 同例）。
export function validateRuleEntry(raw, t) {
  let s = String(raw || '').trim();
  if (!s) return '';
  // mesh 转发尾段先剥（对齐后端 splitMeshTail 在 ClassifyEntry 之前剥离）：
  // 若不先剥，`ip-cidr:1.2.3.0/24,mesh:dev-b` 会被下方冒号分支整串拒、
  // `geoip:cn,mesh:dev-b` 会被判逗号多国——都是「前端拒绝后端接受的输入」。
  // 形态非法（空 deviceID/多策略段/未知策略段/空 base）→ 本地报错：后端整条
  // 静默跳过，前端不说的话用户看到的是「保存成功但规则没生效」。
  const meshTail = splitMeshRuleTail(s);
  if (meshTail.state === 'invalid') return t('rules_meshInvalid');
  if (meshTail.state === 'parsed') {
    // base 按常规语法校验（后端同样先剥尾再 ClassifyEntry）；base 内不再含 mesh: 段
    return validateRuleEntry(meshTail.base, t);
  }
  const lower = s.toLowerCase();
  // geoip：冒号风格（多国 + 分隔）——必须先于下方 IP/域名分支短路：
  // geoip 条目含 ':'/'+'（不在 ENTRY_CHARS_RE 字符集），且逗号分支与 host:port 提取都会把它拆坏
  if (lower.startsWith('geoip:')) {
    const rem = s.slice(6).trim();
    if (!rem) return t('rules_geoipInvalid');
    if (rem.includes(',')) return t('rules_geoipCommaMultiProxy');
    const codes = rem.split('+').map((c) => c.trim().toUpperCase()).filter(Boolean);
    if (!codes.length || !codes.every((c) => /^[A-Z]{2}$/.test(c))) return t('rules_geoipInvalid');
    return '';
  }
  // 裸 geoip 关键字：后端 ClassifyEntry 专门拒绝（当域名放行无意义），前端对齐
  if (lower === 'geoip') return t('rules_geoipInvalid');
  if (lower.startsWith('ip-cidr:')) return isValidIPCond(s.slice(8).trim()) ? '' : t('rules_ipInvalid');
  if (lower.startsWith('ip-range:')) return isValidIPCond(s.slice(9).trim()) ? '' : t('rules_ipInvalid');
  if (lower.startsWith('ip-from:')) {
    const from = /ip-from:\s*([0-9a-fA-F:.]+)\s*(?:,|$)/.exec(lower);
    const to = /ip-to:\s*([0-9a-fA-F:.]+)\s*(?:,|$)/.exec(lower);
    return from && to && parseIPFamily(from[1]) > 0 && parseIPFamily(to[1]) > 0 ? '' : t('rules_ipInvalid');
  }
  if (parseIPFamily(lower) > 0 || isValidIPCond(lower)) return '';
  if (lower.includes(',')) {
    const parts = lower.split(',');
    const first = parts[0].trim();
    if (first === 'domain' || first === 'domains') s = parts[1] || '';
    else if (first === 'ip-cidr' || first === 'ip-range') {
      // 对齐后端：关键字与逗号之间允许空白（first 已 trim，分派取代原 startsWith('ip-cidr,') 特判）
      const second = parts[1];
      return second && isValidIPCond(second.trim()) ? '' : t('rules_ipInvalid');
    } else if (first === 'geoip') {
      // 对齐后端 Clash geoip 分支：单国 + 可选 proxy 尾段合法；
      // 其余段再出现两位国家码 = 逗号多国尝试（后端按格式拒绝）→ 提示 + 分隔（proxy 面文案）
      if (parts.length < 2 || !parts[1].trim()) return t('rules_geoipInvalid');
      const second = parts[1].trim().toUpperCase();
      if (!/^[A-Z]{2}$/.test(second)) return t('rules_geoipInvalid');
      for (let i = 2; i < parts.length; i++) {
        if (/^[A-Za-z]{2}$/.test(parts[i].trim())) return t('rules_geoipCommaMultiProxy');
      }
      return '';
    } else {
      s = commaFallbackPart(parts);
    }
  }
  // 与后端一致：URL/host:port 宽容提取 + 剥 [] 包裹（normalizeConditionHost 与
  // extractProxyRuleDomain 共用同一实现）；提取产物若为 IP 形态则按 IP 合法处理；
  // 结构校验前归一大小写；尾点不剥离——尾点属结构非法，交由空标签检查拒绝
  s = normalizeConditionHost(s);
  if (parseIPFamily(s) > 0) return '';
  s = s.toLowerCase();
  if (!ENTRY_CHARS_RE.test(s)) return t('rules_entryInvalidChars');
  if (s.split('.').some((label) => !label)) return t('rules_entryEmptyLabel');
  return '';
}

// —— proxy_rules 条件提取（供重叠比对共用；与 validateRuleEntry / 后端
// processor/match ClassifyEntry 同一兼容语义：冒号关键字取冒号后条件段、
// domain/domains 与 ip-cidr/ip-range 逗号前缀取第二段、兜底取尾段（动作词
// 尾段剥一层）、URL/host:port/[] 提取——提取子步骤与 validateRuleEntry 共用
// commaFallbackPart / normalizeConditionHost，单一来源防漂移）。
// geoip 行（冒号/逗号形）归一为 geoip:code 冒号形，与 include 面条目（sanitizeList
// 产物保留 geoip:CN+HK 原形）同形可比；ip-from 冒号对整行返回（include 面经
// sanitizeList 拆逗号后不存在可比形态，永不误报）。其余产物小写化。
// 返回 '' 表示空条目；本函数不做合法性校验（合法性仍归 validateRuleEntry），
// 非法行照常参与比对无害——红色错误提示已在优先位置展示。
// 具名导出仅供同目录 RulesSettings.test.js 做提取语义回归测试使用。
export function extractProxyRuleDomain(raw) {
  let s = String(raw || '').trim();
  if (!s) return '';
  // mesh 尾段先剥（后端 splitMeshTail 先于 ClassifyEntry）：条件只取 base，
  // 否则 `x.com,mesh:dev-b` 会被当成「保留末段」提取出 'mesh:dev-b'，
  // 与 AI include 的假重叠、（若将来参与去重）错键一并产生。形态非法时
  // 整条返回 ''——非法行不参与比对，红色错误提示已在优先位置展示。
  const meshTail = splitMeshRuleTail(s);
  if (meshTail.state === 'invalid') return '';
  if (meshTail.state === 'parsed') s = meshTail.base;
  const lower = s.toLowerCase();
  if (lower.startsWith('geoip:')) return `geoip:${lower.slice(6).trim()}`;
  if (lower.startsWith('ip-cidr:')) return lower.slice(8).trim();
  if (lower.startsWith('ip-range:')) return lower.slice(9).trim();
  if (lower.startsWith('ip-from:')) return lower;
  if (parseIPFamily(lower) > 0 || isValidIPCond(lower)) return lower;
  if (lower.includes(',')) {
    const parts = lower.split(',');
    const first = parts[0].trim();
    if (first === 'domain' || first === 'domains') s = parts[1] || '';
    else if (first === 'ip-cidr' || first === 'ip-range') return (parts[1] || '').trim();
    else if (first === 'geoip') return `geoip:${(parts[1] || '').trim()}`;
    else s = commaFallbackPart(parts);
  }
  return normalizeConditionHost(s).toLowerCase();
}

// 重叠比对键归一：小写 + 去尾点（比对口径与后端快照去重键 (kind,cond) 的 cond 域
// 一致——域名/IP 条件按字面小写可比，geoip 冒号形两侧同形）。
function normalizeOverlapKey(value) {
  return String(value || '').trim().toLowerCase().replace(/\.+$/, '');
}

// AI include 与 proxy_rules 的条件重叠比对（HIGH#3）：后端快照编译时 ai_allowlist
// 先入 (kind,cond) 共享 seen 表，proxy_rules 同键条目被剪除——AI 恒优先，用户的
// 代理意图被静默反转。includeEntries = 启用 provider 的 include 条目（sanitizeList
// 产物），proxyLines = proxy_rules 各行；返回重叠条目（归一形，排序去重），
// 空数组 = 无重叠。具名导出仅供同目录 RulesSettings.test.js 做比对回归测试使用。
export function findRuleOverlaps(includeEntries, proxyLines) {
  const includeKeys = new Set();
  for (const entry of includeEntries || []) {
    const key = normalizeOverlapKey(entry);
    if (key) includeKeys.add(key);
  }
  const overlaps = new Set();
  for (const line of proxyLines || []) {
    const key = normalizeOverlapKey(extractProxyRuleDomain(line));
    if (key && includeKeys.has(key)) overlaps.add(key);
  }
  return [...overlaps].sort();
}

// —— 逗号塌缩软提示（HIGH#4）——
// 逗号条目首段已知关键字清单（封闭集合，大小写不敏感）：复制自后端 processor/match
// ClassifyEntry 的 switch 分支（ip-from 为口径补入，其逗号形同样不走兜底塌缩）；
// 两边必须同步维护。冒号风格前缀行（geoip:/ip-cidr:/ip-range:/ip-from:）由前缀
// 分支整行处理、不进入逗号兜底（如 ip-from:a,ip-to:b 合法整行），先于首段判定排除。
const COMMA_KEYWORD_HEADS = new Set(['domain', 'domains', 'ip-cidr', 'ip-range', 'geoip', 'ip-from']);
const COLON_KEYWORD_PREFIXES = ['geoip:', 'ip-cidr:', 'ip-range:', 'ip-from:'];

// 逗号多域名行按兼容语义仅保留末段（尾段为动作词时剥一层取倒数第二段），其余段
// 静默忽略——保存合法但少存规则（HIGH#4）。返回首个触发行的软提示文案，'' = 无触发。
// 不阻断保存（合法的兼容语义），仅黄色提示；kept 保留原大小写（所见即所得）。
// 具名导出仅供同目录 RulesSettings.test.js 做塌缩语义回归测试使用。
export function detectCommaCollapseIssue(rawText, t) {
  for (const line of sanitizeLines(rawText)) {
    // mesh 尾段先剥（后端 splitMeshTail 先于 ClassifyEntry）：`x.com,mesh:dev-b`
    // 的「保留末段」是 mesh 目标而非被塌缩丢弃的域名，不剥会把每个 mesh 条目
    // 都误报成「其余段被静默忽略」。非法形态不提示（红色错误已优先展示）。
    const meshTail = splitMeshRuleTail(line);
    if (meshTail.state === 'invalid') continue;
    const effective = meshTail.state === 'parsed' ? meshTail.base : line;
    const lower = effective.toLowerCase();
    if (COLON_KEYWORD_PREFIXES.some((prefix) => lower.startsWith(prefix))) continue;
    if (!effective.includes(',')) continue;
    const parts = effective.split(',');
    if (COMMA_KEYWORD_HEADS.has(parts[0].trim().toLowerCase())) continue;
    const tail = parts[parts.length - 1].trim().toLowerCase();
    const kept = (ACTION_TAIL_WORDS.has(tail) ? parts[parts.length - 2] : parts[parts.length - 1]).trim();
    if (!kept) continue; // 空保留段 = 条目本身非法，红色错误已在优先位置提示
    return t('rules_commaCollapseHint', { kept });
  }
  return '';
}

// 返回第一个非法条目（index 从 0 起），全部合法返回 null。
function firstEntryIssue(entries, t) {
  for (let i = 0; i < entries.length; i++) {
    const message = validateRuleEntry(entries[i], t);
    if (message) {
      return { index: i, entry: entries[i], message };
    }
  }
  return null;
}

// 错误文案组装（LOW#18）：firstEntryIssue 的 index 透传为「第 N 行」前缀（N 从 1 起），
// 套入 rules_entryErrorItem 包装；proxy_rules 与 include 两个输入面的报错共用本组装。
// 具名导出仅供同目录 RulesSettings.test.js 做前缀回归测试使用。
export function formatEntryIssue(issue, t) {
  if (!issue) return '';
  return t('rules_entryErrorItem', {
    entry: issue.entry,
    message: t('rules_linePrefix', { line: issue.index + 1 }) + issue.message
  });
}

// include 收起态副文案（LOW#16）：0 条 → Empty；1 条 → 条目本身；≥2 条 →
// 「{first} 等另外 {count} 项」预览（count = 其余条数）。entries 为 sanitizeList
// 产物（已做 URL 域名提取归一）。具名导出仅供同目录测试做副文案回归使用。
export function includeSummaryText(entries, t) {
  const list = entries || [];
  if (!list.length) return t('rules_includeDomainsEmpty');
  if (list.length === 1) return list[0];
  return t('rules_includeDomainsPreview', { first: list[0], count: list.length - 1 });
}

// 拆分前扫描：include 面任何 geoip 逗号形态都会被 sanitizeList 的逗号 tokenizer 拆坏
// （无例外——Clash 形态 GEOIP,CN,proxy 属 proxy_rules 专属，include 面同样非法）。
// 只挂 include 输入；proxy_rules 文本域按行拆分，GEOIP,CN,proxy 在那里合法，不挂。
// 文案 rules_geoipCommaMulti 的「会被拆坏」表述仅对该输入框成立（与 proxy 面的
// rules_geoipCommaMultiProxy 按输入面分派）。
// 具名导出仅供同目录 RulesSettings.test.js 做文案分派回归测试使用。
export function detectGeoipCommaIssue(rawText, t) {
  const lines = String(rawText || '').split('\n');
  for (const line of lines) {
    if (/geoip[:,][^\n]*,[^\n]*/i.test(line)) {
      return t('rules_geoipCommaMulti');
    }
  }
  return '';
}

function normalizeAiRules(aiRules = {}) {
  if (!aiRules || typeof aiRules !== 'object' || Array.isArray(aiRules)) {
    return {};
  }

  return Object.fromEntries(
    Object.entries(aiRules).map(([provider, value]) => [provider, {
      enble: Boolean(value?.enble ?? value?.enable),
      include: normalizeStringList(value?.include ?? value?.exclude),
      editable: typeof value?.editable === 'boolean' ? value.editable : undefined
    }])
  );
}

function normalizeProxyRules(raw) {
  return normalizeStringList(raw);
}

// jump_servers 线格式（SSHJumpServerRaw）：server 是单串（host:port 或
// ssh://user:pass@host:port），无独立 port 字段——UI 与载荷都按单串原样透传。
// 归一化只白名单五个线格式字段；行内 UI 状态（_authModeHint 认证二选一面提示，
// 仅在两侧全空的空行上有意义）不在归一化层种默认值——回显时面板由有值侧推导。
function normalizeJumpServers(raw) {
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.map((entry) => ({
    server: typeof entry?.server === 'string' ? entry.server : '',
    username: typeof entry?.username === 'string' ? entry.username : '',
    password: typeof entry?.password === 'string' ? entry.password : '',
    private_key: typeof entry?.private_key === 'string' ? entry.private_key : '',
    key_passphrase: typeof entry?.key_passphrase === 'string' ? entry.key_passphrase : ''
  }));
}

// 跳板行认证二选一的面（password/key）：用户显式选择（_authModeHint）优先——否则
// 密码已有值的行永远切不进私钥面；无提示时按有值侧推导（回显既有配置，初始挂载
// 的行对象没有 hint）；两侧全空且无提示默认密码面（新行同）。只读推导，行对象上
// 不落 _authMode 键——提示态单独放 _authModeHint，避免进线格式载荷。
// 模板经 methods 里的同名转发方法调用（模板只能访问组件属性）。
function resolveJumpAuthMode(jump) {
  if (jump?._authModeHint === 'key') return 'key';
  if (jump?._authModeHint === 'password') return 'password';
  if (String(jump?.private_key ?? '').trim() || String(jump?.key_passphrase ?? '').trim()) return 'key';
  return 'password';
}

// 主连接认证二选一初始面：私钥/口令有值 → 私钥面（存量并存配置落在有值侧，不静默
// 丢凭据），否则密码面。仅组件初始化用一次；config watcher 刷新表单时不重置用户已选面。
function initialSshAuthMode(config = {}) {
  const privateKey = String(config?.proxy?.private_key ?? '').trim();
  const passphrase = String(config?.proxy?.key_passphrase ?? '').trim();
  return privateKey || passphrase ? 'key' : 'password';
}

function normalizeConfig(config = {}) {
  const defaults = defaultConfig();
  const rawPoolSize = config?.proxy?.pool_size;
  return {
    proxy: {
      enable: valueOrDefaultBoolean(config?.proxy?.enable, true),
      type: ['http', 'ssh'].includes(config?.proxy?.type) ? config.proxy.type : defaults.proxy.type,
      server: typeof config?.proxy?.server === 'string' ? config.proxy.server : defaults.proxy.server,
      username: typeof config?.proxy?.username === 'string' ? config.proxy.username : defaults.proxy.username,
      password: typeof config?.proxy?.password === 'string' ? config.proxy.password : defaults.proxy.password,
      private_key: typeof config?.proxy?.private_key === 'string' ? config.proxy.private_key : '',
      key_passphrase: typeof config?.proxy?.key_passphrase === 'string' ? config.proxy.key_passphrase : '',
      // 空串 = 未设置（输入框空 + placeholder「默认 3」）；提交时转 0，默认与 1..16
      // 钳位由后端 ssh.New 承担（解析层 0 即 0）
      pool_size: typeof rawPoolSize === 'number' && Number.isFinite(rawPoolSize) && rawPoolSize > 0 ? rawPoolSize : '',
      jump_servers: normalizeJumpServers(config?.proxy?.jump_servers)
    },
    ai_rules: normalizeAiRules(config?.ai_rules),
    proxy_rules: normalizeProxyRules(config?.proxy_rules)
  };
}

function providerKeys(aiRules = {}) {
  return Object.keys(aiRules);
}

// 归一化 AI provider 键：与后端 NormalizeAIProviderKey 对齐（trim + 小写）。
// 键大小写不同但同一 provider 时，配置键与预置键必须折叠为一张卡——
// 大小写敏感的精确匹配会渲染出「Anthropic」「anthropic」两张卡。
function normalizeProviderKey(key) {
  return String(key ?? '').trim().toLowerCase();
}

// 具名导出仅供同目录 RulesSettings.test.js 做大小写折叠回归测试使用。
// 已配置键在前（大小写变体去重），预置中真正缺失的键按序补在后。
export function mergeProviderOrder(configuredKeys = [], presetProviders = []) {
  const seen = new Set();
  const ordered = [];
  for (const key of configuredKeys) {
    const normalizedKey = normalizeProviderKey(key);
    if (!normalizedKey || seen.has(normalizedKey)) continue;
    seen.add(normalizedKey);
    ordered.push(key);
  }
  for (const p of presetProviders) {
    const key = String(p?.key ?? '').trim();
    if (!key || seen.has(normalizeProviderKey(key))) continue;
    seen.add(normalizeProviderKey(key));
    ordered.push(key);
  }
  return ordered;
}

// 具名导出仅供同目录 RulesSettings.test.js 做大小写折叠回归测试使用。
// 大小写不敏感补种缺失的预置 provider：配置里已存在同 provider 的任意大小写
// 形态时绝不按预置键名再种一份（否则保存时会写回配置文件形成重复键）。
export function seedMissingProviders(aiRules = {}, presetProviders = []) {
  const existing = new Set(Object.keys(aiRules).map(normalizeProviderKey));
  for (const p of presetProviders) {
    const key = String(p?.key ?? '').trim();
    if (!key) continue;
    if (existing.has(normalizeProviderKey(key))) {
      const entry = aiRules[key];
      if (entry && typeof entry.editable !== 'boolean' && typeof p.editable === 'boolean') {
        entry.editable = p.editable;
      }
      continue;
    }
    aiRules[key] = { enble: false, include: [], editable: Boolean(p.editable) };
    existing.add(normalizeProviderKey(key));
  }
  return aiRules;
}

// 具名导出仅供同目录 RulesSettings.test.js 做大小写折叠回归测试使用。
export function providerLabelFor(key, presetProviders = []) {
  const normalizedKey = normalizeProviderKey(key);
  const preset = presetProviders.find((p) => normalizeProviderKey(p?.key) === normalizedKey);
  return preset ? preset.label : key;
}

const HOST_PORT_RE = /^(.+):(\d{1,5})$/;
const DOMAIN_RE = /^(?=.{1,253}$)([a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/;

function isValidServer(value, t) {
  if (!value) return '';
  const parsed = parseProxyServer(value);
  if (!parsed) return t('rules_serverError');
  const port = Number(parsed.port);
  if (port < 1 || port > 65535) return t('rules_serverPortRange');
  if (isValidHost(parsed.host)) return '';
  return t('rules_serverHostInvalid');
}

function parseProxyServer(value) {
  const raw = String(value || '').trim();
  if (!raw) return null;

  if (/^[a-zA-Z][a-zA-Z\d+\-.]*:\/\//.test(raw) || raw.startsWith('//') || raw.includes('@')) {
    try {
      const urlValue = raw.includes('@') && !raw.includes('://') && !raw.startsWith('//')
        ? `proxy://${raw}`
        : raw;
      const parsed = new URL(urlValue);
      if (!parsed.hostname || !parsed.port) return null;
      return { host: parsed.hostname, port: parsed.port };
    } catch {
      return null;
    }
  }

  if (raw.startsWith('[')) {
    const end = raw.indexOf(']');
    if (end <= 1 || raw[end + 1] !== ':') return null;
    return { host: raw.slice(1, end), port: raw.slice(end + 2) };
  }

  const match = raw.match(HOST_PORT_RE);
  if (!match) return null;
  return { host: match[1], port: match[2] };
}

function isValidHost(host) {
  if (!host) return false;
  if (/^[\da-fA-F:]+$/.test(host) && host.includes(':')) return true;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) {
    return host.split('.').every((o) => Number(o) >= 0 && Number(o) <= 255);
  }
  return DOMAIN_RE.test(host);
}

function valueOrDefaultBoolean(value, defaultValue) {
  return typeof value === 'boolean' ? value : defaultValue;
}

const SUCCESS_RING_RADIUS = 15.5;
const SUCCESS_RING_CIRCUMFERENCE = 2 * Math.PI * SUCCESS_RING_RADIUS;

export default {
  name: 'RulesSettings',
  components: {
    SshChainCanvas
  },
  setup() {
    const { t } = useI18n();
    // HIGH#5：打开语法文档（usage-guide 12.1 规则格式）。openTutorialDocs 是模块级
    // 共享状态（App.vue 渲染 TutorialDocsModal），无需经父组件 emit 链转发。
    const openRulesSyntaxDocs = () => openTutorialDocs('usage-guide');
    return { t, openRulesSyntaxDocs };
  },
  props: {
    config: {
      type: Object,
      default: () => defaultConfig()
    },
    presetProviders: {
      type: Array,
      default: () => []
    },
    loading: {
      type: Boolean,
      default: false
    },
    saving: {
      type: Boolean,
      default: false
    },
    error: {
      type: String,
      default: ''
    },
    successMessage: {
      type: String,
      default: ''
    },
    version: {
      type: String,
      default: ''
    }
  },
  emits: ['save'],
  data() {
    return {
      providerEditor: {
        provider: '',
        draft: ''
      },
      form: normalizeConfig(this.config),
      // SSH 主连接认证二选一的面（password/key）。初始值按配置推导：私钥/口令有值
      // → 私钥面，否则密码面（存量「密码+私钥并存」的配置落在有值侧，不静默丢凭据）。
      sshAuthMode: initialSshAuthMode(this.config),
      // 服务器指纹（TOFU）区块状态：失败静默降级，不阻塞主表单
      knownHosts: {
        loading: false,
        failed: false,
        items: []
      },
      // SSH 测试连接 + 链路拓扑画布状态。测试是一次性动作（loading/result）；
      // 画布吃 summary.ssh 整体快照（保存并生效后才非空），仅 ssh 类型挂载时轮询。
      sshTest: {
        loading: false,
        result: null
      },
      sshStatusRaw: null,
      _proxyRulesText: '',
      _providerIncludeTexts: {},
      _showSuccessDialog: false,
      _successDialogTimer: null,
      _countdownProgress: 1,
      _providerEditorClickHandler: null
    };
  },
  created() {
    this.ensureProviders();
    this.syncTextFromForm();
  },
  mounted() {
    this._providerEditorClickHandler = (event) => {
      if (!this.providerEditor.provider) {
        return;
      }

      const target = event.target;
      if (!(target instanceof Element)) {
        return;
      }

      const selector = `[data-provider-editor-root="${this.providerEditor.provider}"]`;
      if (target.closest(selector)) {
        return;
      }

      this.cancelProviderEditor();
    };
    document.addEventListener('click', this._providerEditorClickHandler, true);
    // SSH 类型挂载即拉取指纹列表；socks5/http → ssh 的切换由 type watcher 补拉
    if (this.form.proxy.type === 'ssh') {
      this.loadKnownHosts();
      this.startSshChainPolling();
    }
  },
  beforeUnmount() {
    this.clearSuccessDialogTimer();
    this.stopSshChainPolling();
    if (this._providerEditorClickHandler) {
      document.removeEventListener('click', this._providerEditorClickHandler, true);
      this._providerEditorClickHandler = null;
    }
  },
  computed: {
    providerOrder() {
      return mergeProviderOrder(providerKeys(this.form.ai_rules), this.presetProviders);
    },
    // —— 连接测试 + 链路拓扑画布（客户端连接状态反馈）——
    // 泳道/节点推导已收敛入 SshChainCanvas（spec §5.2 单份推导），此处只留轮询喂快照。
    // 测试结果条文案：成功=✓ 延迟 + 指纹截断；失败=阶段文案 + 原始错误。
    sshTestResultText() {
      const res = this.sshTest.result;
      if (!res) return '';
      if (res.ok) {
        const latency = Number(res.latency_millis || 0);
        const fpr = String(res.host_key_fingerprint || '');
        const fprShort = fpr.length > 24 ? `${fpr.slice(0, 24)}…` : fpr;
        return this.t('rules_sshTestSuccess', { latency, fingerprint: fprShort });
      }
      const stageKey = {
        auth: 'rules_sshTestStageAuth',
        dial: 'rules_sshTestStageDial',
        handshake: 'rules_sshTestStageHandshake',
        channel: 'rules_sshTestStageChannel',
        roundtrip: 'rules_sshTestStageRoundTrip'
      }[String(res.stage || '')];
      const stageText = stageKey ? this.t(stageKey) : '';
      const detail = String(res.error || '');
      return this.t('rules_sshTestFailed', { stage: stageText, error: detail });
    },
    isSshProxy() {
      return this.form.proxy.type === 'ssh';
    },
    serverError() {
      if (!this.form.proxy.enable) return '';
      return isValidServer(this.form.proxy.server, this.t);
    },
    proxyRulesError() {
      return formatEntryIssue(firstEntryIssue(sanitizeLines(this._proxyRulesText), this.t), this.t);
    },
    // —— 软警告（amber，绝不阻断保存；红线：与红色 error 分流——保存按钮 disabled
    // 与 handleSubmit 均只看错误、不看警告）——
    // HIGH#3：启用 provider 的 include 与 proxy_rules 条件重叠（AI 规则恒优先，
    // 后端允许共存仅是代理规则对该条目不生效——警告让用户知情即可）。
    overlapWarning() {
      const includeEntries = [];
      for (const [key, text] of Object.entries(this._providerIncludeTexts)) {
        if (!this.form.ai_rules[key]?.enble) continue;
        includeEntries.push(...sanitizeList(text));
      }
      const overlaps = findRuleOverlaps(includeEntries, sanitizeLines(this._proxyRulesText));
      return overlaps.length ? this.t('rules_overlapWarning', { entries: overlaps.join(', ') }) : '';
    },
    // HIGH#4：proxy_rules 逗号多域名行的兼容塌缩提示（仅保留末段，其余段被忽略）。
    proxyRulesCollapseHint() {
      return detectCommaCollapseIssue(this._proxyRulesText, this.t);
    },
    // —— SSH 校验（与后端 SSHProxyConfig.Validate 对齐的提示层，后端保存时仍是权威）——
    // username 必填 + 认证二选一（当前 tab 侧有值即可）；跳板行同规则（server/username/
    // 认证至少其一，序号从 1 起）。仅 enable 且 type==='ssh' 时生效；整行留空的跳板行
    // 先经 activeJumpServers 过滤（UI 空行不参与校验也不进载荷）。
    sshValidationError() {
      if (!this.form.proxy.enable || this.form.proxy.type !== 'ssh') return '';
      if (!String(this.form.proxy.username || '').trim()) return this.t('rules_sshValidationErrorUsername');
      if (!this.sshAuthHasCredential) return this.t('rules_sshValidationErrorAuth');
      const jumps = this.activeJumpServers();
      for (let i = 0; i < jumps.length; i++) {
        const jump = jumps[i];
        if (!jump.server) return this.t('rules_sshValidationErrorJumpServer', { index: i + 1 });
        if (!jump.username) return this.t('rules_sshValidationErrorJumpUsername', { index: i + 1 });
        if (!jump.password && !jump.private_key) return this.t('rules_sshValidationErrorJumpAuth', { index: i + 1 });
      }
      return '';
    },
    // 认证二选一在「当前 tab 侧」必须有凭据：密码面看 password；私钥面看 private_key
    // 或 key_passphrase（口令不能单独作为认证凭据，但此处放行口令——后端 Validate
    // 只要求 password/private_key 至少其一，口令单独存在时后端会拒绝并给权威报错，
    // 前端不重复下钻判定，避免双权威）。另一种含义：另一 tab 侧残留的凭据不救场。
    sshAuthHasCredential() {
      if (this.sshAuthMode === 'key') {
        return Boolean(String(this.form.proxy.private_key || '').trim());
      }
      return Boolean(String(this.form.proxy.password || '').trim());
    },
    providerEditorError() {
      if (!this.providerEditor.provider) return '';
      // include 面：先做 geoip 逗号形态的拆分前扫描，再走逐条校验
      const commaIssue = detectGeoipCommaIssue(this.providerEditor.draft, this.t);
      if (commaIssue) return commaIssue;
      return formatEntryIssue(firstEntryIssue(sanitizeList(this.providerEditor.draft), this.t), this.t);
    },
    // 已存 include 文本的实时校验（草稿在弹窗内另有提示）；条目非法时阻止整表提交。
    submitError() {
      for (const [key, text] of Object.entries(this._providerIncludeTexts)) {
        const commaIssue = detectGeoipCommaIssue(text, this.t);
        if (commaIssue) {
          return `${this.providerLabel(key, this.presetProviders)}: ${commaIssue}`;
        }
        const issue = firstEntryIssue(sanitizeList(text), this.t);
        if (issue) {
          return `${this.providerLabel(key, this.presetProviders)}: ${formatEntryIssue(issue, this.t)}`;
        }
      }
      return '';
    },
    serverFieldClass() {
      if (!this.form.proxy.server) return 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900';
      return this.serverError
        ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
        : 'border-emerald-400 bg-emerald-50/50 focus:border-emerald-500 focus:ring-emerald-500/20 dark:border-emerald-500/50 dark:bg-emerald-900/10';
    },
    successRingCircumference() {
      return SUCCESS_RING_CIRCUMFERENCE;
    },
    successRingDashoffset() {
      const clampedProgress = Math.min(Math.max(this._countdownProgress, 0), 1);
      return SUCCESS_RING_CIRCUMFERENCE * (1 - clampedProgress);
    }
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = normalizeConfig(nextConfig);
        this.ensureProviders();
        this.syncTextFromForm();
      }
    },
    // 切到 ssh 时拉取指纹列表（每次切换刷新；socks5/http 不请求）
    'form.proxy.type'(nextType) {
      if (nextType === 'ssh') {
        this.loadKnownHosts();
        this.startSshChainPolling();
        return;
      }
      // 非 ssh 类型：链路画布无数据源，停轮询并清结果
      this.stopSshChainPolling();
      this.sshStatusRaw = null;
      this.sshTest.result = null;
    },
    presetProviders: {
      deep: true,
      handler() {
        this.ensureProviders();
        this.syncTextFromForm();
      }
    },
    successMessage(nextValue) {
      if (typeof nextValue === 'string' && nextValue.trim()) {
        this.showSuccessDialog();
        return;
      }
      this.hideSuccessDialog();
    }
  },
  methods: {
    clearSuccessDialogTimer() {
      if (this._successDialogTimer !== null) {
        window.clearInterval(this._successDialogTimer);
        this._successDialogTimer = null;
      }
    },
    showSuccessDialog() {
      this._showSuccessDialog = true;
      this.clearSuccessDialogTimer();
      const totalDuration = 1800;
      const interval = 50;
      const totalSteps = totalDuration / interval;
      let currentStep = 0;
      this._countdownProgress = 1;
      this._successDialogTimer = window.setInterval(() => {
        currentStep++;
        this._countdownProgress = 1 - (currentStep / totalSteps);
        if (currentStep >= totalSteps) {
          window.clearInterval(this._successDialogTimer);
          this._successDialogTimer = null;
          this._showSuccessDialog = false;
        }
      }, interval);
    },
    hideSuccessDialog() {
      this._showSuccessDialog = false;
      this.clearSuccessDialogTimer();
    },
    ensureProviders() {
      this.form.ai_rules = seedMissingProviders(this.form.ai_rules, this.presetProviders);
    },
    providerLabel(key, presetProviders) {
      return providerLabelFor(key, presetProviders);
    },
    // LOW#16：include 收起态副文案（空/单条目/first+N more）
    providerIncludeSummary(provider) {
      return includeSummaryText(normalizeStringList(this.form.ai_rules[provider]?.include), this.t);
    },
    // LOW#16：include 编辑弹窗标题的草稿实时条目数（与保存同口径的 sanitizeList 计数）
    providerDraftCount() {
      return sanitizeList(this.providerEditor.draft).length;
    },
    providerEditable(provider) {
      const rule = this.form.ai_rules[provider];
      if (typeof rule?.editable === 'boolean') {
        return rule.editable;
      }
      const preset = this.presetProviders.find((entry) => entry.key === provider);
      return Boolean(preset?.editable);
    },
    syncTextFromForm() {
      this._proxyRulesText = (this.form.proxy_rules || []).join('\n');
      for (const key of Object.keys(this.form.ai_rules)) {
        this._providerIncludeTexts[key] = (this.form.ai_rules[key].include || []).join('\n');
      }
    },
    isProviderEditorOpen(provider) {
      return this.providerEditor.provider === provider;
    },
    toggleProviderEditor(provider) {
      if (!this.providerEditable(provider)) {
        return;
      }
      if (this.isProviderEditorOpen(provider)) {
        this.cancelProviderEditor();
        return;
      }

      this.providerEditor.provider = provider;
      this.providerEditor.draft = this._providerIncludeTexts[provider] || '';
      this.$nextTick(() => {
        const refValue = this.$refs[`providerEditor-${provider}`];
        const textarea = Array.isArray(refValue) ? refValue[0] : refValue;
        if (textarea && typeof textarea.focus === 'function') {
          textarea.focus();
        }
      });
    },
    cancelProviderEditor() {
      this.providerEditor.provider = '';
      this.providerEditor.draft = '';
    },
    saveProviderEditor() {
      const provider = this.providerEditor.provider;
      if (!provider) {
        return;
      }
      if (!this.providerEditable(provider)) {
        this.cancelProviderEditor();
        return;
      }
      if (this.providerEditorError) {
        // 条目非法时保持弹窗打开，错误文案已展示在输入框下方
        return;
      }

      const include = sanitizeList(this.providerEditor.draft);
      this._providerIncludeTexts[provider] = include.join('\n');
      if (this.form.ai_rules[provider]) {
        this.form.ai_rules[provider].include = include;
      }
      this.cancelProviderEditor();
    },
    // —— SSH 跳板链编辑 ——
    // 参与校验与载荷的跳板行：trim 后整行全空的行静默丢弃（新增即留空的 UI 行），
    // 半填行保留（交由 sshValidationError 给出序号文案）。产物只含线格式五字段
    //（_authModeHint UI 状态在此剥除），且认证按当前面收敛：密码面清 private_key/
    // key_passphrase，私钥面清 password——线上二选一，另一侧残值不进载荷。
    activeJumpServers() {
      const rows = Array.isArray(this.form.proxy.jump_servers) ? this.form.proxy.jump_servers : [];
      return rows
        .map((row) => {
          const mode = resolveJumpAuthMode(row);
          return {
            server: String(row?.server ?? '').trim(),
            username: String(row?.username ?? '').trim(),
            password: mode === 'password' ? String(row?.password ?? '') : '',
            private_key: mode === 'key' ? String(row?.private_key ?? '') : '',
            key_passphrase: mode === 'key' ? String(row?.key_passphrase ?? '') : ''
          };
        })
        .filter((row) => row.server || row.username || row.password || row.private_key || row.key_passphrase);
    },
    addJumpServer() {
      this.form.proxy.jump_servers.push({
        server: '',
        username: '',
        password: '',
        private_key: '',
        key_passphrase: '',
        _authModeHint: 'password'
      });
    },
    removeJumpServer(index) {
      this.form.proxy.jump_servers.splice(index, 1);
    },
    // 模板转发：跳板行认证二选一当前面（模块级 resolveJumpAuthMode 的只读推导）
    jumpAuthMode(jump) {
      return resolveJumpAuthMode(jump);
    },
    // 跳板行认证二选一切换：仅落 _authModeHint（提示态）。两侧全空的行切换即生效；
    // 已有凭据的行切到另一面时值保留在表单状态里，提交时按当前面收敛（另一侧置零）。
    setJumpAuthMode(jump, mode) {
      if (jump && typeof jump === 'object') {
        jump._authModeHint = mode;
      }
    },
    // 主连接认证 tab 切换：仅改显示面；两侧已填的值都保留在表单状态里不丢，
    // 提交时按当前面收敛（另一侧置零）。
    setSshAuthMode(mode) {
      this.sshAuthMode = mode === 'key' ? 'key' : 'password';
    },
    // —— 服务器指纹（TOFU）区块 ——
    // 请求失败静默降级（failed 文案），不抛错、不阻塞主表单。
    async loadKnownHosts() {
      this.knownHosts.loading = true;
      this.knownHosts.failed = false;
      try {
        const res = await fetch('/api/proxy/ssh/known-hosts', { credentials: 'same-origin' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const json = await res.json();
        const items = json?.data?.known_hosts;
        this.knownHosts.items = Array.isArray(items) ? items : [];
      } catch {
        this.knownHosts.items = [];
        this.knownHosts.failed = true;
      } finally {
        this.knownHosts.loading = false;
      }
    },
    async removeKnownHost(entry) {
      try {
        const query = `?host=${encodeURIComponent(entry.host)}&port=${encodeURIComponent(entry.port)}`;
        await fetch(`/api/proxy/ssh/known-hosts${query}`, { method: 'DELETE', credentials: 'same-origin' });
      } catch {
        // 删除失败同样静默：下方刷新会把最新列表（或失败文案）带回来
      }
      await this.loadKnownHosts();
    },
    async clearKnownHosts() {
      if (typeof window !== 'undefined' && typeof window.confirm === 'function') {
        if (!window.confirm(this.t('rules_knownHostsClearConfirm'))) {
          return;
        }
      }
      try {
        await fetch('/api/proxy/ssh/known-hosts', { method: 'DELETE', credentials: 'same-origin' });
      } catch {
        // 同上：刷新兜底
      }
      await this.loadKnownHosts();
    },
    truncateFingerprint(fingerprint) {
      const value = String(fingerprint || '');
      return value.length > 20 ? `${value.slice(0, 20)}…` : value;
    },
    // —— 连接测试 + 链路拓扑画布 ——
    // 测试连接：用当前表单凭据做一次现场探测（后端不落盘不触注册表）。
    // 载荷与保存路径同构（server 单串线格式 + 当前面凭据收敛 + 跳板线格式），
    // 测的就是「将要保存的这份配置」。失败也停留在结果条（role=status），
    // 不抛错不阻塞表单。
    async runSshTest() {
      if (!this.form.proxy.enable || this.sshTest.loading || this.sshValidationError) {
        return;
      }
      this.sshTest.loading = true;
      this.sshTest.result = null;
      try {
        const envelope = await testSshConnection(this.buildSshTestPayload());
        this.sshTest.result = envelope?.data || { ok: false, stage: '', error: 'empty response' };
      } catch (error) {
        // 网络层失败（本机 API 不可达等）：同样落结果条
        this.sshTest.result = {
          ok: false,
          stage: '',
          error: error instanceof Error ? error.message : String(error)
        };
      } finally {
        this.sshTest.loading = false;
      }
    },
    // 测试载荷：与 handleSubmit 的 SSH 载荷口径一致（server 单串、当前面
    // 凭据收敛、跳板线格式五字段、pool_size 不参与探测）。
    buildSshTestPayload() {
      const mode = this.sshAuthMode;
      const payload = {
        server_host: String(this.form.proxy.server || '').trim(),
        username: String(this.form.proxy.username || '').trim(),
        password: mode === 'password' ? String(this.form.proxy.password ?? '') : '',
        private_key: mode === 'key' ? String(this.form.proxy.private_key ?? '') : '',
        key_passphrase: mode === 'key' ? String(this.form.proxy.key_passphrase ?? '') : '',
        jump_servers: this.activeJumpServers()
      };
      // server 单串线格式可能带端口（host:port）；无端口时后端要求 server_port，
      // 这里对常见缺省 22 做前端兜底拆分（与 parseCustomerProxyServer 的宽容度对齐）。
      const raw = payload.server_host;
      if (raw && !raw.includes(':')) {
        payload.server_host = raw;
        payload.server_port = 22;
      } else if (raw) {
        const idx = raw.lastIndexOf(':');
        const host = raw.slice(0, idx);
        const port = Number(raw.slice(idx + 1));
        payload.server_host = host;
        payload.server_port = Number.isFinite(port) && port > 0 ? port : 22;
      }
      // 跳板行 server 同为单串线格式（host:port 或 ssh://user:pass@host:port）：
      // 简单形态直接拆 host:port；ssh:// URL 原样交后端（后端 parseCustomerProxyServer
      // 同款解析——测试载荷复用 server_host/server_port 字段,这里透传单串给
      // 后端解析的口径:跳板行的 URL 形态在前端不拆,交 EffectiveSSHProxy 同源解析）。
      payload.jump_servers = (payload.jump_servers || []).map((jump) => {
        const server = String(jump.server || '').trim();
        if (server && !server.includes('://') && server.includes(':')) {
          const idx = server.lastIndexOf(':');
          const port = Number(server.slice(idx + 1));
          return {
            server_host: server.slice(0, idx),
            server_port: Number.isFinite(port) && port > 0 ? port : 22,
            username: jump.username,
            password: jump.password,
            private_key: jump.private_key,
            key_passphrase: jump.key_passphrase
          };
        }
        if (server && server.includes('://')) {
          // ssh://user:pass@host:port —— 提取凭据+host:port（与后端解析同构）
          try {
            const url = new URL(server);
            const port = url.port ? Number(url.port) : 22;
            return {
              server_host: url.hostname,
              server_port: port,
              username: url.username || jump.username,
              password: url.password || jump.password,
              private_key: jump.private_key,
              key_passphrase: jump.key_passphrase
            };
          } catch {
            return { server_host: server, server_port: 22, username: jump.username, password: jump.password, private_key: jump.private_key, key_passphrase: jump.key_passphrase };
          }
        }
        return { server_host: server, server_port: 22, username: jump.username, password: jump.password, private_key: jump.private_key, key_passphrase: jump.key_passphrase };
      });
      return payload;
    },
    // 链路画布轮询：复用状态 summary 的 ssh 整体快照（5s 与状态页同频）。
    // 失败静默（下个周期自愈），与指纹区块同款降级纪律。
    async loadSshChains() {
      try {
        const envelope = await getStatusSummary();
        this.sshStatusRaw = envelope?.data?.ssh ?? null;
      } catch {
        // 静默：画布保持上一次快照，下个轮询周期自愈
      }
    },
    startSshChainPolling() {
      this.stopSshChainPolling();
      this.loadSshChains();
      this._sshChainTimer = window.setInterval(() => {
        this.loadSshChains();
      }, 5000);
    },
    stopSshChainPolling() {
      if (this._sshChainTimer) {
        window.clearInterval(this._sshChainTimer);
        this._sshChainTimer = null;
      }
    },
    async handleSubmit() {
      const normalized = normalizeConfig(this.form);
      normalized.proxy_rules = sanitizeLines(this._proxyRulesText);
      for (const [key, text] of Object.entries(this._providerIncludeTexts)) {
        if (normalized.ai_rules[key]) {
          normalized.ai_rules[key].include = sanitizeList(text);
        }
      }

      // 保存前逐条校验：任一非法则不提交（错误文案已在对应输入处实时展示）
      if (firstEntryIssue(normalized.proxy_rules, this.t)) return;
      if (this.submitError) return;
      if (this.sshValidationError) return;

      // SSH 载荷口径：跳板行取过滤后的线格式五字段（认证按当前面收敛）；pool_size
      // 空串归 0（后端默认 3、1..16 钳位由 ssh.New 承担）。主连接认证二选一：只带
      // 当前 tab 侧凭据，另一侧显式置零——父层 SettingsPage 对空串凭据字段显式发
      // 空串（buildCustomerConfigPatch 兜底段），后端 deep-merge 才能清掉旧值。
      // 非 ssh 类型全部置零值清除后端旧值（父层 diff-patch 对空串字段默认不发送，
      // 靠 SettingsPage 显式清零兜底）。
      if (normalized.proxy.type === 'ssh') {
        normalized.proxy.jump_servers = this.activeJumpServers();
        normalized.proxy.pool_size = Number(normalized.proxy.pool_size) || 0;
        if (this.sshAuthMode === 'key') {
          normalized.proxy.password = '';
        } else {
          normalized.proxy.private_key = '';
          normalized.proxy.key_passphrase = '';
        }
      } else {
        normalized.proxy.private_key = '';
        normalized.proxy.key_passphrase = '';
        normalized.proxy.pool_size = 0;
        normalized.proxy.jump_servers = [];
      }

      this.$emit('save', normalized);
    }
  }
}
</script>
