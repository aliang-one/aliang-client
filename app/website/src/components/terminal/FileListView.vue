<template>
  <!--
    当前目录列表(导航补全批,spec §7.4 右栏列表视图):目录行点击/双击→emit
    open(entry)(窗层接 store.cd 进入);文件/符号链接行点击→emit select(entry)
    (窗层接 selectNode→切预览)。目录/文件同列,类型图标区分(iconFor 扩展名
    嗅探,symlink 附 ↗ 徽章同树侧);行尾 size/mtime 右对齐小字。空目录→提示
    行;loading→spinner。纯展示组件:不碰 store、不拼跳转路径(currentPath
    只用于行 title 提示),导航语义全部 emit 给窗层。
  -->
  <div data-testid="file-list" class="h-full min-h-0 overflow-y-auto py-1">
    <!-- 当前目录列表拉取中(cd/强刷进行时) -->
    <div v-if="loading" data-testid="file-list-loading" class="flex items-center justify-center py-8">
      <span class="inline-block size-4 animate-spin rounded-full border-2 border-slate-300 border-t-primary"></span>
    </div>
    <!-- 空目录 -->
    <div
      v-else-if="!entries.length"
      data-testid="file-list-empty"
      class="flex items-center justify-center px-4 py-8 text-center text-xs text-slate-400 dark:text-slate-500"
    >{{ t('fs_empty_dir') }}</div>
    <!-- 条目行(Go 侧已目录优先、同层按名排序,此处原序铺开) -->
    <template v-else>
      <div
        v-for="entry in entries"
        :key="entry.name"
        :data-testid="`file-list-row-${entry.name}`"
        :data-type="entry.type"
        class="flex cursor-pointer select-none items-center gap-2 px-3 py-1.5 text-xs text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
        :title="entryPath(entry)"
        @click="onRowClick(entry)"
        @dblclick="onRowDblClick(entry)"
      >
        <span class="material-symbols-outlined shrink-0 text-sm leading-none text-slate-400" :data-file-icon="iconFor(entry)">{{ iconFor(entry) }}</span>
        <span class="min-w-0 flex-1 truncate" :class="{ 'font-medium': entry.type === 'dir' }">{{ entry.name }}</span>
        <span v-if="entry.type === 'symlink'" class="shrink-0 text-[10px] leading-none text-slate-400">↗</span>
        <span class="w-20 shrink-0 truncate text-right font-mono text-[10px] leading-4 text-slate-400 dark:text-slate-500">{{ sizeText(entry) }}</span>
        <span class="w-36 shrink-0 truncate text-right font-mono text-[10px] leading-4 text-slate-400 dark:text-slate-500">{{ mtimeText(entry) }}</span>
      </div>
    </template>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';
import { iconFor } from './fileIcons';

const props = defineProps({
  // 当前目录条目(fsList entries 原样:[{name,type,size,mtime,mode}])
  entries: { type: Array, default: () => [] },
  // 当前目录完整 path(行 title 提示用)
  currentPath: { type: String, default: '' },
  // 当前目录列表拉取中
  loading: { type: Boolean, default: false },
});

const emit = defineEmits(['open', 'select']);

const { t } = useI18n();

// 目录行=进入(open);文件/符号链接行=选中(select;symlink 目标由 fsRead 侧解析)
function onRowClick(entry) {
  if (entry?.type === 'dir') emit('open', entry);
  else emit('select', entry);
}

// 双击目录亦进(单击已进;兜底——将来若改单击=仅选中,「进入」语义不丢)
function onRowDblClick(entry) {
  if (entry?.type === 'dir') emit('open', entry);
}

// 行完整路径(title 提示):currentPath+name('/' 根特判,与树侧同款拼法)
function entryPath(entry) {
  if (!props.currentPath) return entry?.name || '';
  if (props.currentPath === '/') return `/${entry.name}`;
  return `${props.currentPath.replace(/\/$/, '')}/${entry.name}`;
}

// size 小字:目录无意义占 '—';文件人读 B/KB/MB/GB
function sizeText(entry) {
  if (entry?.type === 'dir') return '—';
  const n = Number(entry?.size);
  if (!Number.isFinite(n) || n < 0) return '—';
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n;
  for (const u of units) {
    v /= 1024;
    if (v < 1024) return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${u}`;
  }
  return `${v.toFixed(1)} PB`;
}

// mtime 小字:Go 契约=RFC3339 串;兼容数字夹具(按 Unix 秒)。缺省 '—'
function mtimeText(entry) {
  const raw = entry?.mtime;
  let ms = NaN;
  if (typeof raw === 'number' && Number.isFinite(raw) && raw > 0) ms = raw * 1000;
  else if (typeof raw === 'string' && raw) ms = Date.parse(raw);
  if (!Number.isFinite(ms)) return '—';
  return new Date(ms).toLocaleString();
}
</script>
