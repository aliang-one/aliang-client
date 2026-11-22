<template>
  <!--
    文件浏览面包屑(导航补全批,spec §7.4 右栏顶部):段按钮可点(emit jump
    带该段完整 path,窗层接 store.cd);末段=当前目录(高亮非按钮);最左
    「↑」=返回上一层(emit up,窗层接 store.cdUp;根时=单段禁用)。根段用
    home 图标(material 字面量),title 出本地化「主目录」。纯展示组件:导航
    动作全部 emit 出去,不碰 store(与 FileListView 同分工;行为闭环在
    FileBrowserWindow.test.js 钉)。
  -->
  <div
    data-testid="file-breadcrumb"
    class="flex min-w-0 shrink-0 select-none items-center gap-1 border-b border-slate-200 bg-slate-50 px-2 py-1.5 dark:border-slate-700 dark:bg-slate-800/60"
  >
    <button
      data-testid="file-breadcrumb-up"
      type="button"
      class="shrink-0 rounded border border-slate-300 px-1.5 py-px text-[11px] leading-4 text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
      :disabled="atRoot"
      :title="t('fs_parent')"
      @click="emit('up')"
    >↑</button>
    <template v-for="(seg, i) in segments" :key="seg.path || `seg-${i}`">
      <span v-if="i > 0" class="shrink-0 text-[10px] leading-none text-slate-400 dark:text-slate-500">/</span>
      <!-- 非末段:可点跳转(cd 到该段 path) -->
      <button
        v-if="i < segments.length - 1"
        :data-testid="`file-breadcrumb-seg-${i}`"
        type="button"
        class="flex min-w-0 items-center rounded px-1 py-px text-xs text-slate-600 transition hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
        :title="seg.isRoot ? t('fs_home') : seg.path"
        @click="emit('jump', seg.path)"
      >
        <span v-if="seg.isRoot" class="material-symbols-outlined shrink-0 text-sm leading-none text-slate-500 dark:text-slate-400">home</span>
        <span v-else class="max-w-[10rem] truncate">{{ seg.name }}</span>
      </button>
      <!-- 末段=当前目录:高亮不可点 -->
      <span
        v-else
        :data-testid="`file-breadcrumb-seg-${i}`"
        class="flex min-w-0 items-center px-1 py-px text-xs font-medium text-primary"
        :title="currentPath"
      >
        <span v-if="seg.isRoot" class="material-symbols-outlined shrink-0 text-sm leading-none">home</span>
        <span v-else class="max-w-[14rem] truncate">{{ seg.name }}</span>
      </span>
    </template>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';

const props = defineProps({
  // breadcrumbSegments 投影:[{name,path,isRoot}](段 0=根 home 标识,名空)
  segments: { type: Array, default: () => [] },
  // 当前目录完整 path(末段 title 用)
  currentPath: { type: String, default: '' },
});

const emit = defineEmits(['jump', 'up']);

const { t } = useI18n();

// 根=仅根段(单段):↑ 禁用(再上越守卫根,Go 侧亦拒;窗层 goUp 另有双保险)
const atRoot = computed(() => props.segments.length <= 1);
</script>
