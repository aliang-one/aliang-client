import { computed, ref } from 'vue';
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
//
// 导航补全批(2026-11-22 文件管理计划 Task A):右栏列表导航态——currentDir
// (引导后=规范 home,cd 进子目录后=子目录 canonical path)、cd/cdUp 动作、
// breadcrumbSegments(根段=home 标识+各子段完整跳转 path)。cd 失败不跳转
// (errors[path] 呈现);树与列表双视图独立(树目录点击仍只展开,见
// FileBrowserWindow 头注拍板)。

// POSIX 语义上级目录:去掉最后一段('/a/b'→'/a','/a'→'/','/'→'')。前端
// 只做 '/' 拼接/拆分——分隔符契约由 Go 信封边界保证:AgentFSList/Read/Upload
// 回包 path 一律 filepath.ToSlash(agent_fs.go 信封注记,Windows 原生 '\'
// 不出服务层),入向 '/' 拼接路径 Go filepath.Clean/Join 亦兼容。
export function parentPathOf(path) {
  if (typeof path !== 'string' || !path) return '';
  const trimmed = path.length > 1 && path.endsWith('/') ? path.slice(0, -1) : path;
  const idx = trimmed.lastIndexOf('/');
  if (idx < 0) return '';
  return idx === 0 ? '/' : trimmed.slice(0, idx);
}

export function createFileBrowserStore(tokenRef) {
  const expanded = ref(new Set()); // 已展开目录 path 集(收起仅移出,缓存保留)
  const children = ref({});        // path → entries[](仅成功请求落键)
  const loading = ref({});         // path → bool
  const errors = ref({});          // path → error msg(成功即清除)
  const selected = ref(null);      // {path, type, name} | null
  const root = ref('');            // 规范根(引导回包 path;空=尚未引导)
  const currentDir = ref('');      // 右栏列表所在目录(canonical;''=未引导,引导后=root)

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

  // —— 导航补全批:右栏列表导航 ——

  // cd 进入目录。成功→currentDir=回包 canonical path(Go EvalSymlinks 归一)
  // 并做树同步(见下);失败→listDir 已把文案落 errors[path],currentDir 不动
  // (不跳转),调用方读 errors 面呈现。前端双保险闸(直拒零请求):空 path
  // (=引导语义,防误拉把 children[''] 污染进缓存)与越根(守卫根之外/前缀
  // 撞名的兄弟路径——Go 侧本也会拒,前端先拦省一次往返)。
  async function cd(path) {
    if (!path) return null;
    if (root.value && path !== root.value && !path.startsWith(root.value.endsWith('/') ? root.value : `${root.value}/`)) {
      return null;
    }
    const data = await listDir(path);
    if (!data) return null;
    currentDir.value = typeof data.path === 'string' && data.path ? data.path : path;
    // 换层清选中(评审 Fix 3a):面包屑/上级导航后,陈旧的文件预览不再对着
    // 新 currentDir 悬挂;目录选中(树行点击)同理回列表视图。
    selected.value = null;
    // 树同步(推荐增强):把 currentDir 路径上已缓存的各级目录补进 expanded,
    // 树随面包屑导航展开。只补有缓存的层级——无缓存层级若硬进 expanded,树上
    // 箭头变 ▾ 却无子行,且下一次行点击会被 toggleNode 当「收起」吞掉跳过
    // 懒拉(破坏树语义),故留原状待用户自行展开。
    let acc = '';
    for (const seg of currentDir.value.split('/')) {
      if (!seg) continue;
      acc += `/${seg}`;
      if (children.value[acc]) expanded.value.add(acc);
    }
    return data;
  }

  // 返回上一层:已在根(==root)no-op(零请求);否则 cd(parent)——parent 按
  // POSIX 语义去掉 currentDir 最后一段。
  async function cdUp() {
    if (!root.value || !currentDir.value || currentDir.value === root.value) return null;
    return cd(parentPathOf(currentDir.value));
  }

  // 面包屑段:currentDir 相对 root 的段数组。段 0=根(home 标识 isRoot,名空),
  // 其后各段 {name, 完整可跳转 path};root 未引导时空数组。防御:currentDir
  // 不在 root 之下(理论不可达——cd 有越根闸)时只给根段,不硬拼。
  const breadcrumbSegments = computed(() => {
    const rootPath = root.value;
    if (!rootPath) return [];
    const segs = [{ name: '', path: rootPath, isRoot: true }];
    const cur = currentDir.value;
    if (!cur || cur === rootPath) return segs;
    const prefix = rootPath.endsWith('/') ? rootPath : `${rootPath}/`;
    if (!cur.startsWith(prefix)) return segs;
    let acc = rootPath.endsWith('/') ? rootPath.slice(0, -1) : rootPath;
    for (const name of cur.slice(prefix.length).split('/')) {
      if (!name) continue;
      acc = `${acc}/${name}`;
      segs.push({ name, path: acc, isRoot: false });
    }
    return segs;
  });

  // 根引导:空 path 让 Go 缺省守卫根(home);成功后规范根进 expanded
  // (根级子项初始即展示)+ currentDir=规范根(右栏列表初始=home)。返回
  // listDir 结果(null=失败,errors 有文案)。
  async function setRoot(path = '') {
    const data = await listDir(path);
    if (root.value) {
      expanded.value.add(root.value);
      currentDir.value = root.value;
    }
    return data;
  }

  return {
    expanded, children, loading, errors, selected, root, currentDir,
    breadcrumbSegments,
    listDir, toggleNode, refreshNode, selectNode, setRoot, cd, cdUp,
  };
}
