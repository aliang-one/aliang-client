<template>
  <!--
    文件浏览浮窗(P3.4+P3.5+P3.6,spec §7.4):FloatingWindow 壳(缺省 860×540,
    width/height props 化供工作台最大化取满幅)+顶部上传工具条+双栏网格——左
    240px FileTree / 右 FilePreview。几何 props 与五个窗事件(move/minimize/
    maximize-toggle/close/focus)全透传:编排(注册表/桌面几何)归上层,与
    FloatingWindow 的「壳不管状态」分工一致,本组件只加「文件浏览内容」。
    store 拍板:每窗一份 createFileBrowserStore(P2.4 per-window 架构——多个
    文件浏览窗各自独立展开态/缓存/选中,互不串扰);树与预览共享同一实例
    (selected 同源),令牌经 computed 逐调用读最新值(滑动续期友好)。
    上传(P3.6,spec「上传按钮(进度+失败提示)」):目标=当前选中目录(目录
    行点击即选中,见 FileTreeNode),未选中目录→守卫根;fsUpload 成功→
    refreshNode(目标目录)强刷+选中新落盘文件(终名含冲突时间戳,以回包
    path 为准);失败→工具条错误行。uploading 态钮禁用+spinner 文案。
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
      class="flex h-full min-h-0 flex-col overflow-hidden bg-white text-slate-900 dark:bg-slate-900 dark:text-slate-100"
    >
      <!-- 上传工具条:目标=当前选中目录(否则守卫根);hidden file input 承接选档 -->
      <div
        data-testid="fs-upload-bar"
        class="flex shrink-0 items-center gap-2 border-b border-slate-200 bg-slate-50 px-3 py-1.5 dark:border-slate-700 dark:bg-slate-800/60"
      >
        <button
          data-testid="fs-upload-button"
          type="button"
          class="shrink-0 rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          :disabled="uploading"
          @click="pickFile"
        >{{ t('fs_upload') }}</button>
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
        <input
          ref="fileInputEl"
          data-testid="fs-upload-input"
          type="file"
          class="hidden"
          @change="onFileChosen"
        />
      </div>
      <!-- 双栏:左树 240px / 右预览 -->
      <div class="grid min-h-0 min-w-0 flex-1 grid-cols-[240px_minmax(0,1fr)] overflow-hidden">
        <!-- 左栏:目录树(240px 固定,栏内纵向滚动) -->
        <div class="min-h-0 min-w-0 overflow-hidden border-r border-slate-200 dark:border-slate-700">
          <FileTree :store="browser" />
        </div>
        <!-- 右栏:预览(文件名/徽章/下载 + 高亮码面) -->
        <div class="min-h-0 min-w-0">
          <FilePreview :entry="browser.selected.value" :token="token" />
        </div>
      </div>
    </div>
  </FloatingWindow>
</template>

<script setup>
import { computed, ref } from 'vue';
import { useI18n } from '../../i18n';
import { createFileBrowserStore } from '../../composables/useFileBrowser';
import { fsUpload } from '../../services/agentApi';
import FloatingWindow from './FloatingWindow.vue';
import FileTree from './FileTree.vue';
import FilePreview from './FilePreview.vue';

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

// —— 上传(P3.6)——
const uploading = ref(false);
const uploadError = ref('');
const fileInputEl = ref(null);

// 上传目标目录:当前选中目录(目录行点击即选中);未选/选中非目录→上次上
// 传目录(粘滞:上传成功后选中会移到新文件,连传须留在同一目录)→守卫根
// (root 未引导时空串,Go 侧缺省守卫根,语义一致)
const lastUploadDir = ref('');
const uploadDir = computed(() => {
  const sel = browser.selected.value;
  if (sel && sel.type === 'dir' && sel.path) return sel.path;
  return lastUploadDir.value || browser.root.value || '';
});

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
    // 强刷目标目录(展开态重拉;未展开目录靠懒加载天然取新)再选中新文件,
    // 终名以回包 path 为准(同名冲突 Go 侧已追加时间戳);目标目录粘滞,
    // 连传(选中已移到新文件)不退守卫根
    lastUploadDir.value = dir;
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
</script>
