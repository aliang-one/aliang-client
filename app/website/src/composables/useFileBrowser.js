import { ref } from 'vue';
import { fsList } from '../services/agentApi';

// 文件浏览器树状态工厂(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md
// §6.3;计划 P3.3)。
//
// 为什么是工厂而非模块单例:文件浏览是「每个浏览器浮窗一份」的状态(P2.4
// per-window 架构)——多个文件浏览窗各有独立展开态/目录缓存/选中项,互不
// 串扰;与 useWebTerminal(保险箱全局态=单例)分工不同。每个浮窗挂载时
// createFileBrowserStore(tokenRef) 取一份独立 store,无需 resetForTests
// (工厂即隔离)。
//
// 懒加载语义(参照 aliangboard usePodFiles):目录首次展开才拉 fsList;
// 收起保留缓存(再展开零请求);失败不落缓存 → 再 toggle 即重试。
// 令牌逐调用读 tokenRef.value——15 分钟滑动续期后自动携带新值。
//
// 根引导(P3.3 决策):开窗时 setRoot('')(空 path)→ Go 侧缺省守卫根
// (home),回包 {path,...};回包 path 即规范根——缓存重挂其键下并置
// root/expanded,之后子目录路径一律由 规范根+name 拼出,toggleNode(根)
// 直接命中缓存而非重复拉取。

export function createFileBrowserStore(tokenRef) {
  const expanded = ref(new Set()); // 已展开目录 path 集(收起仅移出,缓存保留)
  const children = ref({});        // path → entries[](仅成功请求落键)
  const loading = ref({});         // path → bool
  const errors = ref({});          // path → error msg(成功即清除)
  const selected = ref(null);      // {path, type, name} | null
  const root = ref('');            // 规范根(引导回包 path;空=尚未引导)

  // path → 进行中 Promise(非响应式,纯传输簿记):同目录并发请求去重,
  // 防 UI 快速连点双拉。
  const pending = {};

  // 拉目录列表。失败不抛:errors[path] 记录文案、返回 null(调用方读 errors 面)。
  function listDir(path) {
    if (pending[path]) return pending[path];
    const run = (async () => {
      loading.value[path] = true;
      delete errors.value[path];
      try {
        const data = await fsList(path, tokenRef.value);
        const entries = Array.isArray(data?.entries) ? data.entries : [];
        if (!root.value && typeof data?.path === 'string' && data.path) {
          // 首拉即引导:回包 path=规范根(Go 空 path 缺省 home)。缓存挂
          // 规范根键下(空串键不留),后续 toggleNode(root.value) 命中缓存。
          root.value = data.path;
          if (data.path !== path) {
            children.value[data.path] = entries;
            return data;
          }
        }
        children.value[path] = entries;
        return data;
      } catch (e) {
        errors.value[path] = e?.message || String(e);
        return null;
      } finally {
        loading.value[path] = false;
        delete pending[path];
      }
    })();
    pending[path] = run;
    return run;
  }

  // 展开/收起。未展开:有缓存直接展开(零请求);无缓存拉取,成功才进
  // expanded(失败留 errors、不进 expanded → 再 toggle 即重试)。已展开:
  // 收起,仅移出 expanded,缓存保留。
  async function toggleNode(path) {
    if (expanded.value.has(path)) {
      expanded.value.delete(path);
      return;
    }
    if (children.value[path]) {
      expanded.value.add(path);
      return;
    }
    const data = await listDir(path);
    if (data) expanded.value.add(path);
  }

  // 强刷:删缓存;仍在展开中的目录立即重拉(收起态只清缓存,下次展开取新)。
  async function refreshNode(path) {
    delete children.value[path];
    if (expanded.value.has(path)) await listDir(path);
  }

  // 选中条目({path,type,name} 或 null 清除)。
  function selectNode(entry) {
    selected.value = entry || null;
  }

  // 根引导:空 path 让 Go 缺省守卫根(home);成功后规范根进 expanded
  // (根级子项初始即展示)。返回 listDir 结果(null=失败,errors 有文案)。
  async function setRoot(path = '') {
    const data = await listDir(path);
    if (root.value) expanded.value.add(root.value);
    return data;
  }

  return {
    expanded, children, loading, errors, selected, root,
    listDir, toggleNode, refreshNode, selectNode, setRoot,
  };
}
