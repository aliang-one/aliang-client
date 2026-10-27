<template>
  <div class="fixed inset-0 z-[900] flex bg-white dark:bg-slate-950">
    <!-- Mobile: conversation list overlay -->
    <div
      v-if="showMobileSidebar"
      class="fixed inset-0 z-10 bg-black/50 md:hidden"
      @click="showMobileSidebar = false"
    ></div>

    <!-- Sidebar: conversation list -->
    <aside
      class="fixed left-0 top-0 bottom-0 z-20 w-72 flex flex-col border-r border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-900 transition-transform duration-200 md:relative md:translate-x-0"
      :class="showMobileSidebar ? 'translate-x-0' : '-translate-x-full'"
    >
      <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <h2 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('chat_title') }}</h2>
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-200 dark:hover:bg-slate-700"
          @click="createConversation"
        >
          <span class="material-symbols-outlined text-lg">add</span>
        </button>
      </div>

      <div class="flex-1 overflow-y-auto py-1">
        <template v-if="groupedConversations.today.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_today') }}</div>
          <div
            v-for="c in groupedConversations.today"
            :key="c.id"
            role="button"
            tabindex="0"
            class="group w-full cursor-pointer px-4 py-2.5 text-left text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
            @keydown.enter.prevent="selectConversation(c.id)"
            @keydown.space.prevent="selectConversation(c.id)"
          >
            <input
              v-if="renamingId === c.id && renameSurface === 'list'"
              v-model="renameDraft"
              :ref="(el) => { if (el) renameInputEl = el; }"
              type="text"
              maxlength="60"
              :placeholder="t('chat_renamePlaceholder')"
              class="w-full rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
              @click.stop
              @keydown.enter="onRenameEnter"
              @keydown.esc="onRenameEscape"
              @keydown.space.stop
              @blur="commitRename"
            />
            <div v-else class="flex items-center gap-2">
              <div class="min-w-0 flex-1 truncate">{{ c.title || t('chat_emptyTitle') }}</div>
              <button
                type="button"
                class="relative shrink-0 rounded p-1 text-slate-400 opacity-100 transition-opacity hover:bg-slate-200 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100 dark:hover:bg-slate-700 before:absolute before:-inset-2 before:content-['']"
                :aria-label="`${t('chat_rename')} ${c.title || t('chat_emptyTitle')}`"
                @click.stop="startRename(c, 'list')"
                @keydown.enter.stop
                @keydown.space.stop
              >
                <span class="material-symbols-outlined text-base">edit</span>
              </button>
            </div>
          </div>
        </template>
        <template v-if="groupedConversations.yesterday.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_yesterday') }}</div>
          <div
            v-for="c in groupedConversations.yesterday"
            :key="c.id"
            role="button"
            tabindex="0"
            class="group w-full cursor-pointer px-4 py-2.5 text-left text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
            @keydown.enter.prevent="selectConversation(c.id)"
            @keydown.space.prevent="selectConversation(c.id)"
          >
            <input
              v-if="renamingId === c.id && renameSurface === 'list'"
              v-model="renameDraft"
              :ref="(el) => { if (el) renameInputEl = el; }"
              type="text"
              maxlength="60"
              :placeholder="t('chat_renamePlaceholder')"
              class="w-full rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
              @click.stop
              @keydown.enter="onRenameEnter"
              @keydown.esc="onRenameEscape"
              @keydown.space.stop
              @blur="commitRename"
            />
            <div v-else class="flex items-center gap-2">
              <div class="min-w-0 flex-1 truncate">{{ c.title || t('chat_emptyTitle') }}</div>
              <button
                type="button"
                class="relative shrink-0 rounded p-1 text-slate-400 opacity-100 transition-opacity hover:bg-slate-200 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100 dark:hover:bg-slate-700 before:absolute before:-inset-2 before:content-['']"
                :aria-label="`${t('chat_rename')} ${c.title || t('chat_emptyTitle')}`"
                @click.stop="startRename(c, 'list')"
                @keydown.enter.stop
                @keydown.space.stop
              >
                <span class="material-symbols-outlined text-base">edit</span>
              </button>
            </div>
          </div>
        </template>
        <template v-if="groupedConversations.older.length">
          <div class="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 uppercase tracking-wider">{{ t('chat_older') }}</div>
          <div
            v-for="c in groupedConversations.older"
            :key="c.id"
            role="button"
            tabindex="0"
            class="group w-full cursor-pointer px-4 py-2.5 text-left text-sm transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
            :class="c.id === activeId ? 'bg-primary/10 text-primary font-medium' : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'"
            @click="selectConversation(c.id)"
            @keydown.enter.prevent="selectConversation(c.id)"
            @keydown.space.prevent="selectConversation(c.id)"
          >
            <input
              v-if="renamingId === c.id && renameSurface === 'list'"
              v-model="renameDraft"
              :ref="(el) => { if (el) renameInputEl = el; }"
              type="text"
              maxlength="60"
              :placeholder="t('chat_renamePlaceholder')"
              class="w-full rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
              @click.stop
              @keydown.enter="onRenameEnter"
              @keydown.esc="onRenameEscape"
              @keydown.space.stop
              @blur="commitRename"
            />
            <div v-else class="flex items-center gap-2">
              <div class="min-w-0 flex-1 truncate">{{ c.title || t('chat_emptyTitle') }}</div>
              <button
                type="button"
                class="relative shrink-0 rounded p-1 text-slate-400 opacity-100 transition-opacity hover:bg-slate-200 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 md:opacity-0 md:group-hover:opacity-100 md:focus-visible:opacity-100 dark:hover:bg-slate-700 before:absolute before:-inset-2 before:content-['']"
                :aria-label="`${t('chat_rename')} ${c.title || t('chat_emptyTitle')}`"
                @click.stop="startRename(c, 'list')"
                @keydown.enter.stop
                @keydown.space.stop
              >
                <span class="material-symbols-outlined text-base">edit</span>
              </button>
            </div>
          </div>
        </template>
        <div v-if="conversations.length === 0" class="px-4 py-8 text-center text-sm text-slate-400">
          {{ t('chat_noConversations') }}
        </div>
      </div>
    </aside>

    <!-- Main chat area -->
    <div class="flex flex-1 flex-col min-w-0">
      <!-- Header -->
      <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-100 md:hidden dark:hover:bg-slate-800"
          @click="showMobileSidebar = true"
        >
          <span class="material-symbols-outlined text-lg">menu</span>
        </button>
        <input
          v-if="activeId && renamingId === activeId && renameSurface === 'header'"
          v-model="renameDraft"
          :ref="(el) => { if (el) renameInputEl = el; }"
          type="text"
          maxlength="60"
          :placeholder="t('chat_renamePlaceholder')"
          class="min-w-0 flex-1 rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
          @click.stop
          @keydown.enter="onRenameEnter"
          @keydown.esc="onRenameEscape"
          @blur="commitRename"
        />
        <h3 v-else class="flex-1 truncate text-sm font-semibold text-slate-900 dark:text-white">
          {{ activeConversation?.title || t('chat_emptyTitle') }}
        </h3>
        <button
          v-if="activeId && renamingId !== activeId"
          type="button"
          class="relative rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 dark:hover:bg-slate-800 before:absolute before:-inset-2 before:content-['']"
          :aria-label="t('chat_rename')"
          @click="startRename(activeConversation, 'header')"
        >
          <span class="material-symbols-outlined text-lg">edit</span>
        </button>
        <span
          v-if="activeId && activeRunning"
          class="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
        >
          <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
        </span>
        <button
          v-if="activeId"
          type="button"
          class="rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-rose-500 dark:hover:bg-slate-800"
          @click="deleteConversation"
        >
          <span class="material-symbols-outlined text-lg">delete</span>
        </button>
        <button
          type="button"
          class="rounded p-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800"
          @click="close"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </div>

      <!-- Session settings bar: directory / model / effort -->
      <div v-if="activeConversation" class="relative border-b border-slate-200 dark:border-slate-800">
        <!-- 内层滚动行：overflow-x-auto 会连 y 轴一起裁剪（CSS 规范行为），
             absolute 浮层若放行内会被整个裁掉，必须挂到外层包裹容器上。 -->
        <div class="flex items-center gap-2 overflow-x-auto px-4 py-1.5 text-xs">
          <span class="hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsDirectory') }}</span>
          <span
            v-if="directoryLocked"
            data-testid="chat-settings-directory"
            class="flex min-h-[40px] max-w-[16rem] shrink-0 items-center gap-1 text-slate-400 md:min-h-0"
            :title="directoryLabel"
          >
            <span class="material-symbols-outlined text-sm">folder</span>
            <span class="truncate">{{ directoryLabel }}</span>
          </span>
          <button
            v-else
            type="button"
            data-testid="chat-settings-directory"
            class="flex min-h-[40px] max-w-[16rem] shrink-0 items-center gap-1 rounded border border-slate-200 px-2 text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800 md:min-h-0"
            :disabled="activeRunning"
            :title="directoryLabel"
            :aria-label="t('chat_settingsDirectory')"
            @click="toggleDirectoryPanel"
          >
            <span class="material-symbols-outlined text-sm">folder</span>
            <span class="truncate">{{ directoryLabel }}</span>
          </button>
          <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsModel') }}</span>
          <select
            v-model="activeModel"
            data-testid="chat-settings-model"
            class="min-h-[40px] shrink-0 rounded border border-slate-200 bg-white px-1 text-slate-600 outline-none disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
            :disabled="activeRunning"
            :aria-label="t('chat_settingsModel')"
          >
            <option v-for="opt in modelOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <input
            v-if="modelCustomActive"
            v-model="modelCustomDraft"
            type="text"
            class="h-9 w-36 shrink-0 rounded border border-slate-200 bg-white px-2 text-xs outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-800"
            :placeholder="t('chat_customModel')"
            @keydown.enter="onCustomModelEnter"
            @blur="commitCustomModel"
          />
          <span class="ml-2 hidden shrink-0 text-slate-400 md:inline">{{ t('chat_settingsEffort') }}</span>
          <select
            v-model="activeEffort"
            data-testid="chat-settings-effort"
            class="min-h-[40px] shrink-0 rounded border border-slate-200 bg-white px-1 text-slate-600 outline-none disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 md:min-h-0"
            :disabled="activeRunning"
            :aria-label="t('chat_settingsEffort')"
          >
            <option v-for="opt in EFFORT_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.value || t('chat_modelDefault') }}</option>
          </select>
          <div
            v-if="createError"
            data-testid="chat-settings-error"
            class="ml-2 shrink-0 truncate text-rose-500"
            :title="createError"
          >{{ createError }}</div>
        </div>

        <!-- 遮罩与浮层是滚动行的兄弟节点（外层容器的直接子级）：不再被裁剪；
             Esc 在浮层上拦截（.stop），不冒泡到文档级 handleKeydown 的 close()。 -->
        <div v-if="directoryPanelOpen" class="fixed inset-0 z-20" @click="directoryPanelOpen = false"></div>
        <div
          v-if="directoryPanelOpen"
          class="absolute left-2 top-full z-30 mt-1 w-80 rounded-lg border border-slate-200 bg-white p-3 shadow-lg dark:border-slate-600 dark:bg-slate-800"
          @keydown.esc.stop="directoryPanelOpen = false"
        >
          <div class="mb-1 text-xs font-semibold text-slate-400">{{ t('chat_recentProjects') }}</div>
          <div v-if="recentProjects.length === 0" class="px-1 py-1 text-xs text-slate-400">{{ t('chat_noRecentProjects') }}</div>
          <button
            v-for="p in recentProjects"
            :key="p"
            type="button"
            class="flex min-h-[40px] w-full items-center rounded px-1 text-left text-xs text-slate-700 transition hover:bg-primary/10 dark:text-slate-200"
            :title="p"
            @click="pickDirectory(p)"
          ><span class="truncate">{{ p }}</span></button>
          <div class="mt-2 flex gap-1">
            <input
              v-model="customPathDraft"
              type="text"
              :placeholder="t('chat_customPathPlaceholder')"
              class="h-10 min-w-0 flex-1 rounded border border-slate-200 bg-white px-2 text-xs outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-800"
              @keydown.enter="onCustomPathEnter"
              @keydown.esc.stop="directoryPanelOpen = false"
            />
            <button
              type="button"
              class="h-10 shrink-0 rounded bg-primary px-3 text-xs font-semibold text-white transition hover:bg-primary/90"
              @click="commitCustomPath"
            >{{ t('chat_pathConfirm') }}</button>
          </div>
        </div>
      </div>

      <!-- Messages -->
      <div ref="messagesContainer" class="flex-1 overflow-y-auto bg-slate-50 p-4 dark:bg-slate-800/40">
        <div v-if="!activeId" class="flex h-full items-center justify-center">
          <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
        </div>
        <template v-else>
          <div v-if="activeMessages.length === 0" class="flex h-full items-center justify-center">
            <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
          </div>
          <div v-for="(msg, idx) in activeMessages" :key="idx" class="mb-3">
            <div class="mb-1 flex items-center gap-2 text-xs text-slate-400">
              <span>{{ msg.role === 'user' ? t('dash_me') : t('dash_ai') }}</span>
              <span v-if="msg.role === 'assistant' && msg.status === 'running'" class="inline-flex items-center gap-1 text-primary">
                <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
              </span>
              <span v-else-if="msg.role === 'assistant' && msg.status === 'done'" class="text-emerald-500">{{ t('chat_statusDone') }}</span>
              <span v-else-if="msg.role === 'assistant' && msg.status === 'error'" class="text-rose-500">!</span>
            </div>
            <div
              class="inline-block max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm"
              :class="msg.role === 'user'
                ? 'ml-auto block bg-primary text-white'
                : msg.status === 'error'
                  ? 'bg-rose-50 text-rose-600 dark:bg-rose-900/30 dark:text-rose-300'
                  : 'bg-white text-slate-700 dark:bg-slate-700 dark:text-slate-100'"
            >
              <span
                v-if="msg.role === 'assistant' && msg.status === 'running' && !msg.content"
                class="ai-thinking"
              >
                <span class="thinking-dot"></span><span class="thinking-dot"></span><span class="thinking-dot"></span>
                <span class="ml-1 align-middle">{{ t('chat_aiThinking') }}</span>
              </span>
              <template v-else>{{ msg.content }}</template>
            </div>
          </div>
          <div
            v-for="approval in activeApprovals"
            :key="approval.key"
            class="mb-3 max-w-[85%] rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-950 shadow-sm dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-100"
          >
            <div class="flex flex-wrap items-center gap-2">
              <span class="material-symbols-outlined text-base">{{ approvalIcon(approval) }}</span>
              <span class="font-semibold">{{ approval.title || t('chat_approvalTitle') }}</span>
              <span class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/60 dark:text-amber-200">
                {{ approval.status === 'responding' ? t('chat_approvalResponding') : t('chat_approvalPending') }}
              </span>
            </div>
            <p v-if="approval.reason" class="mt-1 whitespace-pre-wrap text-xs text-amber-800 dark:text-amber-200">
              {{ approval.reason }}
            </p>
            <pre
              v-if="approval.command"
              class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
            >{{ approval.command }}</pre>
            <pre
              v-else-if="approvalDetailText(approval)"
              class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
            >{{ approvalDetailText(approval) }}</pre>
            <div v-if="approval.cwd || approval.toolName" class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-amber-700 dark:text-amber-200">
              <span v-if="approval.toolName">{{ t('chat_approvalTool') }}: {{ approval.toolName }}</span>
              <span v-if="approval.cwd">{{ t('chat_approvalCwd') }}: {{ approval.cwd }}</span>
            </div>
            <div class="mt-2 flex flex-wrap gap-2">
              <button
                v-if="approvalAllows(approval, 'accept_for_session')"
                type="button"
                class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'accept_for_session')"
              >
                <span class="material-symbols-outlined text-sm">verified_user</span>{{ t('chat_approvalAcceptSession') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'accept')"
                type="button"
                class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'accept')"
              >
                <span class="material-symbols-outlined text-sm">check</span>{{ t('chat_approvalAccept') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'decline')"
                type="button"
                class="inline-flex items-center gap-1 rounded border border-amber-300 bg-white px-2.5 py-1 text-xs font-semibold text-amber-800 transition hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-700 dark:bg-slate-900 dark:text-amber-100 dark:hover:bg-amber-900/50"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'decline')"
              >
                <span class="material-symbols-outlined text-sm">block</span>{{ t('chat_approvalDecline') }}
              </button>
              <button
                v-if="approvalAllows(approval, 'cancel')"
                type="button"
                class="inline-flex items-center gap-1 rounded border border-slate-300 bg-white px-2.5 py-1 text-xs font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
                :disabled="approval.status === 'responding'"
                @click="respondApproval(activeConversation, approval, 'cancel')"
              >
                <span class="material-symbols-outlined text-sm">close</span>{{ t('chat_approvalCancel') }}
              </button>
            </div>
            <p v-if="approval.error" class="mt-1 text-xs text-rose-600 dark:text-rose-300">{{ approval.error }}</p>
          </div>
        </template>
      </div>

      <!-- Input -->
      <div v-if="activeId" class="border-t border-slate-200 p-4 dark:border-slate-700">
        <div class="flex items-center gap-2">
          <div class="relative flex-1">
            <div
              v-if="slashOpen && slashMatches.length"
              class="absolute bottom-full left-0 z-20 mb-2 max-h-60 w-full overflow-auto rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-600 dark:bg-slate-800"
            >
              <button
                v-for="(c, i) in slashMatches"
                :key="c.name"
                type="button"
                class="flex w-full items-baseline gap-2 px-3 py-2 text-left text-sm transition hover:bg-primary/10"
                :class="i === slashIndex ? 'bg-primary/10' : ''"
                @click="applySlash(c)"
                @mouseenter="slashIndex = i"
              >
                <span class="font-mono font-medium text-primary">/{{ c.name }}</span>
                <span v-if="c.arg_hint" class="font-mono text-xs text-slate-400">{{ c.arg_hint }}</span>
                <span class="truncate text-xs text-slate-500 dark:text-slate-400">{{ c.description }}</span>
              </button>
            </div>
            <input
              ref="inputRef"
              v-model="inputText"
              type="text"
              :placeholder="t('chat_placeholder')"
              :disabled="sending"
              class="h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
              @input="onSlashInput"
              @keydown="onInputKeydown"
              @blur="closeSlashDeferred"
            />
          </div>
          <button
            type="button"
            class="h-10 rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="sending"
            @click="sendMessage"
          >
            {{ sending ? t('chat_sending') : t('chat_send') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from '../composables/useNavigation';
import { slashProviderFor } from '../utils/chatProvider';
import { normalizeConversationTitle } from '../utils/chatTitle';
import { useAuthStore } from '../stores/auth';
import { getAgentSessions } from '../services/agentApi';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  isDirectoryLocked,
  recentProjectsFromSessions,
  sessionCreatePayload,
} from '../utils/chatSettings';

const STORAGE_KEY = 'aliang-chat-data';

const { t } = useI18n();
const { showDashboard, chatProviderPreset } = useNavigation();
const { isAuthenticated } = useAuthStore();

const conversations = ref([]);
const activeId = ref(null);
const inputText = ref('');
const sending = ref(false);
const messagesContainer = ref(null);
const inputRef = ref(null);
const showMobileSidebar = ref(false);

// --- 会话设置条（工作目录/模型/effort）状态 ---
const directoryPanelOpen = ref(false);
const customPathDraft = ref('');
const recentProjects = ref([]);
const recentProjectsLoaded = ref(false);
const modelCustomActive = ref(false);
const modelCustomDraft = ref('');
const createError = ref('');

// Local agent AI WebSocket: streams the local Claude Code / Codex headless run
// token-by-token and carries the running -> done status transitions.
let aiSocket = null;
let aiSocketPromise = null;
const handledApprovalIds = new Set();

const activeConversation = computed(() =>
  conversations.value.find(c => c.id === activeId.value) || null
);

const activeMessages = computed(() =>
  activeConversation.value?.messages || []
);

const activeApprovals = computed(() =>
  (activeConversation.value?.approvals || []).filter(a => a.status === 'pending' || a.status === 'responding')
);

// True while the active conversation's latest assistant message is still streaming.
const activeRunning = computed(() => {
  const msgs = activeMessages.value;
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') return msgs[i].status === 'running';
  }
  return false;
});

// --- 会话设置条逻辑 ---

const directoryLocked = computed(() => isDirectoryLocked(activeConversation.value));

const directoryLabel = computed(() => activeConversation.value?.projectPath || t('chat_pathDefault'));

const modelOptions = computed(() => AGENTCORE_MODEL_OPTIONS.map(o => ({
  ...o,
  label: o.value === '' ? t('chat_modelDefault')
    : o.value === MODEL_CUSTOM_VALUE ? t('chat_customModel')
    : o.label,
})));

// select 当前值：预设模型→自身；非预设自定义值→哨兵（输入框承接显示）。
const modelSelectValue = computed(() => {
  const m = activeConversation.value?.model || '';
  if (!m) return '';
  return isPresetModel(m) ? m : MODEL_CUSTOM_VALUE;
});

function isPresetModel(m) {
  return AGENTCORE_MODEL_OPTIONS.some(o => o.value === m && o.value !== MODEL_CUSTOM_VALUE);
}

// 统一写入口：落字段 → 持久化 → 清错误 → 服务端原位同步（掉线时下条消息兜底）。
function applySessionSettings(conv, patch) {
  if (activeRunning.value) return;
  Object.assign(conv, patch);
  conv.updatedAt = now();
  saveToStorage();
  createError.value = '';
  resendCreate(conv);
}

// 已建会话的设置变更要立即让服务端知道；socket 不在线就放弃本轮——
// sendMessage 现在每条消息前都重发 create，天然兜底，无需脏标记。
function resendCreate(conv) {
  if (!conv.aiSessionCreated) return;
  if (!aiSocket || aiSocket.readyState !== WebSocket.OPEN) return;
  sendAI(sessionCreatePayload(conv));
}

async function toggleDirectoryPanel() {
  const conv = activeConversation.value;
  if (!conv || directoryLocked.value || activeRunning.value) return;
  directoryPanelOpen.value = !directoryPanelOpen.value;
  if (directoryPanelOpen.value && !recentProjectsLoaded.value) {
    recentProjectsLoaded.value = true;
    try {
      const data = await getAgentSessions();
      recentProjects.value = recentProjectsFromSessions(data?.sessions);
    } catch (_) {
      recentProjects.value = []; // 拉取失败静默，手输路径不受影响
    }
  }
}

function pickDirectory(path) {
  const conv = activeConversation.value;
  if (!conv) return;
  customPathDraft.value = '';
  directoryPanelOpen.value = false;
  applySessionSettings(conv, { projectPath: path });
}

function commitCustomPath() {
  const conv = activeConversation.value;
  if (!conv) return;
  const path = customPathDraft.value.trim();
  if (!path) return;
  pickDirectory(path);
}

// IME 组合期的 Enter 是「确认候选词」，不是提交。preventDefault 必须放在
// 组合判定之后（模板 .prevent 会先于 handler 执行，把输入法自己的按键处理
// 一起吃掉）——与 onRenameEnter 同一套路。
function onCustomPathEnter(e) {
  if (isComposingEvent(e)) return;
  e.preventDefault();
  commitCustomPath();
}

function onModelSelect(value) {
  const conv = activeConversation.value;
  if (!conv || activeRunning.value) return;
  if (value === MODEL_CUSTOM_VALUE) {
    modelCustomDraft.value = conv.model && !isPresetModel(conv.model) ? conv.model : '';
    modelCustomActive.value = true;
    return;
  }
  modelCustomActive.value = false;
  applySessionSettings(conv, { model: value });
}

function commitCustomModel() {
  const conv = activeConversation.value;
  if (!conv) return;
  const model = modelCustomDraft.value.trim();
  modelCustomActive.value = false;
  if (!model) return; // 空输入视作取消，保持原值
  applySessionSettings(conv, { model });
}

function onCustomModelEnter(e) {
  if (isComposingEvent(e)) return;
  e.preventDefault();
  commitCustomModel();
}

// v-model 写法让 select 每次渲染都回写 el.value：取消自定义输入后选择框
// 立即弹回当前值，不会停留在已被丢弃的哨兵选项上（与 activeEffort 同构）。
const activeModel = computed({
  get: () => modelSelectValue.value,
  set: (v) => onModelSelect(v),
});

const activeEffort = computed({
  get: () => activeConversation.value?.effort || '',
  set(value) {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    applySessionSettings(conv, { effort: value });
  },
});

// 切会话收起面板、清一次性错误。
watch(activeId, () => {
  directoryPanelOpen.value = false;
  modelCustomActive.value = false;
  createError.value = '';
});

// create 确认门：sendMessage 发出 create 后等回执再发 ai.message——目录无效
// 时服务端拒绝建会话，绝不能让消息经 ai.message 的 lazy-create 落到 home 跑。
// 回执来源：ai.session.created → 'created'；create 期 ai.error（无 message_id）
// → 'error'（错误文本已由该分支渲染）；超时 → 'timeout'（socket 死亡等）。
const pendingCreates = new Map(); // session_id -> settle(status)

function waitForCreateAck(conv, timeoutMs = 5000) {
  return new Promise((resolve) => {
    const sid = conv.aiSessionId;
    let done = false;
    const settle = (status) => {
      if (done) return;
      done = true;
      pendingCreates.delete(sid);
      clearTimeout(timer);
      resolve(status);
    };
    const timer = setTimeout(() => settle('timeout'), timeoutMs);
    pendingCreates.set(sid, settle);
    conv.aiSessionCreated = true;
    // ensureAISocket 之后 socket 仍可能已死：sendAI 返回 false 就立刻按超时
    // 结算，不白等 5 秒死钟。
    if (!sendAI(sessionCreatePayload(conv))) settle('timeout');
  });
}

// --- Slash command autocomplete (provider-aware) ---
// The `/` popup is backed by slash.commands.list over the same AI stream socket
// the chat already uses. Commands are cached per provider (claude vs codex) so a
// claude conversation never surfaces codex's builtins and vice-versa.
const slashCommandsByProvider = ref({});
const slashOpen = ref(false);
const slashIndex = ref(0);
const slashPending = {};

function slashProviderOf(conv) {
  return slashProviderFor(conv?.provider);
}

const slashActiveProvider = computed(() => slashProviderOf(activeConversation.value));

// The in-progress command token (text after the leading `/`, before any space),
// or null when the input is not a slash invocation. An empty string means the
// user typed exactly `/` — show the full list.
function slashToken(text) {
  if (!text.startsWith('/')) return null;
  const rest = text.slice(1);
  if (/\s/.test(rest)) return null; // args started -> stop suggesting
  return rest.toLowerCase();
}

const slashMatches = computed(() => {
  if (!slashOpen.value) return [];
  const q = slashToken(inputText.value);
  if (q === null) return [];
  const list = slashCommandsByProvider.value[slashActiveProvider.value] || [];
  const filtered = q === '' ? list : list.filter(c => String(c.name || '').toLowerCase().startsWith(q));
  return filtered.slice(0, 20);
});

function generateId() {
  return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

function now() {
  return Date.now();
}

const groupedConversations = computed(() => {
  const todayStart = new Date();
  todayStart.setHours(0, 0, 0, 0);
  const todayTs = todayStart.getTime();

  const yesterdayStart = new Date(todayStart);
  yesterdayStart.setDate(yesterdayStart.getDate() - 1);
  const yesterdayTs = yesterdayStart.getTime();

  const today = [];
  const yesterday = [];
  const older = [];

  const sorted = [...conversations.value].sort((a, b) => b.updatedAt - a.updatedAt);
  for (const c of sorted) {
    if (c.updatedAt >= todayTs) {
      today.push(c);
    } else if (c.updatedAt >= yesterdayTs) {
      yesterday.push(c);
    } else {
      older.push(c);
    }
  }
  return { today, yesterday, older };
});

function loadFromStorage() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    if (data && Array.isArray(data.conversations)) {
      conversations.value = data.conversations.map(normalizeConversation).filter(Boolean);
      activeId.value = data.activeConversationId || null;
    }
  } catch (_) {
    // ignore corrupt data
  }
}

function saveToStorage() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      conversations: conversations.value.map(conversationForStorage),
      activeConversationId: activeId.value
    }));
  } catch (_) {
    // storage full or unavailable
  }
}

function conversationForStorage(conv) {
  const copy = { ...conv };
  copy.approvals = [];
  return copy;
}

function createConversation() {
  const conv = {
    id: generateId(),
    title: '',
    messages: [],
    approvals: [],
    provider: chatProviderPreset.value || 'claudecode',
    createdAt: now(),
    updatedAt: now()
  };
  conversations.value.unshift(conv);
  activeId.value = conv.id;
  showMobileSidebar.value = false;
  saveToStorage();
  nextTick(() => inputRef.value?.focus());
}

function selectConversation(id) {
  activeId.value = id;
  showMobileSidebar.value = false;
  saveToStorage();
  nextTick(() => scrollToBottom());
}

// --- 会话重命名（仅本地标题，不同步 native / 服务端） ---
const renamingId = ref('');
// 'list' | 'header'：同一会话在列表与顶栏各有一个可编辑位点，
// 必须靠它保证**同一时刻只渲染一个输入框**，否则键盘焦点会落到 DOM 里靠后的那个。
const renameSurface = ref('');
const renameDraft = ref('');
// v-for 内的 ref 会收集成数组，故用函数 ref 只记住当前那一个输入框。
let renameInputEl = null;

function startRename(conv, surface = 'list') {
  if (!conv?.id) return;
  // iOS Safari 点 <button> 不移动焦点，输入框收不到 blur，「失焦提交」不会触发；
  // 进新编辑位点前先主动提交上一个，避免共享的 renameDraft 冲掉在编文字。
  if (renamingId.value) commitRename();
  renamingId.value = conv.id;
  renameSurface.value = surface;
  renameDraft.value = conv.title || '';
  nextTick(() => {
    renameInputEl?.focus();
    renameInputEl?.select();
  });
}

function commitRename() {
  if (!renamingId.value) return;
  const conv = conversations.value.find(c => c.id === renamingId.value);
  if (conv) {
    conv.title = normalizeConversationTitle(renameDraft.value);
    saveToStorage(); // 刻意不动 updatedAt：改名不是内容更新，不该把会话顶到分组首位
  }
  cancelRename();
}

function cancelRename() {
  renamingId.value = '';
  renameSurface.value = '';
  renameDraft.value = '';
  renameInputEl = null;
}

// IME 组合期（中文/日文输入法）的 Enter/Esc 属于「确认候选词 / 取消组合」，
// Firefox/Safari 会如实上报 key，模板修饰符拦不住，必须自己判。
// 也不能靠模板 `.prevent`：withModifiers 会先 preventDefault 再进 handler，
// 那样会把输入法自己的按键处理一起吃掉。
function isComposingEvent(e) {
  return e.isComposing || e.keyCode === 229;
}

function onRenameEnter(e) {
  // 组合期既不 preventDefault（会吃掉输入法的确认候选词），
  // 也不能省 stopPropagation（会冒泡到行级 selectConversation / 文档级 close）。
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  commitRename();
}

function onRenameEscape(e) {
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  cancelRename();
}

function deleteConversation() {
  commitRename();
  if (!activeId.value) return;
  if (!confirm(t('chat_deleteConfirm'))) return;
  conversations.value = conversations.value.filter(c => c.id !== activeId.value);
  if (conversations.value.length > 0) {
    activeId.value = conversations.value[0].id;
  } else {
    activeId.value = null;
  }
  saveToStorage();
}

function scrollToBottom() {
  nextTick(() => {
    if (messagesContainer.value) {
      messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight;
    }
  });
}

// --- Local agent AI streaming over WebSocket ---

function aiSocketUrl() {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
  return `${proto}://${window.location.host}/api/agent/ai/stream`;
}

function ensureAISocket() {
  if (aiSocket && aiSocket.readyState === WebSocket.OPEN) return Promise.resolve();
  if (aiSocket && aiSocket.readyState === WebSocket.CONNECTING && aiSocketPromise) return aiSocketPromise;

  aiSocketPromise = new Promise((resolve, reject) => {
    let sock;
    try {
      sock = new WebSocket(aiSocketUrl());
    } catch (e) {
      aiSocketPromise = null;
      reject(e);
      return;
    }
    aiSocket = sock;
    sock.onopen = () => {
      console.log('[aliang-chat] ws open', aiSocketUrl());
      aiSocketPromise = null;
      resolve();
    };
    sock.onmessage = (ev) => {
      let parsed = null;
      try {
        parsed = JSON.parse(ev.data);
      } catch (_) {
        // ignore malformed frames
      }
      if (parsed && parsed.type) {
        console.log('[aliang-chat] frame', parsed.type, parsed.type === 'ai.delta' ? `len=${(parsed.delta || '').length}` : '');
      }
      handleAIEvent(parsed);
    };
    sock.onerror = (e) => {
      console.warn('[aliang-chat] ws error', e);
    };
    sock.onclose = (e) => {
      console.log('[aliang-chat] ws close code=', e.code, 'reason=', e.reason);
      if (aiSocket === sock) aiSocket = null;
      aiSocketPromise = null;
    };
  });
  return aiSocketPromise;
}

function sendAI(payload) {
  if (aiSocket && aiSocket.readyState === WebSocket.OPEN) {
    aiSocket.send(JSON.stringify(payload));
    return true;
  }
  return false;
}

// Ask the agent for the active provider's `/`-command surface. Cached per
// provider so we only round-trip once; a second call while pending is a no-op.
async function ensureSlashCommands() {
  const provider = slashActiveProvider.value;
  // agentcore 内核没有 claude/codex 的插件命令面。绝不发空 provider 请求：
  // 后端 normalizeSlashProvider 把空值当「未指定」并返回全部 provider 的命令，
  // 正是要避免的误导。
  if (!provider) return;
  if (slashCommandsByProvider.value[provider] || slashPending[provider]) return;
  slashPending[provider] = true;
  try {
    await ensureAISocket();
    sendAI({
      type: 'slash.commands.list',
      request_id: 'slash-' + provider,
      project_path: activeConversation.value?.projectPath || '',
      provider,
    });
  } catch (_) {
    // socket unavailable: leave uncached so a later retry can fill it in
    slashPending[provider] = false;
  }
}

function onSlashInput() {
  const token = slashToken(inputText.value);
  // 无命令面（agentcore）：`/` 按普通文本发送，不弹补全。
  if (token === null || !slashActiveProvider.value) {
    slashOpen.value = false;
    return;
  }
  ensureSlashCommands();
  slashOpen.value = true;
  slashIndex.value = 0;
}

function applySlash(cmd) {
  if (!cmd) return;
  const name = String(cmd.name || '').trim();
  if (!name) return;
  // Insert `/name ` and let the user append arguments. (For Codex the app-server
  // does not execute slash commands, so this is the most we can do — the token is
  // forwarded as text. For Claude the CLI likewise receives it as a prompt.)
  inputText.value = '/' + name + ' ';
  slashOpen.value = false;
  nextTick(() => inputRef.value?.focus());
}

// Close the popup shortly after the input loses focus so a click on a popup
// item still registers (mousedown fires before blur).
function closeSlashDeferred() {
  setTimeout(() => { slashOpen.value = false; }, 150);
}

// Unified keydown so the popup can intercept arrows/enter/tab/escape before the
// send-on-enter binding fires.
function onInputKeydown(e) {
  const matches = slashMatches.value;
  if (slashOpen.value && matches.length) {
    if (e.key === 'ArrowDown') {
      slashIndex.value = (slashIndex.value + 1) % matches.length;
      e.preventDefault();
      return;
    }
    if (e.key === 'ArrowUp') {
      slashIndex.value = (slashIndex.value - 1 + matches.length) % matches.length;
      e.preventDefault();
      return;
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      applySlash(matches[slashIndex.value] || matches[0]);
      e.preventDefault();
      return;
    }
    if (e.key === 'Escape') {
      slashOpen.value = false;
      e.preventDefault();
      return;
    }
  }
  if (e.key === 'Enter') {
    e.preventDefault();
    sendMessage();
  }
}

function findRunningAssistant(conv) {
  const msgs = conv.messages || [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
  }
  return null;
}

// Returns the live assistant bubble for a streaming run, creating a fresh
// placeholder if sendMessage's placeholder was lost (e.g. page reloaded mid-run
// holding a stale session id, or a state desync). Never spawns a stray bubble
// after the turn already finished (done/error) — late deltas are then ignored.
function ensureAssistantBubble(conv) {
  const asst = findRunningAssistant(conv);
  if (asst) return asst;
  const msgs = conv.messages || [];
  const last = msgs[msgs.length - 1];
  if (last && last.role === 'assistant' && (last.status === 'done' || last.status === 'error')) {
    return null;
  }
  const fresh = { role: 'assistant', content: '', status: 'running', ts: now() };
  conv.messages.push(fresh);
  return fresh;
}

function normalizeConversation(conv) {
  if (!conv || typeof conv !== 'object') return null;
  if (!Array.isArray(conv.messages)) conv.messages = [];
  conv.approvals = [];
  return conv;
}

function approvalKeyFromValues(sessionId, approvalId) {
  return `${sessionId || ''}:${approvalId || ''}`;
}

function approvalKey(approval) {
  return approvalKeyFromValues(approval?.sessionId, approval?.id);
}

function ensureApprovalList(conv) {
  if (!Array.isArray(conv.approvals)) conv.approvals = [];
  return conv.approvals;
}

function normalizeApprovalDecision(value) {
  const lower = String(value || '').replace(/[-\s]/g, '_').toLowerCase();
  if (['accept', 'accepted', 'approve', 'approved', 'allow', 'allowed', 'yes'].includes(lower)) return 'accept';
  if (['accept_for_session', 'acceptforsession', 'approved_for_session', 'approve_for_session', 'allow_for_session'].includes(lower)) return 'accept_for_session';
  if (['decline', 'declined', 'deny', 'denied', 'reject', 'rejected', 'no'].includes(lower)) return 'decline';
  if (['cancel', 'abort', 'timed_out', 'timeout'].includes(lower)) return 'cancel';
  return '';
}

function normalizeApprovalDecisions(values) {
  const input = Array.isArray(values) ? values : [];
  const out = [];
  for (const item of input) {
    const normalized = normalizeApprovalDecision(item);
    if (normalized && !out.includes(normalized)) out.push(normalized);
  }
  return out.length ? out : ['accept', 'decline', 'cancel'];
}

function removeApproval(conv, key) {
  if (!conv || !key || !Array.isArray(conv.approvals)) return;
  conv.approvals = conv.approvals.filter(item => item.key !== key);
}

function markApprovalHandled(conv, key) {
  if (!key) return;
  handledApprovalIds.add(key);
  removeApproval(conv, key);
}

function clearApprovalsForSession(conv, sessionId) {
  if (!conv || !Array.isArray(conv.approvals)) return;
  const keep = [];
  for (const item of conv.approvals) {
    if (!sessionId || item.sessionId === sessionId) {
      handledApprovalIds.add(item.key);
    } else {
      keep.push(item);
    }
  }
  conv.approvals = keep;
}

function upsertApproval(conv, event) {
  const id = event.approval_id || event.id;
  const sessionId = event.session_id || conv.aiSessionId || '';
  const key = approvalKeyFromValues(sessionId, id);
  if (!id || handledApprovalIds.has(key)) return;
  if (event.status && event.status !== 'pending') {
    markApprovalHandled(conv, key);
    return;
  }

  const approvals = ensureApprovalList(conv);
  const existing = approvals.find(item => item.key === key);
  const next = {
    ...(existing || {}),
    key,
    id,
    sessionId,
    messageId: event.message_id || existing?.messageId || '',
    provider: event.provider || existing?.provider || '',
    kind: event.kind || existing?.kind || 'tool',
    status: existing?.status === 'responding' ? 'responding' : 'pending',
    title: event.title || existing?.title || '',
    reason: event.reason || existing?.reason || '',
    command: event.command || existing?.command || '',
    cwd: event.cwd || existing?.cwd || '',
    toolName: event.tool_name || event.toolName || existing?.toolName || '',
    toolInput: event.tool_input ?? event.toolInput ?? existing?.toolInput ?? null,
    fileChanges: event.file_changes ?? event.fileChanges ?? existing?.fileChanges ?? null,
    availableDecisions: normalizeApprovalDecisions(event.available_decisions || event.availableDecisions || existing?.availableDecisions),
    error: '',
    updatedAt: now()
  };
  if (existing) {
    Object.assign(existing, next);
  } else {
    approvals.push(next);
  }
  conv.updatedAt = now();
  scrollToBottom();
}

function serializeApprovalValue(value) {
  if (value === null || value === undefined || value === '') return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return String(value);
  }
}

function approvalDetailText(approval) {
  return serializeApprovalValue(approval?.toolInput) || serializeApprovalValue(approval?.fileChanges);
}

function approvalAllows(approval, decision) {
  const normalized = normalizeApprovalDecision(decision);
  return normalizeApprovalDecisions(approval?.availableDecisions).includes(normalized);
}

function approvalIcon(approval) {
  switch (approval?.kind) {
    case 'command':
      return 'terminal';
    case 'file_change':
      return 'edit_document';
    case 'permissions':
      return 'lock';
    default:
      return 'rule';
  }
}

async function respondApproval(conv, approval, decision) {
  if (!conv || !approval || approval.status !== 'pending') return;
  const normalized = normalizeApprovalDecision(decision);
  if (!normalized) return;

  const key = approvalKey(approval);
  if (handledApprovalIds.has(key)) {
    removeApproval(conv, key);
    return;
  }

  approval.status = 'responding';
  approval.error = '';
  approval.updatedAt = now();
  saveToStorage();

  try {
    await ensureAISocket();
    const ok = sendAI({
      type: 'ai.approval.response',
      session_id: approval.sessionId || conv.aiSessionId,
      message_id: approval.messageId || '',
      approval_id: approval.id,
      decision: normalized,
      scope: normalized === 'accept_for_session' ? 'session' : 'turn'
    });
    if (!ok) throw new Error('socket-closed');
    markApprovalHandled(conv, key);
    saveToStorage();
  } catch (_) {
    if (!handledApprovalIds.has(key)) {
      approval.status = 'pending';
      approval.error = t('chat_approvalSendFailed');
      approval.updatedAt = now();
      saveToStorage();
    }
  }
}

function finishRun(conv) {
  conv.updatedAt = now();
  if (activeId.value === conv.id) sending.value = false;
  saveToStorage();
}

function handleAIEvent(e) {
  if (!e || !e.type) return;

  // Slash-command discovery results are not tied to a conversation session; cache
  // them by provider before the conversation-targeting logic below.
  if (e.type === 'slash.commands.list.result') {
    const provider = e.provider === 'codex' ? 'codex' : 'claude';
    slashPending[provider] = false;
    slashCommandsByProvider.value = {
      ...slashCommandsByProvider.value,
      [provider]: Array.isArray(e.commands) ? e.commands : [],
    };
    return;
  }
  if (e.type === 'slash.commands.list.error') {
    const provider = e.provider === 'codex' ? 'codex' : 'claude';
    slashPending[provider] = false;
    slashCommandsByProvider.value = { ...slashCommandsByProvider.value, [provider]: [] };
    return;
  }

  let conv = e.session_id ? conversations.value.find(c => c.aiSessionId === e.session_id) : null;
  // session_id 是否命中决定事件归属；matchedById=false 的事件走 fallback 仅为
  // 让流仍能渲染，绝不能成为回显/dirLocked 的写入目标（会话已删或他页会话的
  // 迟到 create 回执会污染当前活跃会话）。
  const matchedById = !!conv;
  // Fallback: a run started from this page always targets the active
  // conversation's live assistant bubble. If the session id didn't match
  // (e.g. reloaded localStorage holding a stale id, or an id mismatch), route
  // to the active conversation so the stream still renders instead of being
  // silently dropped.
  if (!conv && activeConversation.value) {
    conv = activeConversation.value;
  }
  if (!conv) {
    console.debug('[chat] ai event has no target conversation', e);
    return;
  }

  // Lightweight trace so a streaming/render issue can be localized from the
  // browser console (event type + session match). Delta text is intentionally
  // omitted to keep the log readable.
  console.log('[aliang-chat] ai event', e.type, 'session=', e.session_id, 'conv=', conv.id);

  let persist = false;
  switch (e.type) {
    case 'ai.session.created': {
      const pending = pendingCreates.get(e.session_id);
      if (!matchedById) {
        // 未按 session_id 命中（会话已删/他页会话的迟到回执）：只结算确认门，
        // 不做任何 conv 写入——fallback 命中的活跃会话不能被污染。
        if (pending) pending('created');
        return;
      }
      conv.aiReady = true;
      // 目录锁定以服务端确认为准：create 成功后不可再改（create 失败保持可改）。
      conv.dirLocked = true;
      // 回显是服务端清洗后的权威值：project_path 已是解析后的绝对路径。
      if (typeof e.project_path === 'string' && e.project_path) conv.projectPath = e.project_path;
      if (typeof e.model === 'string') conv.model = e.model;
      if (typeof e.effort === 'string') conv.effort = e.effort;
      if (pending) pending('created');
      persist = true;
      break;
    }
    case 'ai.run.started': {
      const asst = ensureAssistantBubble(conv);
      if (asst) asst.status = 'running';
      break;
    }
    case 'ai.delta': {
      const asst = ensureAssistantBubble(conv);
      if (!asst) break;
      asst.content += e.delta || '';
      console.log('[aliang-chat] rendered delta, contentLen=', asst.content.length, 'conv=', conv.id);
      scrollToBottom();
      break;
    }
    case 'ai.status': {
      const asst = findRunningAssistant(conv);
      if (asst && !asst.content && e.status) asst.content = e.status;
      persist = true;
      break;
    }
    case 'ai.approval.request': {
      upsertApproval(conv, e);
      persist = true;
      break;
    }
    case 'ai.done': {
      const asst = findRunningAssistant(conv);
      if (asst) asst.status = 'done';
      clearApprovalsForSession(conv, e.session_id);
      finishRun(conv);
      return;
    }
    case 'ai.error': {
      const asst = findRunningAssistant(conv);
      if (!e.message_id) {
        // create 阶段失败（典型：手输目录不存在）。亮在设置条上、结算确认门；
        // 不进消息流。目录未锁定（dirLocked 只在回显成功时置位），纠正路径后
        // 下一条消息重发 create 即成功。判定只用 message_id 单条件：运行期
        // 所有 ai.error 出口都带 message_id，create 期全不带；「无 running 气泡」
        // 不可作条件——sendMessage 在发 create 前已推入占位气泡。
        if (matchedById && conv.id === activeId.value) createError.value = e.error || t('chat_createFailed');
        const pending = pendingCreates.get(e.session_id);
        if (pending) pending('error');
        saveToStorage();
        return;
      }
      if (asst) {
        asst.status = 'error';
        if (!asst.content) asst.content = e.error || t('chat_aiError');
      }
      clearApprovalsForSession(conv, e.session_id);
      finishRun(conv);
      return;
    }
    default:
      return;
  }
  if (persist) saveToStorage();
}

async function sendMessage() {
  if (!isAuthenticated.value) {
    if (activeConversation.value) {
      activeConversation.value.messages.push({ role: 'assistant', content: t('chat_loginRequired'), status: 'done', ts: now() });
    }
    return;
  }

  const text = inputText.value.trim();
  if (!text || sending.value || !activeId.value) return;

  const conv = activeConversation.value;
  if (!conv) return;

  conv.messages.push({ role: 'user', content: text, ts: now() });
  if (!conv.title) {
    conv.title = text.slice(0, 20);
  }
  conv.updatedAt = now();
  inputText.value = '';
  saveToStorage();
  scrollToBottom();

  // Live assistant placeholder: its content fills in token-by-token as
  // ai.delta frames arrive, and its status drives the running -> done UI.
  const assistantMsg = { role: 'assistant', content: '', status: 'running', ts: now() };
  conv.messages.push(assistantMsg);
  scrollToBottom();

  sending.value = true;
  if (!conv.aiSessionId) conv.aiSessionId = 'local-' + conv.id;
  const messageId = generateId();

  try {
    await ensureAISocket();
    createError.value = '';
    const ack = await waitForCreateAck(conv);
    if (ack !== 'created') {
      // create 被拒或无回执：内联错误已由 ai.error 分支渲染（timeout 无帧，
      // 这里兜底提示）；撤掉空占位气泡防空转；不发 ai.message。
      const asst = findRunningAssistant(conv);
      if (asst && !asst.content) conv.messages = conv.messages.filter(m => m !== asst);
      // 与 ai.error 分支同约定：只在发送会话仍是当前会话时落兜底提示。
      if (conv.id === activeId.value && !createError.value) createError.value = t('chat_serviceUnavailable');
      sending.value = false; // 本函数的 sending=false 在 catch 尾部，早退须自理
      saveToStorage();
      return;
    }
    const ok = sendAI({
      type: 'ai.message',
      session_id: conv.aiSessionId,
      message_id: messageId,
      content: text,
    });
    if (!ok) throw new Error('socket-closed');
  } catch (_) {
    const asst = findRunningAssistant(conv);
    if (asst) {
      asst.status = 'error';
      asst.content = t('chat_serviceUnavailable');
    }
    sending.value = false;
    saveToStorage();
  }
}

function close() {
  // iOS Safari 上点关闭不会触发输入框 blur，先把在编的标题落地再卸载页面。
  commitRename();
  showDashboard();
}

function handleKeydown(e) {
  if (e.key === 'Escape') {
    close();
  }
}

onMounted(() => {
  console.log('[aliang-chat] ChatPage mounted build=stream-v3 ws=' + aiSocketUrl());
  loadFromStorage();
  document.addEventListener('keydown', handleKeydown);
});

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown);
  if (aiSocket) {
    try { aiSocket.close(); } catch (_) {}
    aiSocket = null;
  }
});
</script>

<style scoped>
.ai-thinking {
  display: inline-flex;
  align-items: center;
}

.thinking-dot {
  display: inline-block;
  width: 5px;
  height: 5px;
  margin: 0 1px;
  border-radius: 9999px;
  background-color: rgb(148 163 184);
  animation: thinking-bounce 1.2s infinite ease-in-out both;
}

.thinking-dot:nth-child(2) { animation-delay: 0.15s; }
.thinking-dot:nth-child(3) { animation-delay: 0.3s; }

@keyframes thinking-bounce {
  0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
  40% { transform: scale(1); opacity: 1; }
}
</style>
