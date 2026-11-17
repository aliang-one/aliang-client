import { ref, watch } from 'vue';
import { useEscStack } from './useEscStack';

const currentPage = ref('dashboard');

// 聊天页的 provider 预置：仪表盘「AliangCode 聊天」入口写入 'agentcore'，
// ChatPage 新建会话时读取。模块级 = 跨组件共享；浏览器刷新自然重置为空，
// 不引入陈旧状态（不持久化）。
const chatProviderPreset = ref('');

// Agent 页子 tab（模块级，与 currentPage 同生命周期，浏览器刷新自然重置）。
// 词表与 settings/agent/AgentPage.vue 的子 tab 一一对应；非法值写入时归一
// 为默认，深链安全。仅 showPage('agent', { sub }) 显式携带时才改写——仪表盘
// showAgent() 一类无参跳转不打断用户当前所在子 tab。
export const AGENT_SUB_TABS = ['records', 'user-agent', 'aliangcode', 'peer-access'];
const agentSubTab = ref('records');

function normalizeAgentSubTab(sub) {
  return AGENT_SUB_TABS.includes(sub) ? sub : 'records';
}

// ── ESC 页面返回层 / 子 tab 收回层(设计:docs/superpowers/specs/2026-11-18-esc-navigation-design.md)──
// chat 页返回层不在这里压:其 handler 依赖 useAliangChat.close()(在途重命名落地),
// 反向 import 会成环,由 ChatPage 组件挂载/卸载自管。
const PAGE_RETURN_PAGES = ['settings', 'agent', 'user', 'log'];
const AGENT_DEFAULT_SUB = 'records';
const { push: escPush, remove: escRemove, stack: escStack } = useEscStack();

// 层归属以栈为唯一事实源(按 label 查找):不在模块侧缓存层 id——栈被清空
// (测试复位/HMR)后缓存 id 变陈旧,会让后续压层被误跳过。
function escHasLayer(label) {
  return escStack.value.some((layer) => layer.label === label);
}

function escRemoveByLabel(label) {
  const layer = escStack.value.find((item) => item.label === label);
  if (layer) escRemove(layer.id);
}

// 同步两类层的压/移。幂等:label 判重,showPage 与 watch 重复触发只收敛一次。
// 收回层闸门 = 所属页面是当前页——子 tab 状态刻意跨页保留(useNavigation 既有
// 语义),若只按子 tab 值压层,跨页后会残留陈旧层,在别的页按 Esc 误收回隐藏子 tab。
function syncEscLayers() {
  const needPageReturn = PAGE_RETURN_PAGES.includes(currentPage.value);
  if (needPageReturn && !escHasLayer('page-return')) {
    escPush('page-return', () => showDashboard());
  } else if (!needPageReturn && escHasLayer('page-return')) {
    escRemoveByLabel('page-return');
  }

  const needSubTabCollapse = currentPage.value === 'agent' && agentSubTab.value !== AGENT_DEFAULT_SUB;
  if (needSubTabCollapse && !escHasLayer('subtab-collapse')) {
    escPush('subtab-collapse', () => {
      agentSubTab.value = AGENT_DEFAULT_SUB;
      // 收回动作完成后即时同步层状态,不等 watch 微任务:handler 改值后 watch 是
      // 异步 flush,窗口期内第二次 Esc 会重复命中本层。手动同步消除该窗口。
      syncEscLayers();
    });
  } else if (!needSubTabCollapse && escHasLayer('subtab-collapse')) {
    escRemoveByLabel('subtab-collapse');
  }
}

// agentSubTab 可能被组件直接改写(不经 showPage),watch 兜底同步收回层;
// 模块级 watch 与模块同生命周期,无泄漏问题
watch(agentSubTab, syncEscLayers);

// 导航动作全部模块级:状态(currentPage/agentSubTab/chatProviderPreset)本就是模块级,
// 且 ESC 层 handler 在模块级创建,闭包必须引用同作用域的函数(曾因引用 useNavigation()
// 内局部函数而在派发时 ReferenceError 被吞)。
function showPage(page, options = {}) {
  currentPage.value = page;
  if (page === 'agent' && options && options.sub !== undefined) {
    agentSubTab.value = normalizeAgentSubTab(options.sub);
  }
  syncEscLayers();
}

function showSettings() {
  showPage('settings');
}

function showAgent() {
  showPage('agent');
}

function showUserCenter() {
  showPage('user');
}

function showLogs() {
  showPage('log');
}

function showDashboard() {
  showPage('dashboard');
}

function showChat(options = {}) {
  // 只认字符串：provider 是封闭字符串词表，非字符串（Number/对象）一律归一为空串，
  // 否则 `String({})` 会写出 '[object object]' 这种脏预置传进 ChatPage。
  const provider = options?.provider;
  chatProviderPreset.value = typeof provider === 'string' ? provider.trim().toLowerCase() : '';
  showPage('chat');
}

export function useNavigation() {
  return {
    currentPage,
    chatProviderPreset,
    agentSubTab,
    showPage,
    showSettings,
    showAgent,
    showUserCenter,
    showLogs,
    showDashboard,
    showChat
  };
}
