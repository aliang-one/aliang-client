<template>
  <!--
    文件树递归节点(P3.4,spec §7.4;结构移植 aliangboard FileTreeNode):
    目录行(箭头▸/▾+文件夹图标+懒加载 spinner)与文件/符号链接行(文件图标
    +↗ 徽章)。store 由父级(FileTree→FileBrowserWindow)以 prop 注入——同一
    浮窗内树与预览共享 selected(P3.5 store-as-prop 拍板)。
    懒加载:目录行点击→store.toggleNode(path)(未展开无缓存才拉,失败留
    errors 不进 expanded);错误行重试走公开 listDir(path) 而非 toggleNode
    (P3.4 契约注记),成功才补 expanded(对齐 toggleNode 成功语义)。
    递归:展开且缓存就绪时,子条目以 depth+1 渲染嵌套 FileTreeNode(SFC 文件
    名隐式自引用);缩进=行内 paddingLeft(depth×14px),不依赖嵌套 DOM。
    P3.6:文件行图标改扩展名嗅探(fileIcons 声明式映射);目录行点击在
    toggleNode 外兼选中(上传目标=当前选中目录)。
  -->
  <div data-testid="file-tree-node" :data-path="path" :data-type="entry.type">
    <!-- 目录行:点击=展开/收起(toggleNode)+选中为当前目录(P3.6:目录可选中
         ——上传按钮以「当前选中目录」为目标,预览侧目录选中落 dir 提示无害) -->
    <div
      v-if="isDir"
      data-testid="file-tree-row-dir"
      class="flex cursor-pointer select-none items-center gap-1 rounded py-[3px] pr-2 text-xs"
      :class="rowClass"
      :style="{ paddingLeft: indentPx }"
      :title="path"
      @click="onDirClick"
    >
      <span data-testid="file-tree-arrow" class="w-3 shrink-0 text-center text-[10px] leading-none text-slate-400">{{ isExpanded ? '▾' : '▸' }}</span>
      <!-- 首拉/强刷进行中:行内 spinner(此时尚未进 expanded,箭头仍 ▸) -->
      <span
        v-if="isLoading"
        data-testid="file-tree-spinner"
        class="inline-block size-3 shrink-0 animate-spin rounded-full border-2 border-slate-300 border-t-primary"
      ></span>
      <span class="material-symbols-outlined shrink-0 text-sm leading-none" :class="isExpanded ? 'text-sky-500' : 'text-slate-400'">folder</span>
      <span class="min-w-0 flex-1 truncate">{{ entry.name }}</span>
    </div>
    <!-- 文件/符号链接行:点击=选中(selectNode,symlink 附 ↗ 徽章) -->
    <div
      v-else
      data-testid="file-tree-row-file"
      class="flex cursor-pointer select-none items-center gap-1 rounded py-[3px] pr-2 text-xs"
      :class="rowClass"
      :style="{ paddingLeft: indentPx }"
      :title="path"
      @click="select"
    >
      <span class="w-3 shrink-0"></span>
      <span class="material-symbols-outlined shrink-0 text-sm leading-none text-slate-400" :data-file-icon="fileIcon">{{ fileIcon }}</span>
      <span class="min-w-0 flex-1 truncate">{{ entry.name }}</span>
      <span v-if="isSymlink" data-testid="file-tree-symlink-badge" class="shrink-0 text-[10px] leading-none text-slate-400">↗</span>
    </div>
    <!-- 错误行:该目录上次 listDir 失败(errors[path] 非空即现,含强刷失败) -->
    <div
      v-if="isDir && errorText"
      data-testid="file-tree-error"
      class="flex items-center gap-1.5 rounded py-[3px] pr-2 text-xs text-red-500 dark:text-red-400"
      :style="{ paddingLeft: indentPx }"
    >
      <span class="min-w-0 flex-1 truncate">{{ t('fs_load_error') }}：{{ errorText }}</span>
      <button
        data-testid="file-tree-retry"
        type="button"
        class="shrink-0 rounded border border-red-300 px-1.5 py-px text-[11px] leading-4 transition hover:bg-red-50 dark:border-red-700 dark:hover:bg-red-500/10"
        @click="retry"
      >{{ t('fs_retry') }}</button>
    </div>
    <!-- 递归子级:展开且缓存就绪(children[path] 存在,含空数组)才渲染 -->
    <div v-if="isDir && isExpanded && childEntries" data-testid="file-tree-children">
      <FileTreeNode
        v-for="child in childEntries"
        :key="joinPath(path, child.name)"
        :entry="child"
        :path="joinPath(path, child.name)"
        :depth="depth + 1"
        :store="store"
      />
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';
import { iconFor } from './fileIcons';

const props = defineProps({
  // 条目元数据 {name,type:'dir'|'file'|'symlink',size,mtime,mode}(spec §6.3)
  entry: { type: Object, required: true },
  // 本条目绝对路径(父级拼好传入:规范根+name 逐级拼接)
  path: { type: String, required: true },
  // 嵌套深度(根=0),缩进=4+depth×14 px
  depth: { type: Number, default: 0 },
  // 文件浏览 store(createFileBrowserStore 实例;refs 不自动解包,经 .value 读)
  store: { type: Object, required: true },
});

const { t } = useI18n();

const isDir = computed(() => props.entry?.type === 'dir');
const isSymlink = computed(() => props.entry?.type === 'symlink');
const isExpanded = computed(() => props.store.expanded.value.has(props.path));
const isLoading = computed(() => !!props.store.loading.value[props.path]);
const errorText = computed(() => props.store.errors.value[props.path] || '');
const isSelected = computed(() => props.store.selected.value?.path === props.path);
// 展开且缓存就绪才有子级(空数组=空目录,渲染空容器)
const childEntries = computed(() => props.store.children.value[props.path] || null);

const indentPx = computed(() => 4 + props.depth * 14 + 'px');

const rowClass = computed(() =>
  isSelected.value
    ? 'bg-primary/10 text-primary'
    : 'text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800'
);

// 文件行图标:扩展名嗅探(P3.6 fileIcons 声明式映射;data-file-icon 供测试锚定)
const fileIcon = computed(() => iconFor(props.entry));

// 选中:只带 {path,type,name} 三字段(与 store.selectNode 契约一致,预览只关心这些)
function select() {
  props.store.selectNode({
    path: props.path,
    type: props.entry.type,
    name: props.entry.name,
  });
}

// 目录行点击:展开/收起 + 选中当前目录(P3.6 上传目标语义,见头注)
function onDirClick() {
  select();
  void props.store.toggleNode(props.path);
}

// 错误行重试(P3.4 契约):走公开 listDir(path) 而非 toggleNode——成功才补
// expanded(对齐 toggleNode 成功语义),失败维持错误行待再试。
async function retry() {
  const data = await props.store.listDir(props.path);
  if (data) props.store.expanded.value.add(props.path);
}

// 子路径拼接:根 '/' 特判避免 '//x'(与 FileTree 同款本地纯函数;不共享模块
// 是为守住「本批只动 4 组件+2 测试+i18n」的文件边界)。
function joinPath(parent, name) {
  if (parent === '/') return '/' + name;
  return parent.endsWith('/') ? parent + name : parent + '/' + name;
}
</script>
