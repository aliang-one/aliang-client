<template>
  <!--
    文件浏览浮窗(P3.4+P3.5+P3.6,spec §7.4):FloatingWindow 壳(缺省 860×540,
    width/height props 化供工作台最大化取满幅)+顶部上传工具条+双栏网格——左
    240px FileTree / 右导航双视图。几何 props 与五个窗事件(move/minimize/
    maximize-toggle/close/focus)全透传:编排(注册表/桌面几何)归上层,与
    FloatingWindow 的「壳不管状态」分工一致,本组件只加「文件浏览内容」。
    store 拍板:每窗一份 createFileBrowserStore(P2.4 per-window 架构——多个
    文件浏览窗各自独立展开态/缓存/选中,互不串扰);树与预览共享同一实例
    (selected 同源),令牌经 computed 逐调用读最新值(滑动续期友好)。
    导航补全批(2026-11-22 文件管理计划 Task A):右栏改双视图——顶部
    FileBreadcrumb(段跳转=cd、「↑」=cdUp)+列表/预览二态:selected=文件→
    FilePreview(顶加「← 返回列表」清 selected);null/目录→FileListView
    当前目录条目(目录行点击=cd 进入)。拍板(钉死):左树目录点击仍只
    toggleNode 展开不切右栏列表——树=全局导航、列表=当前目录,双视图语义
    独立不打架;右栏列表的「进入」才切 currentDir,面包屑/↑ 同源。
    上传(P3.6,导航补全批改):目标=当前目录 currentDir(「在哪一层,上传落
    哪一层」;引导前兜底 root/空串=Go 缺省守卫根);fsUpload 成功→
    refreshNode(目标目录)强刷+选中新落盘文件(终名含冲突时间戳,以回包
    path 为准);失败→工具条错误行。uploading 态钮禁用+spinner 文案。
    文件操作批(2026-11-22 文件管理计划 Task C):工具条扩「新建文件夹/
    重命名/删除」。mkdir 目标=规范 currentDir(空串传 '.',服务端同款缺省);
    rename/delete 作用域=选中项 selected(文件=列表/树行点击选中,目录=树行
    点击选中——列表目录行点击是「进入」,选中语义在树侧)。删除=双段确认
    弹窗(浮窗内 absolute overlay):段1 意图确认(目录附递归红警,保守判定
    type==='dir' 即递归语义提示);段2 仅目录——输入条目名完全匹配才亮
    「确认删除」(防误删最硬一档);文件段1 即可执行。文件名消毒全权在服务
    端(前端不重复消毒,避免双重转义 UX);错误(重名/非法名/权限/不存在)
    直显服务器 msg 不吞。成功统一 refreshNode(父目录)+选中清理+旧子树
    缓存作废;若 currentDir 指进被删/改名子树则退回父目录(已刷新即所见)。
    壳体 body 恒深色底(FloatingWindow 中性底),内容区自带白/深底。
  -->
  <FloatingWindow
    :title="t('fs_title')"
    :x="x"
    :y="y"
    :width="width"
    :height="height"
    :z-index="zIndex"
    :active="active"
    :minimized="minimized"
    @move="(wx, wy) => emit('move', wx, wy)"
    @minimize="emit('minimize')"
    @maximize-toggle="emit('maximize-toggle')"
    @close="emit('close')"
    @focus="emit('focus')"
  >
    <div
      data-testid="file-browser-body"
      class="relative flex h-full min-h-0 flex-col overflow-hidden bg-white text-slate-900 dark:bg-slate-900 dark:text-slate-100"
    >
      <!-- 上传+文件操作工具条:上传目标=当前目录 currentDir(导航补全批改;引导前
           守卫根);文件操作(Task C)新建文件夹/重命名/删除;hidden file input 承接选档 -->
      <div
        data-testid="fs-upload-bar"
        class="flex shrink-0 items-center gap-2 border-b border-slate-200 bg-slate-50 px-3 py-1.5 dark:border-slate-700 dark:bg-slate-800/60"
      >
        <button
          data-testid="fs-upload-button"
          type="button"
          class="shrink-0 rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          :disabled="uploading || opBusy"
          @click="pickFile"
        >{{ t('fs_upload') }}</button>
        <button
          data-testid="file-op-mkdir"
          type="button"
          class="shrink-0 rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          :disabled="uploading || opBusy"
          @click="openMkdir"
        >{{ t('fs_mkdir') }}</button>
        <button
          data-testid="file-op-rename"
          type="button"
          class="shrink-0 rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          :disabled="!selectedEntry || uploading || opBusy"
          @click="openRename"
        >{{ t('fs_rename') }}</button>
        <button
          data-testid="file-op-delete"
          type="button"
          class="shrink-0 rounded border border-red-300 px-2 py-0.5 text-[11px] text-red-600 transition hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-red-700 dark:text-red-400 dark:hover:bg-red-500/10"
          :disabled="!selectedEntry || uploading || opBusy"
          @click="openDelete"
        >{{ t('fs_delete') }}</button>
        <!-- 行内命名输入(新建文件夹/重命名共用一行二态;确认=✓/回车,取消=✗/Esc;
             失败保留输入可改后重试,成功即收起) -->
        <span v-if="inlineOp" class="flex min-w-0 items-center gap-1">
          <input
            ref="inlineInputEl"
            v-model="inlineName"
            data-testid="file-op-input"
            type="text"
            class="w-36 rounded border border-slate-300 bg-white px-1.5 py-0.5 text-[11px] text-slate-900 outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100"
            :placeholder="inlineOp === 'mkdir' ? t('fs_mkdir') : t('fs_rename')"
            @keyup.enter="confirmInline"
            @keydown.esc="closeInline"
          />
          <button
            data-testid="file-op-input-confirm"
            type="button"
            class="shrink-0 rounded border border-slate-300 px-1.5 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
            :disabled="!inlineName.trim() || opBusy"
            @click="confirmInline"
          >✓</button>
          <button
            data-testid="file-op-input-cancel"
            type="button"
            class="shrink-0 rounded border border-slate-300 px-1.5 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
            :disabled="opBusy"
            @click="closeInline"
          >✗</button>
        </span>
        <span v-if="uploading" data-testid="fs-upload-uploading" class="flex min-w-0 items-center gap-1.5 text-[11px] text-slate-500 dark:text-slate-400">
          <span class="inline-block size-3 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-primary"></span>
          <span class="truncate">{{ t('fs_uploading') }}</span>
        </span>
        <span
          v-else-if="uploadError"
          data-testid="fs-upload-error"
          class="min-w-0 flex-1 truncate text-[11px] text-red-500 dark:text-red-400"
          :title="uploadError"
        >{{ t('fs_upload_error') }}：{{ uploadError }}</span>
        <!-- 文件操作错误行(Task C):服务端 msg 直显;删除弹窗开着时错误在弹窗内呈现 -->
        <span
          v-if="opError && !deleteDialog"
          data-testid="file-op-error"
          class="min-w-0 flex-1 truncate text-[11px] text-red-500 dark:text-red-400"
          :title="opError"
        >{{ t('fs_op_failed') }}：{{ opError }}</span>
        <input
          ref="fileInputEl"
          data-testid="fs-upload-input"
          type="file"
          class="hidden"
          @change="onFileChosen"
        />
      </div>
      <!-- 双栏:左树 240px / 右导航双视图 -->
      <div class="grid min-h-0 min-w-0 flex-1 grid-cols-[240px_minmax(0,1fr)] overflow-hidden">
        <!-- 左栏:目录树(240px 固定,栏内纵向滚动;拍板:目录点击只展开不切
             右栏列表——树=全局导航,列表=当前目录,双视图独立) -->
        <div class="min-h-0 min-w-0 overflow-hidden border-r border-slate-200 dark:border-slate-700">
          <FileTree :store="browser" />
        </div>
        <!-- 右栏:顶部面包屑 + 列表/预览二态 -->
        <div class="flex min-h-0 min-w-0 flex-col">
          <FileBreadcrumb
            :segments="browser.breadcrumbSegments.value"
            :current-path="browser.currentDir.value"
            @jump="navigate"
            @up="goUp"
          />
          <!-- cd 失败呈现:横幅(不跳转,列表/面包屑维持原位;失败未落缓存,
               再点目录行即重试) -->
          <div
            v-if="navError"
            data-testid="fs-nav-error"
            class="shrink-0 border-b border-red-200 bg-red-50 px-3 py-1.5 text-xs text-red-600 dark:border-red-800 dark:bg-red-500/10 dark:text-red-400"
          >{{ t('fs_load_error') }}：{{ navError }}</div>
          <!-- 二态:选中文件→预览(顶加「← 返回列表」);null/目录→当前目录列表 -->
          <div v-if="showPreview" class="flex min-h-0 flex-1 flex-col">
            <button
              data-testid="fs-back-to-list"
              type="button"
              class="shrink-0 border-b border-slate-200 bg-slate-50 px-3 py-1 text-left text-[11px] text-slate-600 transition hover:bg-slate-100 dark:border-slate-700 dark:bg-slate-800/60 dark:text-slate-300 dark:hover:bg-slate-800"
              @click="browser.selectNode(null)"
            >← {{ t('fs_back_to_list') }}</button>
            <div class="min-h-0 flex-1">
              <FilePreview :entry="browser.selected.value" :token="token" />
            </div>
          </div>
          <div v-else class="min-h-0 flex-1">
            <FileListView
              :entries="currentEntries"
              :current-path="browser.currentDir.value"
              :loading="listLoading"
              @open="openEntry"
              @select="selectEntry"
            />
          </div>
        </div>
      </div>
      <!-- 删除双段确认弹窗(Task C):段1 意图确认(目录附递归红警——保守判定
           type==='dir' 即按递归语义提示,children 缓存未知/非空同等对待);段2 仅
           目录递归删——输入条目名完全匹配才亮「确认删除」(防误删最硬一档);
           文件删除段1 即可执行(同 testid file-confirm-submit 直删)。浮窗内
           absolute overlay 盖住整窗(工具条亦不可点),阶段3 无需全局 modal。 -->
      <div
        v-if="deleteDialog"
        data-testid="file-confirm-dialog"
        class="absolute inset-0 z-20 flex items-center justify-center bg-slate-900/50 p-4"
      >
        <div class="w-full max-w-sm rounded-lg border border-slate-200 bg-white p-4 shadow-xl dark:border-slate-700 dark:bg-slate-800">
          <div class="mb-2 text-sm font-medium text-slate-900 dark:text-slate-100">{{ t('fs_delete_confirm_title') }}</div>
          <div class="break-all text-xs text-slate-600 dark:text-slate-300">{{ t('fs_delete') }}「{{ deleteTarget?.name }}」?</div>
          <div
            v-if="deleteRecursive"
            data-testid="file-confirm-recursive-warn"
            class="mt-2 rounded border border-red-300 bg-red-50 px-2 py-1.5 text-xs text-red-600 dark:border-red-800 dark:bg-red-500/10 dark:text-red-400"
          >{{ t('fs_delete_recursive_warn') }}</div>
          <!-- 段2:名字匹配(仅目录递归删);不匹配=按钮禁用+红字提示 -->
          <div v-if="deleteStage === 2" class="mt-3">
            <div class="mb-1 text-xs text-slate-600 dark:text-slate-300">{{ t('fs_delete_type_name') }}</div>
            <input
              ref="deleteInputEl"
              v-model="deleteInput"
              data-testid="file-confirm-input"
              type="text"
              class="w-full rounded border border-slate-300 bg-white px-2 py-1 text-xs text-slate-900 outline-none focus:border-primary dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100"
              @keyup.enter="deleteMatched && submitDelete()"
            />
            <div
              v-if="deleteInput && !deleteMatched"
              data-testid="file-confirm-mismatch"
              class="mt-1 text-[11px] text-red-500 dark:text-red-400"
            >{{ t('fs_mismatch') }}</div>
          </div>
          <!-- 失败直显(权限/不存在):弹窗保留,原因可见 -->
          <div v-if="opError" data-testid="file-op-error-dialog" class="mt-2 break-all text-[11px] text-red-500 dark:text-red-400">{{ opError }}</div>
          <div class="mt-3 flex justify-end gap-2">
            <button
              data-testid="file-confirm-cancel"
              type="button"
              class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="opBusy"
              @click="cancelDelete"
            >{{ t('fs_cancel') }}</button>
            <!-- 段1 目录=「继续」进段2;文件/段2=「确认删除」执行(同一 testid) -->
            <button
              v-if="deleteStage === 1 && deleteRecursive"
              data-testid="file-confirm-continue"
              type="button"
              class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 transition hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-red-700 dark:text-red-400 dark:hover:bg-red-500/10"
              :disabled="opBusy"
              @click="continueDelete"
            >{{ t('fs_continue') }}</button>
            <button
              v-else
              data-testid="file-confirm-submit"
              type="button"
              class="rounded border border-red-400 bg-red-500 px-3 py-1 text-xs text-white transition hover:bg-red-600 disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="(deleteStage === 2 && !deleteMatched) || opBusy"
              @click="submitDelete"
            >{{ t('fs_delete_submit') }}</button>
          </div>
        </div>
      </div>
    </div>
  </FloatingWindow>
</template>

<script setup>
import { computed, nextTick, ref } from 'vue';
import { useI18n } from '../../i18n';
import { createFileBrowserStore, parentPathOf } from '../../composables/useFileBrowser';
import { fsDelete, fsMkdir, fsRename, fsUpload } from '../../services/agentApi';
import FloatingWindow from './FloatingWindow.vue';
import FileTree from './FileTree.vue';
import FilePreview from './FilePreview.vue';
import FileBreadcrumb from './FileBreadcrumb.vue';
import FileListView from './FileListView.vue';

const props = defineProps({
  // 保险箱令牌(逐调用读最新值;空串=未解锁,请求将被拒并落错误行)
  token: { type: String, default: '' },
  // 几何全由父层持有(注册表/桌面编排),本组件透传给壳——与 TerminalWorkspace
  // 给 FloatingWindow 的绑定形态一致;P3.6 起 width/height 亦 props 化(默认
  // 860×540 保持 P3.5 契约),工作台最大化时传满幅量纲
  x: { type: Number, default: 80 },
  y: { type: Number, default: 80 },
  width: { type: Number, default: 860 },
  height: { type: Number, default: 540 },
  zIndex: { type: Number, default: 1 },
  active: { type: Boolean, default: false },
  minimized: { type: Boolean, default: false },
});

// 五个窗事件 1:1 透传(minimize→注册表、maximize-toggle/close/move→父层
// 写回几何、focus→focusWindow;同 FloatingWindow emit 契约)
const emit = defineEmits(['move', 'minimize', 'maximize-toggle', 'close', 'focus']);

const { t } = useI18n();

// 每窗一份 store:tokenRef 用 computed 包 props.token(逐调用读 .value,
// 15 分钟滑动续期后自动携带新值)。树挂载时自行 setRoot 引导(P3.3 契约)。
const browser = createFileBrowserStore(computed(() => props.token));

// —— 右栏导航(导航补全批)——
// cd 失败呈现(评审 Fix 3b 改派生):记失败目标 path,横幅文案派生自 store
// errors[该 path]——失败路径之后无论经哪条路(树展开重试/再点目录行/别处
// 导航)加载成功,listDir 清掉 errors[path] 横幅即自动退场,窗层不留第二份
// 错误态。
const navFailedPath = ref('');
const navError = computed(() => (navFailedPath.value ? browser.errors.value[navFailedPath.value] || '' : ''));

// 列表视图数据:当前目录条目/拉取态(cd 成功即落 children[currentDir] 缓存;
// 引导中 currentDir='' 走 loading[''],引导失败列表空、错误已在树根行呈现)
const currentEntries = computed(() => browser.children.value[browser.currentDir.value] || []);
const listLoading = computed(() => !!browser.loading.value[browser.currentDir.value]);

// 二态:selected=文件/符号链接→预览(symlink 目标由 fsRead 服务端解析,评审
// Fix 2:只认 'file' 会让 symlink 行选中静默无操作);null/目录→列表(目录
// 选中来自树行点击——拍板:树目录点击只展开不切列表,见头注)
const showPreview = computed(() => {
  const sel = browser.selected.value;
  return !!sel && (sel.type === 'file' || sel.type === 'symlink');
});

// 导航统一入口(面包屑段跳转/列表「进入」共用):成功清失败标记(横幅随派
// 生面退场);失败记目标 path、currentDir 不动(cd 内部不抛,ok=null 即失败面)
async function navigate(path) {
  navFailedPath.value = '';
  const ok = await browser.cd(path);
  if (!ok) navFailedPath.value = path;
}

// 返回上一层:根 guard(↑ 钮根时已禁用,此处双保险);parent=POSIX 去尾段
function goUp() {
  if (!browser.currentDir.value || browser.currentDir.value === browser.root.value) return;
  void navigate(parentPathOf(browser.currentDir.value));
}

// 列表「进入目录」:目标=currentDir+name(canonical 拼接,与树侧同款)
function openEntry(entry) {
  if (entry?.type !== 'dir' || !entry?.name) return;
  void navigate(joinPath(browser.currentDir.value, entry.name));
}

// 列表选中文件/符号链接→预览(selected 与树选中同源;「← 返回列表」清回)
function selectEntry(entry) {
  if (!entry?.name) return;
  browser.selectNode({
    path: joinPath(browser.currentDir.value, entry.name),
    type: entry.type,
    name: entry.name,
  });
}

// 子路径拼接:根 '/' 特判避免 '//x'(与 FileTree/FileTreeNode 同款本地纯函数)
function joinPath(parent, name) {
  if (!parent) return name;
  if (parent === '/') return `/${name}`;
  return parent.endsWith('/') ? `${parent}${name}` : `${parent}/${name}`;
}

// —— 上传(P3.6,导航补全批改)——
const uploading = ref(false);
const uploadError = ref('');
const fileInputEl = ref(null);

// 上传目标目录(导航补全批改拍板):随 currentDir——右栏列表「在哪一层,
// 上传就落哪一层」;引导前兜底 root/空串(Go 侧缺省守卫根,语义一致)。
// 原 P3.6「选中目录+粘滞目录」语义由 currentDir 取代:选中上传后会移到新
// 文件,粘滞天然成立(currentDir 不随选中变),不再需要 lastUploadDir。
const uploadDir = computed(() => browser.currentDir.value || browser.root.value || '');

function pickFile() {
  if (uploading.value) return;
  uploadError.value = '';
  fileInputEl.value?.click();
}

async function onFileChosen(e) {
  const file = e.target.files && e.target.files[0];
  e.target.value = ''; // 清选档:同名文件可连传
  if (!file) return;
  const dir = uploadDir.value;
  uploading.value = true;
  uploadError.value = '';
  try {
    const data = await fsUpload(dir, file, props.token);
    // 强刷目标目录(=currentDir,展开态重拉)再选中新文件,终名以回包 path
    // 为准(同名冲突 Go 侧已追加时间戳);currentDir 不随选中变,连传天然
    // 粘滞在同一目录
    await browser.refreshNode(dir);
    const finalPath = typeof data?.path === 'string' ? data.path : '';
    if (finalPath) {
      browser.selectNode({
        path: finalPath,
        type: 'file',
        name: finalPath.split('/').pop() || finalPath,
      });
    }
  } catch (err) {
    uploadError.value = err?.message || String(err);
  } finally {
    uploading.value = false;
  }
}

// —— 文件操作(Task C:新建文件夹/重命名/删除)——
// 错误纪律:失败直显服务端 msg(重名 409/非法名 400/不存在 404/权限 102 全在
// 服务端文案里),不吞不改;行内输入失败保留(改个名字即可重试),删除弹窗
// 失败保留(原因可见)。文件名消毒全权在服务端(agentFSSanitizeFilename),
// 前端只做 trim + 非空闸,不重复消毒(避免双重转义 UX)。
const opBusy = ref(false);
const opError = ref('');
const inlineOp = ref('');       // '' | 'mkdir' | 'rename'(行内命名输入二态)
const inlineName = ref('');
const inlineInputEl = ref(null);
// 删除双段确认弹窗态
const deleteDialog = ref(false);
const deleteStage = ref(1);     // 1=意图确认;2=名字匹配(仅目录递归删)
const deleteInput = ref('');
const deleteTarget = ref(null); // 开弹窗时定格的 {path,type,name} 快照(防操作期间选中漂移)
const deleteInputEl = ref(null);

const selectedEntry = computed(() => browser.selected.value);
// 递归判定(保守):type==='dir' 即按递归语义警示+段2(空目录 RemoveAll 无害,
// children 缓存未知时提示从严比漏警好)
const deleteRecursive = computed(() => deleteTarget.value?.type === 'dir');
const deleteMatched = computed(() => !!deleteTarget.value && deleteInput.value === deleteTarget.value.name);

// 操作目录规范值:始终 currentDir(引导后=规范根;空串=引导前,传 '.' 由服务
// 端缺省守卫根,与 handler 兜底同语义)
function opsDir() {
  return browser.currentDir.value || '.';
}

function openMkdir() {
  if (opBusy.value) return;
  opError.value = '';
  inlineOp.value = 'mkdir';
  inlineName.value = '';
  void nextTick(() => inlineInputEl.value?.focus());
}

function openRename() {
  const sel = browser.selected.value;
  if (!sel || opBusy.value) return;
  opError.value = '';
  inlineOp.value = 'rename';
  inlineName.value = sel.name || ''; // 预填原名
  void nextTick(() => {
    inlineInputEl.value?.focus();
    inlineInputEl.value?.select();
  });
}

function closeInline() {
  if (opBusy.value) return;
  inlineOp.value = '';
  inlineName.value = '';
}

async function confirmInline() {
  const name = inlineName.value.trim();
  if (!name || opBusy.value) return;
  const mode = inlineOp.value;
  if (mode === 'mkdir') {
    const dir = opsDir();
    opBusy.value = true;
    opError.value = '';
    try {
      await fsMkdir(dir, name, props.token);
      inlineOp.value = '';
      inlineName.value = '';
      await browser.refreshNode(dir); // 当前目录强刷(树/列表同步见新目录)
    } catch (err) {
      opError.value = err?.message || String(err); // 重名/非法名直显,输入保留可改
    } finally {
      opBusy.value = false;
    }
    return;
  }
  if (mode === 'rename') {
    const sel = browser.selected.value;
    if (!sel) return;
    if (name === sel.name) { // 名字未变=无事发生,直接收起(不跑服务端)
      inlineOp.value = '';
      inlineName.value = '';
      return;
    }
    // dir=选中项所在目录(selected.path 由 规范目录+name 拼出,parentPathOf 即其容器)
    const dir = parentPathOf(sel.path) || '.';
    opBusy.value = true;
    opError.value = '';
    try {
      await fsRename(dir, sel.name, name, props.token);
      inlineOp.value = '';
      inlineName.value = '';
      await afterEntryGone(sel.path, dir);
    } catch (err) {
      opError.value = err?.message || String(err); // 已存在/非法名直显,输入保留
    } finally {
      opBusy.value = false;
    }
  }
}

// 条目已消失(改名/删除)后的本地态收拾:旧子树缓存+展开态**前缀清扫**(嵌套
// 后代 children[oldPath+'/x'] 若幸存,重建同名路径再展开会按陈旧缓存渲染已删
// 内容且零网络请求——键相等或前缀命中一并作废)、选中清理、父目录强刷;若
// currentDir 指进旧子树(把正所在的目录改名/删除了)退回父目录(刚刷新过,
// 列表即所见)。
async function afterEntryGone(oldPath, parentDir) {
  const prefix = `${oldPath}/`;
  for (const key of Object.keys(browser.children.value)) {
    if (key === oldPath || key.startsWith(prefix)) delete browser.children.value[key];
  }
  for (const path of [...browser.expanded.value]) {
    if (path === oldPath || path.startsWith(prefix)) browser.expanded.value.delete(path);
  }
  browser.selectNode(null);
  const cur = browser.currentDir.value;
  if (cur === oldPath || (!!oldPath && cur.startsWith(prefix))) {
    browser.currentDir.value = parentDir;
  }
  await browser.refreshNode(parentDir);
}

function openDelete() {
  const sel = browser.selected.value;
  if (!sel || opBusy.value) return;
  opError.value = '';
  inlineOp.value = ''; // 收起行内输入(若开着),弹窗独占操作面
  inlineName.value = '';
  deleteTarget.value = { path: sel.path, type: sel.type, name: sel.name };
  deleteStage.value = 1;
  deleteInput.value = '';
  deleteDialog.value = true;
}

function cancelDelete() {
  if (opBusy.value) return;
  deleteDialog.value = false;
  deleteTarget.value = null;
  deleteInput.value = '';
  opError.value = '';
}

// 段1「继续」:仅目录递归删进入段2 名字匹配(文件走 file-confirm-submit 直删,
// 不经过这里)
function continueDelete() {
  if (opBusy.value || !deleteRecursive.value) return;
  deleteStage.value = 2;
  void nextTick(() => deleteInputEl.value?.focus());
}

async function submitDelete() {
  const target = deleteTarget.value;
  if (!target || opBusy.value) return;
  if (deleteStage.value === 2 && !deleteMatched.value) return; // 段2 硬闸(按钮本就禁用,双保险)
  const parent = parentPathOf(target.path) || '.';
  opBusy.value = true;
  opError.value = '';
  try {
    // recursive 仅审计溯源(服务端目录恒 RemoveAll);文件 false/目录 true
    await fsDelete(target.path, target.type === 'dir', props.token);
    deleteDialog.value = false;
    deleteTarget.value = null;
    deleteInput.value = '';
    await afterEntryGone(target.path, parent);
  } catch (err) {
    opError.value = err?.message || String(err); // 权限/不存在直显;弹窗保留原因可见
  } finally {
    opBusy.value = false;
  }
}
</script>
