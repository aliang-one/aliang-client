<template>
  <!--
    文件树编排层(P3.4,spec §7.4):消费一个 createFileBrowserStore 实例
    (prop 注入——store 由 FileBrowserWindow 持有,同窗树/预览共享 selected,
    P3.5 store-as-prop 拍板;本组件不新建 store,便于行为测试直挂真实 store)。
    根引导契约(P3.3):挂载即 setRoot('')——首拉回包 path=规范根,缓存重挂其
    键下且根进 expanded;此后根级条目直接铺 children[root],本组件从不
    toggleNode 根,子目录懒加载按需由 FileTreeNode toggle。
    根引导失败:errors[''] 有文案时给根级错误行,重试走 setRoot('')(完整
    引导语义=收养规范根+根进 expanded,裸 listDir('') 少了 expanded 一步)。
  -->
  <div data-testid="file-tree" class="h-full overflow-y-auto py-1">
    <!-- 根引导失败:错误行+重试(与节点错误行同 testid,按出现位置区分) -->
    <div
      v-if="rootError"
      data-testid="file-tree-error"
      class="mx-1 flex items-center gap-1.5 rounded px-2 py-1.5 text-xs text-red-500 dark:text-red-400"
    >
      <span class="min-w-0 flex-1 break-all">{{ t('fs_load_error') }}：{{ rootError }}</span>
      <button
        data-testid="file-tree-retry"
        type="button"
        class="shrink-0 rounded border border-red-300 px-1.5 py-px text-[11px] leading-4 transition hover:bg-red-50 dark:border-red-700 dark:hover:bg-red-500/10"
        @click="retryRoot"
      >{{ t('fs_retry') }}</button>
    </div>
    <!-- 根引导中(root 尚未收养且首拉进行中):行内 spinner -->
    <div v-else-if="rootLoading" data-testid="file-tree-loading" class="flex items-center px-2 py-2">
      <span class="inline-block size-3.5 animate-spin rounded-full border-2 border-slate-300 border-t-primary"></span>
    </div>
    <!-- 根级条目:root 已收养即铺 children[root](setRoot 已保证根进 expanded) -->
    <template v-else>
      <FileTreeNode
        v-for="e in rootEntries"
        :key="joinPath(rootPath, e.name)"
        :entry="e"
        :path="joinPath(rootPath, e.name)"
        :depth="0"
        :store="store"
      />
    </template>
  </div>
</template>

<script setup>
import { computed, onMounted } from 'vue';
import { useI18n } from '../../i18n';
import FileTreeNode from './FileTreeNode.vue';

const props = defineProps({
  // 文件浏览 store(createFileBrowserStore 实例;由 FileBrowserWindow 每窗一份)
  store: { type: Object, required: true },
});

const { t } = useI18n();

const rootPath = computed(() => props.store.root.value);
const rootEntries = computed(() => props.store.children.value[rootPath.value] || []);
// 引导态只在 root 尚未收养(空串)时看 '' 键的 loading/errors;收养后键恒空
const rootLoading = computed(() => !rootPath.value && !!props.store.loading.value['']);
const rootError = computed(() => (!rootPath.value ? props.store.errors.value[''] || '' : ''));

onMounted(() => {
  // 根引导:挂载即 setRoot('')(内部 listDir('') → 回包 path=规范根)。仅在
  // root 为空时发起——store 已引导过(如组件重挂)直接吃缓存,零重复请求。
  if (!props.store.root.value) void props.store.setRoot('');
});

async function retryRoot() {
  // 根引导重试=setRoot(''):除重拉外还负责收养规范根+根进 expanded
  await props.store.setRoot('');
}

// 子路径拼接:根 '/' 特判避免 '//x'(与 FileTreeNode 同款本地纯函数)。
function joinPath(parent, name) {
  if (parent === '/') return '/' + name;
  return parent.endsWith('/') ? parent + name : parent + '/' + name;
}
</script>
