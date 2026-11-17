# ESC「返回上一层」全 app 层级栈设计

- 日期:2026-11-18
- 状态:已批准(待实施)
- 范围:`app/website`(Vue 3 SPA,无 vue-router)

## 1. 背景与问题

仪表盘前端是纯状态驱动的单页应用:`useNavigation.js` 以模块级 `currentPage` ref 在
dashboard / chat / settings / agent / user / log 之间切换,设置区内还有两级子 tab
(代理 2 子 tab、Agent 4 子 tab)。层级事实存在,但从未被建模。

ESC 键处理已散落 6+ 处、各自为政,均直接挂 `window`/`document`:

| 位置 | 现行为 | 隐患 |
|---|---|---|
| `DashboardPage.handleDashboardKeydown` | 关登录/TUN 弹窗(逐个 if 判断) | 弹窗多于 3 个时持续腐化 |
| `QuickSetupModal.onModalKeydown` | 关弹窗;手写 `comboOverlayOpen` 嵌套守卫 | 每个新弹窗都要重写一遍守卫 |
| `CertManagementModal` | 关弹窗(`!operationBusy` 时) | 不拦冒泡,与页面级监听存在双重触发隐患 |
| `ErrorDialog` | 关弹窗 | 同上 |
| `ChatPage` 兜底 handler | composer 先处理,Esc 冒泡到页面 | 与弹窗监听的优先级靠巧合 |
| `ChatComposer` / `ChatHeader` / `ChatSidebar` | 关弹出菜单 / 取消重命名(inline,Header 与 Sidebar 各一处) | 不在统一体系内 |

目标:为 ESC 建立唯一的「层级栈」语义——**Esc 永远只作用于最顶层**,
页面返回、子 tab 收回、弹窗关闭、编辑态取消全部纳入同一体系。

## 2. 决策记录(2026-11-18 与用户逐项拍板)

1. **范围**:全 app 统一层级栈(不只设置页)。收编全部现有 handler,结构性消除优先级冲突。
2. **聊天页**:也返回 dashboard(无弹出/局部态时,ESC = 返回 dashboard;返回走
   `useAliangChat.close()`,见 §4.3)。
3. **子 tab**:算一层,iOS 式逐层退——非默认子 tab 按 ESC 先收回该栏默认子 tab,
   再按一次才返回 dashboard(代理默认子 tab 键 `proxy`,即「出口代理」页;
   Agent 默认 = `records` 记录,共 4 子 tab:`records / user-agent / aliangcode / peer-access`)。
4. **实现方案**:方案 A——`useEscStack` 层级栈 composable(否决静态层级表与 vue-router)。

## 3. 方案选型记录

- **A. `useEscStack` 层级栈(定案)**:模块级有序栈,激活即压栈、失活即移除,
  全局单点 keydown 取栈顶。与「逐层返回」语义同构;QuickSetupModal 的嵌套守卫可整体删除;
  新增弹窗一行接入。代价:逐个改造 6 处现有 handler(每处小,总面广)。
- **B. 静态层级表 + 单点分发器(否决)**:弹窗开关状态散在各组件内部,分发器要读它们
  就得集中化或广播,重构面比 A 更大;新弹窗要改两处(表 + 分发器)。现有
  `handleDashboardKeydown` 即此思路的手写版,已现扩展性瓶颈。
- **C. vue-router 嵌套路由(否决)**:URL 分享与浏览器后退键收益未被要求,
  全 app 导航重构属过度设计,YAGNI。

## 4. 架构设计

### 4.1 新增 `src/composables/useEscStack.js`

沿用 `useNavigation.js` 的模块级单例模式。

```js
import { ref } from 'vue';

const stack = ref([]);          // [{ id: Symbol, label: string, handler: () => void }]
let installed = false;          // 模块级 once 守卫,防 dev 双监听

function installDispatcher() {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  window.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    // IME 组合期 Esc 属输入法内部语义(候选窗消隐等),不拦截,
    // 保留现有透传行为(ChatPage recall 分支的 isComposingEvent 检查即此语义)
    if (e.isComposing || e.keyCode === 229) return;
    const top = stack.value[stack.value.length - 1];
    if (!top) return;                       // 栈空不拦截,交浏览器默认行为
    e.preventDefault();
    e.stopImmediatePropagation();           // 防第三方残留监听(沿用 QuickSetupModal 现有防御)
    try {
      top.handler();
    } catch (err) {
      console.error('[useEscStack]', top.label, err);
    }
  });
}

export function useEscStack() {
  installDispatcher();
  // push 返回 id;remove 按 id 精确移除;重复同 label 允许(id 去重由调用方持有)
  function push(label, handler) {
    const layer = { id: Symbol('esc-layer'), label, handler };
    stack.value.push(layer);
    return layer.id;
  }
  function remove(id) {
    const i = stack.value.findIndex((l) => l.id === id);
    if (i >= 0) stack.value.splice(i, 1);
  }
  return { push, remove, stack };
}
```

### 4.2 便捷 API `useEscLayer(label, handler, activeRef)`

面向弹窗类组件:`watch(activeRef)` 自动 push/pop,组件卸载自动清理,不漏移除。

```js
export function useEscLayer(label, handler, activeRef) {
  const { push, remove } = useEscStack();
  let id = null;
  watch(activeRef, (on) => {
    if (on && id === null) id = push(label, handler);
    else if (!on && id !== null) { remove(id); id = null; }
  }, { immediate: true });
  onUnmounted(() => { if (id !== null) remove(id); });
}
```

细节约定:

- **handler 以激活瞬间求值为准**:`activeRef` 变真时调用 handler 闭包;
  层激活期间 handler 不热更新(调用方如需最新状态,handler 内部读响应式 ref 即可)。
- **忙碌守卫留在 handler 内部**(如 `loginPending` / `operationBusy` / `restoring` 时
  handler 直接 return,层保持压栈、Esc 被吞)。不引入「不满足守卫则透传下一层」语义——
  保持「栈顶独占」足够简单且符合现有 QuickSetupModal 守卫行为。

### 4.3 导航层接入(`useNavigation.js` 扩展)

- `showPage(page)`:先移除旧页面的 `page-return` 层与 `subtab-collapse` 层,
  再按需压新层。`dashboard` 不压层(栈底为空);`settings/agent/user/log/chat` 压
  `{ label: 'page-return', handler: 见下 }`。
- **聊天页返回 handler = `useAliangChat.close()`**,不是裸 `showDashboard()`:
  `close()` 会先 `commitRename()`(iOS Safari 点关闭不触发 blur,在编标题须先落地)
  再导航回 dashboard,评审时确认该语义必须保留。其余页面 handler 为 `showDashboard()`。
- **子 tab 收回层生命周期必须以「所属页面是当前页」为闸**(评审硬伤修正):
  SettingsPage 在 App.vue 中常驻挂载、`proxySubTab`/`agentSubTab` 均刻意跨页保留
  (useNavigation.js 注释:无参跳转不打断子 tab),若只按子 tab 状态压层,
  跨页切换后会残留陈旧层——在 Agent 页按 Esc 会静默收回看不见的代理子 tab,
  而非返回 dashboard。具体闸门:
  - `agentSubTab` 收回层(导航模块内管理):active 条件 = `currentPage === 'agent' && agentSubTab !== 'records'`;
  - `proxySubTab` 收回层(SettingsPage watch):active 条件 = `currentPage === 'settings' && proxySubTab !== 默认(键名 'proxy',出口代理页)`;
  - 配合 `showPage` 切页即移除旧页两层,双保险。收回层压在页面返回层之后(天然在顶)。

## 5. 层类型与优先级(自下而上)

| 层 | label | 压栈时机 | handler |
|---|---|---|---|
| 页面返回层 | `page-return` | 进入 settings/agent/user/log/chat | chat 用 `useAliangChat.close()`,其余 `showDashboard()` |
| 子 tab 收回层 | `subtab-collapse` | 所属页面为当前页 且 非默认子 tab 激活 | 置回该栏默认子 tab |
| 弹窗层 | 各弹窗名 | modal `open` 为真 | 各自 close |
| 局部编辑态层 | 如 `rename-edit`、`composer-popup` | 局部态激活 | 取消该态 |

优先级 = 入栈顺序,后激活者在上。同一时刻可能共存的层(页面 + 子 tab + 弹窗 + 编辑态)
天然按激活先后排布,无需显式优先级数值。

## 6. 语义规则表(用户可见行为)

| 位置 | Esc 行为 |
|---|---|
| dashboard | 无操作(栈空) |
| 设置区默认 tab / 用户中心 / 日志 | 一步返回 dashboard |
| 代理「入口(SSH)/组网」子 tab | 收回「出口代理」;再按一次返回 dashboard |
| Agent 非记录子 tab | 收回「记录」;再按一次返回 dashboard |
| 聊天页(无弹出/局部态) | 返回 dashboard |
| 任一弹窗(含嵌套) | 关最后打开的那个 |
| 重命名 / composer 弹出菜单 | 取消该态 |
| 普通文本框打字中(非编辑态) | 按所在页面规则返回上一层 |

## 7. 改造清单

### 7.1 收编现有 6 处(handler 业务逻辑保留,接入方式换栈)

1. **`DashboardPage.vue`**:删除 `handleDashboardKeydown` 及其 window 监听;
   登录弹窗、TUN 弹窗改 `useEscLayer`(忙碌守卫 `loginPending` 留在 handler 内)。
2. **`QuickSetupModal.vue`**:`onModalKeydown` + window 监听删除,改
   `useEscLayer('quick-setup', ..., () => props.open)`;**`comboOverlayOpen`
   嵌套守卫整体删除**——新建组合弹窗、删除确认、恢复确认各自注册自己的层,
   栈顶天然正确(净简化,删代码)。
3. **`CertManagementModal.vue`**:document 监听删除,改 `useEscLayer`;
   `operationBusy` 守卫留 handler 内;顺带消除不拦冒泡的双重触发隐患。
4. **`ErrorDialog.vue`**:window 监听改 `useEscLayer(state.open)`。
5. **`ChatPage.vue`**:冒泡兜底 handler 拆解——局部态(弹出菜单、重命名、编辑等)
   各自注册局部层;页面返回交给导航层。实施时逐点核对现有
   `handleKeydown`(ChatPage.vue:304 起)的每个分支。
6. **`ChatComposer.vue` / `ChatHeader.vue` / `ChatSidebar.vue`**:弹出关闭、
   会话重命名取消(Header 与 Sidebar 各有一处 inline 重命名 Esc)改注册局部层
   (保持原有不干扰打字的行为——文本输入内的 inline Esc 若语义是「取消联想」
   保留 inline 并 stopPropagation,由栈统一后不影响)。

### 7.2 新增 2 处

7. **`useNavigation.js`**:页面返回层压栈/移除(见 4.3)。
8. **`SettingsPage.vue`**:`proxySubTab` watch 压子 tab 收回层
   (agentSubTab 层由 `useNavigation` 内部 watch 处理)。

### 7.3 非目标(明确不做)

- 不引入 vue-router,不动 URL 与浏览器后退键(纯键盘语义)。
- 移动端/触屏无 ESC,不做适配。
- 不做「守卫不满足则透传下一层」——保持栈顶独占。

## 8. 错误处理

- dispatch try/catch:handler 抛异常吞掉并 `console.error`,不崩全局、不移除该层。
- id 精确移除(findIndex),不存在时静默(幂等)。
- `useEscLayer` 卸载自动清理,组件忘调 unmount 也不漏。
- 模块级 `installed` once 守卫,防 HMR/dev 双监听。

## 9. 测试策略(vitest + jsdom)

1. **`useEscStack` 单测**(新文件):栈顶独占;push/remove 顺序;handler 异常吞噬且层保留;
   重复 push 同 label;卸载自动清理(`useEscLayer`);栈空时 Esc 不 preventDefault;
   **IME 守卫**:`isComposing` 事件不消费、层不响应。
2. **`useNavigation` 集成**(扩展 `useNavigation.test.js`):进入子页压层、
   回 dashboard 移除;agentSubTab 非默认压收回层、二次 Esc 收回、三次回 dashboard;
   **跨页陈旧层用例**(评审硬伤回归线):代理入口子 tab 激活 → `showPage('agent')`
   → Esc 必须返回 dashboard(而非收回隐藏的代理子 tab);反向(agentSubTab 非
   默认 → `showPage('settings')` → Esc 返回 dashboard)同验。
3. **`SettingsPage`**:proxySubTab 收回→二次回根;默认子 tab 一步回根。
4. **弹窗组件适配**:CertManagementModal / ErrorDialog / QuickSetupModal 现有测试
   逐个适配新接入方式;QuickSetupModal 新增嵌套弹窗「先关内层」用例
   (原 comboOverlayOpen 守卫语义的等价验证)。
5. **ChatPage**:局部态与页面返回的分层行为用例。
6. 不新增 e2e(键盘语义 vitest jsdom 已覆盖)。

## 10. 实施核对点(进入计划时逐项确认)

- `ChatPage.vue:304-323` 现有 Esc 冒泡兜底的完整分支清单(recall 分支含
  `isComposingEvent` 检查;dispatcher IME 守卫就位后,该检查是否简化为仅防重复,
  实施时确认,不得丢失原有透传语义)。
- `ChatComposer` 联想弹层的 inline Esc 是否需要 stopPropagation 收口。
- 其他未盘点到的 window/document 监听(实施前 `grep -rn "Escape" src/` 全量复核,
  以本清单为基线,新增遗漏项补入)。
- `main.test.js` 或 App 级测试是否有对 keydown 监听数量的断言。
