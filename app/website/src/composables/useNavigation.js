import { ref } from 'vue';

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

export function useNavigation() {
  function showPage(page, options = {}) {
    currentPage.value = page;
    if (page === 'agent' && options && options.sub !== undefined) {
      agentSubTab.value = normalizeAgentSubTab(options.sub);
    }
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
