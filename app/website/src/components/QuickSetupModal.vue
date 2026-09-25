<template>
  <div
    v-if="open"
    class="fixed inset-0 z-[130] flex items-center justify-center p-4"
    role="dialog"
    aria-modal="true"
    aria-label="Quick Setup"
  >
    <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="emit('close')"></div>

    <div
      class="relative z-10 flex h-[720px] w-full max-w-6xl overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-800 dark:bg-slate-900"
    >
      <!-- Left sidebar：installed 过滤后的 agent 列表 -->
      <aside class="flex w-72 shrink-0 flex-col border-r border-slate-100 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-800/30">
        <div class="border-b border-slate-100 px-6 py-5 dark:border-slate-800">
          <p class="text-[11px] font-bold uppercase tracking-[0.28em] text-slate-400">{{ t('qs_title') }}</p>
          <h3 class="mt-2 text-lg font-semibold text-slate-900 dark:text-white">{{ t('qs_presetTemplates') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('qs_description') }}
          </p>
        </div>

        <div class="flex-1 space-y-2 overflow-y-auto px-4 py-4 custom-scrollbar">
          <button
            v-for="software in installedSoftwares"
            :key="software.code"
            type="button"
            :class="[
              'relative w-full rounded-xl border px-4 py-3 text-left transition-all',
              software.code === selectedSoftware
                ? 'border-primary/30 bg-white shadow-sm dark:border-primary/30 dark:bg-slate-900'
                : 'border-transparent bg-transparent hover:border-slate-200 hover:bg-white dark:hover:border-slate-700 dark:hover:bg-slate-900/60',
            ]"
            @click="selectSoftware(software.code)"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <p class="text-sm font-semibold text-slate-900 dark:text-white">{{ software.name }}</p>
                <p
                  class="mt-1 truncate text-[11px] leading-5 text-slate-500 dark:text-slate-400"
                  :title="software.description"
                >
                  {{ software.description }}
                </p>
              </div>
              <span
                :class="[
                  'mt-0.5 inline-flex size-2.5 rounded-full',
                  software.code === selectedSoftware ? 'bg-primary' : 'bg-slate-300 dark:bg-slate-600',
                ]"
              ></span>
            </div>
          </button>

          <!-- 全部内置 agent 均未安装时的侧栏空状态 -->
          <div
            v-if="catalogStatus === 'success' && !installedSoftwares.length"
            class="rounded-xl border border-dashed border-slate-200 bg-white/60 px-4 py-6 text-center dark:border-slate-700 dark:bg-slate-900/40"
          >
            <span class="material-symbols-outlined text-2xl text-slate-300 dark:text-slate-600">search_off</span>
            <p class="mt-2 text-sm font-semibold text-slate-700 dark:text-slate-200">{{ t('qs_no_agents_detected') }}</p>
            <p class="mt-1 text-[11px] leading-5 text-slate-500 dark:text-slate-400">{{ t('qs_no_agents_desc') }}</p>
          </div>
        </div>
      </aside>

      <!-- Right panel -->
      <div class="flex min-w-0 flex-1 flex-col">
        <header class="flex h-16 shrink-0 items-center justify-between border-b border-slate-100 px-6 dark:border-slate-800">
          <div class="min-w-0">
            <p class="truncate text-base font-semibold text-slate-900 dark:text-white">
              {{ selectedSoftwareDef?.name || t('qs_title') }}
            </p>
            <p class="text-[11px] text-slate-500 dark:text-slate-400">
              {{ t('qs_filesPerVariant', { count: selectedSoftwareDef?.files?.length || 0 }) }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <button
              type="button"
              class="inline-flex min-h-9 items-center justify-center rounded-lg border border-slate-200 px-3 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
              :disabled="!activeCombo || Boolean(applyResult) || applying"
              @click="openConfigure"
            >
              {{ t('qs_combo_configure') }}
            </button>
            <button
              type="button"
              class="inline-flex min-h-9 items-center justify-center rounded-lg bg-primary px-4 text-xs font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="!applyReadiness.ready || applying || Boolean(applyResult) || configureOpen"
              @click="applyCombo"
            >
              {{ applying ? t('qs_applying') : t('qs_applyFiles') }}
            </button>
            <button
              type="button"
              aria-label="Close quick setup"
              class="inline-flex size-9 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              @click="emit('close')"
            >
              <span class="material-symbols-outlined">close</span>
            </button>
          </div>
        </header>

        <div class="flex-1 overflow-y-auto px-6 py-5 custom-scrollbar">
          <div v-if="loadingCatalog" class="rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-5 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
            {{ t('qs_loading') }}
          </div>

          <div
            v-else-if="catalogStatus === 'unauthenticated'"
            class="rounded-2xl border border-amber-200 bg-amber-50 px-5 py-6 dark:border-amber-900/40 dark:bg-amber-950/20"
          >
            <p class="text-sm font-semibold text-amber-800 dark:text-amber-200">{{ t('qs_signInTitle') }}</p>
            <p class="mt-2 text-sm leading-6 text-amber-700 dark:text-amber-300">
              {{ t('qs_signInDesc') }}
            </p>
          </div>

          <div
            v-else-if="catalogStatus === 'failed'"
            class="rounded-2xl border border-rose-200 bg-rose-50 px-5 py-6 dark:border-rose-900/40 dark:bg-rose-950/20"
          >
            <p class="text-sm font-semibold text-rose-800 dark:text-rose-200">{{ t('qs_failedTitle') }}</p>
            <p class="mt-2 text-sm leading-6 text-rose-700 dark:text-rose-300">{{ catalogMessage || t('qs_tryAgain') }}</p>
          </div>

          <!-- 全部内置 agent 均未安装：与侧栏空态一致，右侧不渲染任何配置控件 -->
          <div
            v-else-if="!selectedSoftwareDef"
            class="flex min-h-full flex-col items-center justify-center px-5 py-10 text-center"
          >
            <span class="material-symbols-outlined text-3xl text-slate-300 dark:text-slate-600">search_off</span>
            <p class="mt-3 text-sm font-semibold text-slate-700 dark:text-slate-200">{{ t('qs_no_agents_detected') }}</p>
            <p class="mt-1 max-w-sm text-[11px] leading-5 text-slate-500 dark:text-slate-400">{{ t('qs_no_agents_desc') }}</p>
          </div>

          <template v-else>
            <!-- 组合 tabs：is_default 星标 + 「+新建」 -->
            <div class="flex flex-wrap items-center gap-2">
              <div class="inline-flex flex-wrap items-center gap-1 rounded-xl border border-slate-200 bg-slate-50 p-1 dark:border-slate-700 dark:bg-slate-900/60">
                <button
                  v-for="combo in agentCombos"
                  :key="combo.id"
                  type="button"
                  :aria-pressed="activeCombo?.id === combo.id"
                  :class="[
                    'min-h-8 rounded-lg px-3 text-xs font-semibold transition',
                    activeCombo?.id === combo.id
                      ? 'bg-white text-primary shadow-sm dark:bg-slate-800 dark:text-primary'
                      : 'text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
                  ]"
                  @click="switchCombo(combo)"
                >
                  {{ combo.name }}<span v-if="combo.is_default" class="ml-1 text-primary" aria-hidden="true">★</span>
                </button>
                <button
                  type="button"
                  class="inline-flex min-h-8 items-center justify-center gap-0.5 rounded-lg border border-dashed border-slate-300 px-2.5 text-xs font-semibold text-slate-500 transition hover:border-primary/40 hover:text-primary dark:border-slate-700 dark:text-slate-400 dark:hover:border-primary/40 dark:hover:text-primary"
                  @click="openCreateDialog"
                >
                  <span class="material-symbols-outlined text-sm">add</span>
                  {{ t('qs_combo_new') }}
                </button>
              </div>
              <button
                v-if="activeCombo"
                type="button"
                :aria-label="t('qs_combo_menu')"
                :title="t('qs_combo_menu')"
                class="inline-flex size-8 items-center justify-center rounded-lg border border-slate-200 text-slate-500 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800"
                @click="comboMenuOpen = !comboMenuOpen"
              >
                <span class="material-symbols-outlined text-base">more_horiz</span>
              </button>
            </div>

            <!-- 当前组合操作行：重命名 / 设默认 / 删除 -->
            <div
              v-if="comboMenuOpen && activeCombo"
              class="mt-2 flex flex-wrap items-center gap-2 rounded-xl border border-slate-200 bg-white p-2 dark:border-slate-700 dark:bg-slate-900"
            >
              <button
                type="button"
                class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                @click="openRename"
              >
                {{ t('qs_combo_rename') }}
              </button>
              <button
                type="button"
                class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                :disabled="activeCombo.is_default"
                @click="makeDefault"
              >
                {{ t('qs_combo_set_default') }}
              </button>
              <button
                type="button"
                class="inline-flex min-h-8 items-center justify-center rounded-lg border border-rose-200 px-3 text-[11px] font-semibold text-rose-600 transition hover:bg-rose-50 dark:border-rose-900/50 dark:text-rose-400 dark:hover:bg-rose-950/30"
                @click="deleteConfirmOpen = true; comboMenuOpen = false"
              >
                {{ t('qs_combo_delete') }}
              </button>
            </div>

            <!-- 重命名（内联 input） -->
            <div
              v-if="renameOpen"
              class="mt-2 flex items-center gap-2 rounded-xl border border-primary/20 bg-white p-2 dark:border-primary/30 dark:bg-slate-900"
            >
              <input
                v-model="renameDraft"
                type="text"
                :aria-label="t('qs_combo_name')"
                :placeholder="t('qs_combo_name')"
                class="h-8 min-w-0 flex-1 rounded-lg border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
                @keyup.enter="confirmRename"
              />
              <button
                type="button"
                class="inline-flex min-h-8 items-center justify-center rounded-lg bg-primary px-3 text-[11px] font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
                :disabled="comboSaving || !renameDraft.trim()"
                @click="confirmRename"
              >
                {{ t('qs_combo_save') }}
              </button>
              <button
                type="button"
                class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
                @click="renameOpen = false"
              >
                {{ t('qs_cancel') }}
              </button>
            </div>

            <!-- apply 前置校验提示：渲染后仍有占位符或变量值为空 -->
            <p
              v-if="activeCombo && applyReadiness.missing.length && activeTab === 'edit' && !applyResult"
              class="mt-2 text-[11px] leading-5 text-amber-600 dark:text-amber-400"
            >
              {{ t('qs_apply_unresolved', { vars: applyReadiness.missing.join(', ') }) }}
            </p>

            <!-- 文件 tabs：该组合的 files.code（声明 label 优先） -->
            <div
              v-if="activeCombo && activeTab === 'edit' && !applyResult && !configureOpen"
              class="mt-3 flex flex-wrap gap-2"
            >
              <button
                v-for="file in activeCombo.files"
                :key="`file-tab-${file.code}`"
                type="button"
                :class="[
                  'inline-flex min-h-9 items-center justify-center rounded-full border px-3 text-[11px] font-semibold transition',
                  file.code === activeFileCode
                    ? 'border-primary/30 bg-primary/10 text-primary'
                    : 'border-slate-200 text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800',
                ]"
                @click="switchFileTab(file.code)"
              >
                {{ fileLabelFor(file.code) }}
              </button>
            </div>

            <!-- 页签：配置预览 | 当前配置 -->
            <div class="mb-4 mt-3">
              <div class="inline-flex items-center gap-1 rounded-xl border border-slate-200 bg-slate-50 p-1 dark:border-slate-700 dark:bg-slate-900/60">
                <button
                  type="button"
                  :aria-pressed="activeTab === 'edit'"
                  :class="[
                    'min-h-8 rounded-lg px-3.5 text-xs font-semibold transition',
                    activeTab === 'edit'
                      ? 'bg-white text-primary shadow-sm dark:bg-slate-800 dark:text-primary'
                      : 'text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
                  ]"
                  @click="activeTab = 'edit'"
                >
                  {{ t('qs_tab_preview') }}
                </button>
                <button
                  type="button"
                  :aria-pressed="activeTab === 'state'"
                  :class="[
                    'min-h-8 rounded-lg px-3.5 text-xs font-semibold transition',
                    activeTab === 'state'
                      ? 'bg-white text-primary shadow-sm dark:bg-slate-800 dark:text-primary'
                      : 'text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200',
                  ]"
                  @click="activeTab = 'state'"
                >
                  {{ t('qs_tab_backup') }}
                </button>
              </div>
            </div>

            <!-- 当前配置页签：磁盘实况查看器 + 一键恢复 -->
            <QuickSetupStatePanel
              v-if="activeTab === 'state'"
              :software="selectedSoftware"
              @confirm-open-change="nestedDialogOpen = $event"
            />

            <template v-else>
              <!-- 应用结果视图：apply 成功后替换组合内容区；返回编辑回到渲染视图 -->
              <QuickSetupResultPanel
                v-if="applyResult"
                :software-name="applyResult.softwareName"
                :written="applyResult.written"
                :backups="applyResult.backups"
                :files="applyResult.files"
                @back="applyResult = null"
                @view-current="activeTab = 'state'"
              />

              <!-- configure：变量表单（确定 = PUT 保存，渲染视图即时刷新） -->
              <QuickSetupConfigurePanel
                v-else-if="configureOpen && activeCombo"
                :variables="activeCombo.variables || {}"
                :presets="catalogPresets"
                :api-keys="apiKeys"
                @confirm="onConfigureConfirm"
                @cancel="configureOpen = false"
              />

              <!-- 模板编辑：textarea 草稿 + 显式保存 -->
              <div v-else-if="templateEditing && activeFile" class="mt-2 space-y-3">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">
                    {{ fileLabelFor(activeFileCode) }}
                  </p>
                  <div class="flex flex-wrap items-center gap-1.5">
                    <span class="text-[11px] text-slate-500 dark:text-slate-400">{{ t('qs_tpl_insert_var') }}</span>
                    <button
                      v-for="varName in templateVariableNames"
                      :key="`insert-${varName}`"
                      type="button"
                      class="min-h-7 rounded-lg border border-slate-200 px-2 font-mono text-[11px] text-slate-600 transition hover:border-primary/40 hover:text-primary dark:border-slate-700 dark:text-slate-300 dark:hover:border-primary/40 dark:hover:text-primary"
                      @click="insertVariable(varName)"
                    >
                      {{ placeholderToken(varName) }}
                    </button>
                  </div>
                </div>
                <textarea
                  ref="templateTextareaEl"
                  v-model="templateDraft"
                  class="code-editor h-[360px] w-full rounded-2xl border border-slate-200 bg-slate-950 px-4 py-3 text-[12px] leading-6 text-slate-100 outline-none transition focus:border-primary dark:border-slate-700"
                  spellcheck="false"
                ></textarea>
                <div class="flex items-center justify-end gap-2">
                  <button
                    type="button"
                    class="inline-flex min-h-9 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                    @click="cancelTemplateEditing"
                  >
                    {{ t('qs_tpl_cancel') }}
                  </button>
                  <button
                    type="button"
                    class="inline-flex min-h-9 items-center justify-center rounded-lg bg-primary px-4 text-xs font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="comboSaving"
                    @click="saveTemplate"
                  >
                    {{ t('qs_tpl_save') }}
                  </button>
                </div>
              </div>

              <!-- 两列 diff 视图：左=渲染预览（apply 所见），右=上次应用快照；变更行淡色标记 -->
              <div v-else-if="activeFile" class="mt-2">
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <div class="flex min-w-0 items-center gap-2">
                    <span class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_targetPath') }}</span>
                    <code class="min-w-0 truncate font-mono text-[12px] text-slate-600 dark:text-slate-300" :title="activeFileDecl?.default_path">
                      {{ activeFileDecl?.default_path || fileLabelFor(activeFileCode) }}
                    </code>
                  </div>
                  <div class="flex items-center gap-2">
                    <button
                      type="button"
                      class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
                      @click="startTemplateEditing"
                    >
                      {{ t('qs_tpl_edit') }}
                    </button>
                  </div>
                </div>

                <div class="mt-2 grid gap-3 xl:grid-cols-2">
                  <!-- 左列：配置预览（渲染） -->
                  <section class="min-w-0 overflow-hidden rounded-2xl border border-slate-200 dark:border-slate-700">
                    <p class="border-b border-slate-200 bg-slate-100/70 px-4 py-2 text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-400">
                      {{ t('qs_diff_left') }}
                    </p>
                    <div class="code-editor custom-scrollbar max-h-[420px] min-h-[240px] overflow-auto bg-slate-950 py-3 text-[12px] text-slate-100">
                      <div
                        v-for="(line, index) in diffView.leftLines"
                        :key="`diff-left-${index}`"
                        class="min-h-6 whitespace-pre px-4 leading-6"
                        :class="diffView.leftFlags[index] ? 'bg-amber-400/15' : ''"
                      >{{ line }}</div>
                    </div>
                  </section>

                  <!-- 右列：上次应用（快照 + 本地时间） -->
                  <section class="min-w-0 overflow-hidden rounded-2xl border border-slate-200 dark:border-slate-700">
                    <p class="border-b border-slate-200 bg-slate-100/70 px-4 py-2 text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-400">
                      {{ t('qs_diff_right') }}
                      <span
                        v-if="appliedAtLabel"
                        class="ml-1 font-mono text-[10px] font-normal normal-case tracking-normal text-slate-400 dark:text-slate-500"
                      >{{ appliedAtLabel }}</span>
                    </p>
                    <div
                      v-if="appliedContent === null"
                      class="flex max-h-[420px] min-h-[240px] items-center justify-center bg-slate-950 px-4 text-[12px] text-slate-500"
                    >
                      {{ t('qs_diff_never_applied') }}
                    </div>
                    <div v-else class="code-editor custom-scrollbar max-h-[420px] min-h-[240px] overflow-auto bg-slate-950 py-3 text-[12px] text-slate-100">
                      <div
                        v-for="(line, index) in diffView.rightLines"
                        :key="`diff-right-${index}`"
                        class="min-h-6 whitespace-pre px-4 leading-6"
                        :class="diffView.rightFlags[index] ? 'bg-amber-400/15' : ''"
                      >{{ line }}</div>
                    </div>
                  </section>
                </div>
              </div>

              <!-- 该 agent 暂无组合（理论上会被服务端种子兜底） -->
              <div
                v-else-if="!agentCombos.length"
                class="mt-6 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-5 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400"
              >
                {{ t('qs_combo_empty') }}
              </div>
            </template>

            <div v-if="statusMessage" class="mt-6 rounded-2xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600 dark:border-slate-800 dark:bg-slate-900/50 dark:text-slate-300">
              {{ statusMessage }}
            </div>
          </template>
        </div>
      </div>
    </div>

    <!-- 新建组合弹窗（三入口） -->
    <div
      v-if="comboCreating"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('qs_combo_source_title')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeCreateDialog"></div>
      <div class="relative z-10 w-full max-w-md rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('qs_combo_source_title') }}</h4>

        <div class="mt-3 grid gap-2">
          <button
            v-for="option in comboSourceOptions"
            :key="option.value"
            type="button"
            :aria-pressed="createSource === option.value"
            :class="[
              'rounded-xl border px-3 py-2 text-left transition',
              createSource === option.value
                ? 'border-primary/40 bg-primary/10 text-slate-900 dark:bg-primary/10 dark:text-white'
                : 'border-slate-200 bg-white text-slate-700 hover:border-primary/30 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200',
            ]"
            @click="createSource = option.value"
          >
            <span class="block text-xs font-semibold">{{ t(option.labelKey) }}</span>
          </button>
        </div>
        <p v-if="createSource === 'disk'" class="mt-2 text-[11px] leading-5 text-slate-500 dark:text-slate-400">
          {{ t('qs_combo_source_disk_hint') }}
        </p>

        <label class="mt-3 block">
          <span class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_combo_name') }}</span>
          <input
            v-model="createName"
            type="text"
            :placeholder="t('qs_combo_name')"
            class="h-9 w-full rounded-lg border border-slate-200 bg-slate-50 px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          />
        </label>

        <label v-if="createSource === 'copy'" class="mt-3 block">
          <span class="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_combo_copy_source') }}</span>
          <select
            v-model="createCopyFromId"
            class="h-9 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            @change="onCopySourceChange"
          >
            <option value="" disabled>{{ t('qs_combo_copy_source') }}</option>
            <option v-for="combo in agentCombos" :key="`copy-src-${combo.id}`" :value="String(combo.id)">
              {{ combo.name }}
            </option>
          </select>
        </label>

        <p v-if="createError" class="mt-2 text-[11px] leading-5 text-rose-600 dark:text-rose-400">{{ createError }}</p>

        <div class="mt-5 flex items-center justify-end gap-2">
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
            @click="closeCreateDialog"
          >
            {{ t('qs_cancel') }}
          </button>
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg bg-primary px-4 text-xs font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="comboSaving || !createReady"
            @click="confirmCreateCombo"
          >
            {{ t('qs_combo_create_confirm') }}
          </button>
        </div>
      </div>
    </div>

    <!-- 删除组合确认弹窗 -->
    <div
      v-if="deleteConfirmOpen && activeCombo"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('qs_combo_delete')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="deleteConfirmOpen = false"></div>
      <div class="relative z-10 w-full max-w-md rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('qs_combo_delete') }}</h4>
        <p class="mt-2 text-[12px] leading-5 text-slate-500 dark:text-slate-400">
          {{ t('qs_combo_delete_confirm', { name: activeCombo.name }) }}
        </p>
        <p v-if="deleteError" class="mt-2 text-[11px] leading-5 text-rose-600 dark:text-rose-400">{{ deleteError }}</p>
        <div class="mt-5 flex items-center justify-end gap-2">
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
            @click="deleteConfirmOpen = false"
          >
            {{ t('qs_cancel') }}
          </button>
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg bg-rose-600 px-4 text-xs font-semibold text-white transition hover:bg-rose-500 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="comboSaving"
            @click="confirmDeleteCombo"
          >
            {{ t('qs_combo_delete') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import {
  applyQuickSetup,
  createCombo,
  deleteCombo,
  getQuickSetupCatalog,
  setComboDefault,
  updateCombo,
} from '../services/quickSetupApi';
import { useI18n } from '../i18n';
import QuickSetupConfigurePanel from './QuickSetupConfigurePanel.vue';
import QuickSetupResultPanel from './QuickSetupResultPanel.vue';
import QuickSetupStatePanel from './QuickSetupStatePanel.vue';
import { diffChangedLines, findUnresolvedPlaceholders, renderComboContent } from '../utils/quickSetupState';

const { t } = useI18n();

const props = defineProps({
  open: {
    type: Boolean,
    default: false,
  },
});
const emit = defineEmits(['close']);
const loadingCatalog = ref(false);
const applying = ref(false);
// 组合元数据写操作（重命名/设默认/删除/新建/保存模板/configure 保存）进行中标记
const comboSaving = ref(false);
const catalogStatus = ref('idle');
const catalogMessage = ref('');
const statusMessage = ref('');
// 右侧面板页签：'edit'=组合内容（渲染/模板/configure/结果视图）| 'state'=当前配置（磁盘实况）。
// 切 software / 选组合时重置为 edit。
const activeTab = ref('edit');
// StatePanel 嵌套确认弹窗打开或恢复进行中时为 true：外层 Esc 关闭被屏蔽，避免绕过确认守卫
const nestedDialogOpen = ref(false);
const softwares = ref([]);
const apiKeys = ref([]);
const selectedSoftware = ref('');
// 组合列表：catalog 整体替换；CRUD 响应就地维护（{combo}/{combos}/{id}），不整页重拉
const combos = ref([]);
const activeCombo = ref(null);
const activeFileCode = ref('');
const templateEditing = ref(false);
const templateDraft = ref('');
const configureOpen = ref(false);
const applyResult = ref(null);
// 组合 tabs 操作区
const comboMenuOpen = ref(false);
const renameOpen = ref(false);
const renameDraft = ref('');
const deleteConfirmOpen = ref(false);
const deleteError = ref('');
// 新建组合弹窗（三入口：blank/copy/disk）
const comboCreating = ref(false);
const createSource = ref('blank');
const createName = ref('');
const createCopyFromId = ref('');
const createError = ref('');
// 模板编辑 textarea 引用（插入变量定位光标）
const templateTextareaEl = ref(null);

// 「插入变量」三个标准变量（与后端变量集一致）
const templateVariableNames = ['base_url', 'api_key', 'model'];
const comboSourceOptions = [
  { value: 'blank', labelKey: 'qs_combo_source_blank' },
  { value: 'copy', labelKey: 'qs_combo_source_copy' },
  { value: 'disk', labelKey: 'qs_combo_source_disk' },
];

const installedSoftwares = computed(() => softwares.value.filter((item) => item.installed));
const selectedSoftwareDef = computed(() => installedSoftwares.value.find((item) => item.code === selectedSoftware.value) || null);
const agentCombos = computed(() => combos.value.filter((combo) => combo.software === selectedSoftware.value));
// 当前 agent 的 base_url 预设（catalog softwares[].presets，后端 omitempty 指针可能缺省）
const catalogPresets = computed(() => selectedSoftwareDef.value?.presets || {});
const activeFile = computed(() => {
  const files = Array.isArray(activeCombo.value?.files) ? activeCombo.value.files : [];
  return files.find((file) => file.code === activeFileCode.value) || null;
});
const activeFileDecl = computed(() => {
  const files = Array.isArray(selectedSoftwareDef.value?.files) ? selectedSoftwareDef.value.files : [];
  return files.find((file) => file.code === activeFileCode.value) || null;
});
// 渲染视图 = apply 所见（同一 renderComboContent 产物）
const renderedContent = computed(() => renderComboContent(activeFile.value?.content, activeCombo.value?.variables));
// 右列内容：上次应用快照（applied）中该 code 的 content；null = 该组合从未应用过
const appliedContent = computed(() => {
  const applied = Array.isArray(activeCombo.value?.applied) ? activeCombo.value.applied : [];
  const hit = applied.find((item) => item?.code === activeFileCode.value);
  return hit ? String(hit.content ?? '') : null;
});
// applied_at（RFC3339）→ 本地时间展示；空串（从未应用）不出标签，解析失败原样透出
const appliedAtLabel = computed(() => {
  const raw = String(activeCombo.value?.applied_at || '');
  if (!raw) {
    return '';
  }
  const date = new Date(raw);
  return Number.isNaN(date.getTime()) ? raw : date.toLocaleString();
});
// 两列 diff 视图：行数组 + 变更行标记（右列从未应用时渲染空态，flags 不参与展示）
const diffView = computed(() => {
  const rightContent = appliedContent.value;
  const { leftFlags, rightFlags } = diffChangedLines(renderedContent.value, rightContent ?? '');
  return {
    leftLines: String(renderedContent.value ?? '').split('\n'),
    rightLines: rightContent === null ? [] : rightContent.split('\n'),
    leftFlags,
    rightFlags,
  };
});
// apply 前置校验：渲染后不得残留占位符，且模板引用的变量值必须非空
// （空白种子的 api_key/model 是 ""，仅查渲染标记拦不住空值）
const applyReadiness = computed(() => {
  const combo = activeCombo.value;
  const files = Array.isArray(combo?.files) ? combo.files : [];
  const vars = combo?.variables || {};
  const missing = new Set();
  for (const file of files) {
    const content = String(file?.content ?? '');
    for (const name of findUnresolvedPlaceholders(renderComboContent(content, vars))) {
      missing.add(name);
    }
    for (const name of findUnresolvedPlaceholders(content)) {
      if (!String(vars[name] ?? '').trim()) {
        missing.add(name);
      }
    }
  }
  return {
    ready: Boolean(combo) && files.length > 0 && missing.size === 0,
    missing: [...missing],
  };
});
// 模板草稿与已保存内容是否有差异
const templateDirty = computed(() => {
  if (!templateEditing.value || !activeFile.value) {
    return false;
  }
  return templateDraft.value !== String(activeFile.value.content ?? '');
});
const createReady = computed(() => {
  if (!createName.value.trim()) {
    return false;
  }
  if (createSource.value === 'copy') {
    return Boolean(agentCombos.value.some((combo) => String(combo.id) === String(createCopyFromId.value)));
  }
  return true;
});
// 组合自身的嵌套弹窗（新建/删除确认）占用期：与 StatePanel 的 nestedDialogOpen 并联，
// 任一占用都屏蔽外层 Esc（两个来源互不覆盖）
const comboOverlayOpen = computed(() => comboCreating.value || deleteConfirmOpen.value);

function selectSoftware(code) {
  if (selectedSoftware.value === code) {
    return;
  }
  selectedSoftware.value = code;
}

function fileLabelFor(code) {
  const files = Array.isArray(selectedSoftwareDef.value?.files) ? selectedSoftwareDef.value.files : [];
  return files.find((file) => file.code === code)?.label || code;
}

// 占位符字面文本（{{base_url}} 等）；经函数返回以避免模板插值与字面花括号冲突
function placeholderToken(name) {
  return `{{${name}}}`;
}

// 选中组合：整对象替换 + 文件 tab 回到第一个 + 瞬态视图复位（绝不深改 .variables）
function selectCombo(combo) {
  activeCombo.value = combo || null;
  const files = Array.isArray(combo?.files) ? combo.files : [];
  activeFileCode.value = files[0]?.code || '';
  templateEditing.value = false;
  templateDraft.value = '';
  applyResult.value = null;
  activeTab.value = 'edit';
  comboMenuOpen.value = false;
  renameOpen.value = false;
}

// 预选：is_default 优先，否则第一个；无组合时清空瞬态
function pickDefaultCombo() {
  const list = agentCombos.value;
  selectCombo(list.find((combo) => combo.is_default) || list[0] || null);
  configureOpen.value = false;
  comboCreating.value = false;
  deleteConfirmOpen.value = false;
  createError.value = '';
  deleteError.value = '';
  statusMessage.value = '';
}

function switchCombo(combo) {
  if (activeCombo.value?.id === combo.id) {
    return;
  }
  if (!confirmDiscardTemplateDraft()) {
    return;
  }
  selectCombo(combo);
}

function switchFileTab(code) {
  if (activeFileCode.value === code) {
    return;
  }
  if (!confirmDiscardTemplateDraft()) {
    return;
  }
  activeFileCode.value = code;
  // 恒复位编辑态：文件页签切换永远退出模板编辑视图（渲染视图是默认视图）。
  // 无改动路径若不复位，上一文件的草稿会残留 textarea，点保存即把 A 的内容写进 B（跨文件模板覆盖）
  templateEditing.value = false;
  templateDraft.value = '';
}

// 有未保存草稿时提示放弃；确认后才允许离开（返回 false = 留在原地）
function confirmDiscardTemplateDraft() {
  if (!templateDirty.value) {
    return true;
  }
  if (window.confirm(t('qs_combo_dirty'))) {
    templateEditing.value = false;
    templateDraft.value = '';
    return true;
  }
  return false;
}

function startTemplateEditing() {
  if (!activeFile.value) {
    return;
  }
  templateDraft.value = String(activeFile.value.content ?? '');
  templateEditing.value = true;
}

function cancelTemplateEditing() {
  templateEditing.value = false;
  templateDraft.value = '';
}

// 在光标处插入 {{var}}；无光标信息（未聚焦）时追加到末尾
function insertVariable(name) {
  const token = `{{${name}}}`;
  const el = templateTextareaEl.value;
  const current = templateDraft.value;
  if (el && typeof el.selectionStart === 'number') {
    const start = el.selectionStart;
    const end = el.selectionEnd;
    templateDraft.value = current.slice(0, start) + token + current.slice(end);
    nextTick(() => {
      el.focus();
      el.selectionStart = el.selectionEnd = start + token.length;
    });
    return;
  }
  templateDraft.value = current + token;
}

async function saveTemplate() {
  const combo = activeCombo.value;
  if (!combo || !activeFile.value) {
    return;
  }
  const files = (Array.isArray(combo.files) ? combo.files : []).map((file) => (
    file.code === activeFileCode.value ? { ...file, content: templateDraft.value } : file
  ));
  comboSaving.value = true;
  try {
    const result = await updateCombo(combo.id, { files });
    const next = result?.combo;
    if (next) {
      adoptCombo(next);
    }
    templateEditing.value = false;
    templateDraft.value = '';
    statusMessage.value = '';
  } catch (error) {
    statusMessage.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

function openConfigure() {
  if (!activeCombo.value) {
    return;
  }
  if (!confirmDiscardTemplateDraft()) {
    return;
  }
  comboMenuOpen.value = false;
  configureOpen.value = true;
}

// configure 确定：PUT variables → {combo} 整体替换（渲染视图即时刷新）
async function onConfigureConfirm(variables) {
  const combo = activeCombo.value;
  if (!combo) {
    configureOpen.value = false;
    return;
  }
  comboSaving.value = true;
  try {
    const result = await updateCombo(combo.id, {
      variables: variables && typeof variables === 'object' ? { ...variables } : {},
    });
    const next = result?.combo;
    if (next) {
      adoptCombo(next);
    }
    configureOpen.value = false;
    statusMessage.value = '';
  } catch (error) {
    statusMessage.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

function openRename() {
  if (!activeCombo.value) {
    return;
  }
  comboMenuOpen.value = false;
  renameDraft.value = activeCombo.value.name;
  renameOpen.value = true;
}

async function confirmRename() {
  const combo = activeCombo.value;
  const name = renameDraft.value.trim();
  if (!combo || !name || name === combo.name) {
    renameOpen.value = false;
    return;
  }
  comboSaving.value = true;
  try {
    const result = await updateCombo(combo.id, { name });
    const next = result?.combo;
    if (next) {
      adoptCombo(next);
    }
    renameOpen.value = false;
  } catch (error) {
    statusMessage.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

// 设默认：响应 {combos} 为该 software 全部组合，整组替换
async function makeDefault() {
  const combo = activeCombo.value;
  if (!combo || combo.is_default) {
    return;
  }
  comboMenuOpen.value = false;
  comboSaving.value = true;
  try {
    const result = await setComboDefault(combo.id);
    const list = Array.isArray(result?.combos) ? result.combos : null;
    if (list) {
      const others = combos.value.filter((item) => item.software !== combo.software);
      combos.value = [...others, ...list];
      const next = list.find((item) => item.id === combo.id);
      if (next) {
        activeCombo.value = next;
      }
    }
  } catch (error) {
    statusMessage.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

async function confirmDeleteCombo() {
  const combo = activeCombo.value;
  if (!combo) {
    deleteConfirmOpen.value = false;
    return;
  }
  comboSaving.value = true;
  deleteError.value = '';
  try {
    const result = await deleteCombo(combo.id);
    const removedId = result && typeof result.id !== 'undefined' ? result.id : combo.id;
    combos.value = combos.value.filter((item) => item.id !== removedId);
    deleteConfirmOpen.value = false;
    if (activeCombo.value?.id === removedId) {
      // 删的是当前组合（含 default）：回退 is_default 优先否则第一个
      pickDefaultCombo();
    }
  } catch (error) {
    deleteError.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

function openCreateDialog() {
  comboMenuOpen.value = false;
  createSource.value = 'blank';
  createName.value = '';
  createCopyFromId.value = '';
  createError.value = '';
  comboCreating.value = true;
}

function closeCreateDialog() {
  comboCreating.value = false;
  createError.value = '';
}

// 复制入口：选定源组合后默认名 = <源名>副本
function onCopySourceChange() {
  const source = agentCombos.value.find((combo) => String(combo.id) === String(createCopyFromId.value));
  createName.value = source ? `${source.name}${t('qs_combo_copy_name_suffix')}` : '';
}

async function confirmCreateCombo() {
  const name = createName.value.trim();
  if (!name || !selectedSoftware.value) {
    return;
  }
  const payload = {
    software: selectedSoftware.value,
    name,
    source: createSource.value,
  };
  if (createSource.value === 'copy') {
    payload.copy_from_id = Number(createCopyFromId.value) || 0;
  }
  comboSaving.value = true;
  createError.value = '';
  try {
    const result = await createCombo(payload);
    const combo = result?.combo;
    if (combo) {
      combos.value = [...combos.value, combo];
      selectCombo(combo);
    }
    comboCreating.value = false;
  } catch (error) {
    createError.value = errorText(error);
  } finally {
    comboSaving.value = false;
  }
}

// CRUD 响应就地维护：combos 列表对应项替换 + activeCombo 整对象替换（引用型）
function replaceComboInList(combo) {
  const index = combos.value.findIndex((item) => item.id === combo.id);
  if (index >= 0) {
    const next = [...combos.value];
    next[index] = combo;
    combos.value = next;
    return;
  }
  combos.value = [...combos.value, combo];
}

function adoptCombo(combo) {
  replaceComboInList(combo);
  activeCombo.value = combo;
  const codes = Array.isArray(combo?.files) ? combo.files.map((file) => file.code) : [];
  if (codes.length && !codes.includes(activeFileCode.value)) {
    activeFileCode.value = codes[0];
  }
}

// apply：组合每文件 → 声明的 DefaultPath/Format/Kind + 渲染后内容
async function applyCombo() {
  const combo = activeCombo.value;
  const def = selectedSoftwareDef.value;
  if (!combo || !def || !applyReadiness.value.ready) {
    return;
  }
  const declaredByCode = new Map((def.files || []).map((file) => [file.code, file]));
  const vars = combo.variables || {};
  const filesToApply = (combo.files || [])
    .filter((file) => declaredByCode.has(file.code))
    .map((file) => {
      const declared = declaredByCode.get(file.code);
      return {
        path: declared.default_path,
        content: renderComboContent(file.content, vars),
        format: declared.format,
        kind: declared.kind,
      };
    });
  if (!filesToApply.length) {
    statusMessage.value = t('qs_noFilesWritten');
    return;
  }
  // 乐观快照源：与 filesToApply 同源（code + 本次渲染内容），apply 成功后整对象替换 activeCombo
  const appliedSnapshot = (combo.files || [])
    .filter((file) => declaredByCode.has(file.code))
    .map((file) => ({ code: file.code, content: renderComboContent(file.content, vars) }));
  applying.value = true;
  try {
    const result = await applyQuickSetup({
      software: selectedSoftware.value,
      files: filesToApply,
      combo_id: combo.id,
    });
    const writtenCount = Array.isArray(result?.written) ? result.written.length : 0;
    if (writtenCount > 0) {
      statusMessage.value = '';
      // 结果页渲染在 edit 页签内；apply 入口在 header，防御性确保结果视图可见
      activeTab.value = 'edit';
      // 乐观更新上次应用快照：右列 diff 即时变干净（后端已持久化权威快照，重开弹窗自然对齐；
      // 绝不深改原组合——展开为整对象新引用替换）
      adoptCombo({
        ...combo,
        applied: appliedSnapshot,
        applied_at: new Date().toISOString(),
      });
      // 应用前的渲染快照即写入磁盘的最终内容（path+content 窄快照，供结果面板展示）
      applyResult.value = {
        softwareName: `${def.name || selectedSoftware.value} · ${combo.name}`,
        written: result.written,
        backups: Array.isArray(result?.backups) ? result.backups : [],
        files: filesToApply.map((file) => ({ path: file.path, content: file.content })),
      };
    } else {
      statusMessage.value = t('qs_noFilesWritten');
    }
  } catch (error) {
    statusMessage.value = error instanceof Error ? error.message : t('qs_failedApply');
  } finally {
    applying.value = false;
  }
}

function errorText(error) {
  return error instanceof Error ? error.message : String(error ?? '');
}

function onModalKeydown(event) {
  // 嵌套确认弹窗占用期（StatePanel 恢复守卫 / 组合新建与删除确认）不响应外层 Esc
  if (nestedDialogOpen.value || comboOverlayOpen.value) {
    return;
  }
  if (event.key === 'Escape' && props.open) {
    event.stopImmediatePropagation();
    emit('close');
  }
}
async function loadCatalog() {
  loadingCatalog.value = true;
  statusMessage.value = '';
  try {
    const result = await getQuickSetupCatalog();
    catalogStatus.value = result.status || 'idle';
    catalogMessage.value = result.message || '';
    if (result.status !== 'success' || !result.data) {
      softwares.value = [];
      apiKeys.value = [];
      combos.value = [];
      pickDefaultCombo();
      return;
    }
    softwares.value = Array.isArray(result.data.softwares) ? result.data.softwares : [];
    apiKeys.value = Array.isArray(result.data.api_keys) ? result.data.api_keys : [];
    combos.value = Array.isArray(result.data.combos) ? result.data.combos : [];
    if (!selectedSoftware.value || !installedSoftwares.value.some((item) => item.code === selectedSoftware.value)) {
      selectedSoftware.value = installedSoftwares.value[0]?.code || '';
    }
    // 组合列表整体替换后重选（含同 agent 重开弹窗刷新场景）
    pickDefaultCombo();
  } catch (error) {
    catalogStatus.value = 'failed';
    catalogMessage.value = error instanceof Error ? error.message : t('qs_failedCatalog');
    statusMessage.value = catalogMessage.value;
  } finally {
    loadingCatalog.value = false;
  }
}
watch(
  () => props.open,
  async (value) => {
    if (!value) {
      // 双保险：StatePanel 卸载时会 emit false，这里确保弹窗关闭瞬间状态不残留
      nestedDialogOpen.value = false;
      return;
    }
    await loadCatalog();
  },
  { immediate: true },
);
// 切 agent：activeCombo 回预选（is_default 优先否则第一个），模板/弹窗态复位；
// 纯同步复位，快速连切只按最新值收敛一次，无竞态
watch(selectedSoftware, () => {
  pickDefaultCombo();
});
onMounted(() => {
  window.addEventListener('keydown', onModalKeydown, true);
});
onUnmounted(() => {
  window.removeEventListener('keydown', onModalKeydown, true);
});
</script>
