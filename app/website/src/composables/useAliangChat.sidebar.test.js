import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉——只测本文件
// 的侧栏合并纯函数，不碰 composable 装配/网络（与 transcript 测试同一结构）。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import { mergeSidebarConversations } from './useAliangChat';
import { draftMatchesDeviceSession, deviceSessionToConversation } from '../utils/chatSettings';

// 清单条目 → 壳的适配用真映射器（chatSettings.deviceSessionToConversation），
// 守卫口径镜像生产 shellForEntry（无字符串 id 不派生壳）。
function shellFor(entry) {
  if (!entry || typeof entry !== 'object' || typeof entry.id !== 'string' || !entry.id) return null;
  return deviceSessionToConversation(entry);
}

const DRAFT = {
  id: 'k3x9z1',
  title: '帮我看看构建',
  provider: 'agentcore',
  messages: [{ role: 'user', content: '帮我看看构建', status: 'done' }],
  aiSessionId: 'local-k3x9z1', // 首条消息发出后由 sendMessage 落值（= 后端 ai 会话 id）
};

// 仪表盘自建会话的服务端孪生：journal 文件名 = ai 会话 id = 草稿 aiSessionId
const TWIN_ENTRY = {
  id: 'local-k3x9z1',
  provider: 'agentcore',
  title: '帮我看看构建',
  status: 'closed',
  origin: 'dashboard',
  updated_at: '2026-11-06T10:00:00Z',
};

describe('mergeSidebarConversations（侧栏三源合并 + G6 双卡去重）', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('草稿与服务端孪生壳同会话：只渲染一张卡（壳优先，草稿剔除）', () => {
    const merged = mergeSidebarConversations([DRAFT], [TWIN_ENTRY], shellFor);
    expect(merged).toHaveLength(1);
    expect(merged[0].deviceSession).toBe(TWIN_ENTRY);
    expect(merged[0].id).toBe('dev-local-k3x9z1');
  });

  it('无对应壳的草稿照常显示，与壳并存时草稿在前', () => {
    const otherDraft = { ...DRAFT, id: 'zzz', aiSessionId: 'local-zzz' };
    const merged = mergeSidebarConversations([otherDraft, DRAFT], [TWIN_ENTRY], shellFor);
    expect(merged).toHaveLength(2);
    expect(merged[0].id).toBe('zzz'); // 未命中壳的草稿保留
    expect(merged[1].deviceSession).toBe(TWIN_ENTRY);
  });

  it('从未发送的草稿（无 aiSessionId）不被误伤', () => {
    const fresh = { id: 'new1', title: '', messages: [], provider: 'agentcore' };
    const merged = mergeSidebarConversations([fresh], [TWIN_ENTRY], shellFor);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toBe(fresh);
    expect(merged[1].deviceSession).toBe(TWIN_ENTRY);
  });

  it('三键均命中即同会话：id 之外 source_session_id / managed_conversation_id 也算', () => {
    const srcEntry = { ...TWIN_ENTRY, id: 'ai_phone1', source_session_id: 'local-k3x9z1' };
    const mngEntry = { ...TWIN_ENTRY, id: 'ai_phone2', managed_conversation_id: 'local-k3x9z1' };
    expect(mergeSidebarConversations([DRAFT], [srcEntry], shellFor)).toHaveLength(1);
    expect(mergeSidebarConversations([DRAFT], [mngEntry], shellFor)).toHaveLength(1);
  });

  it('打开态壳兜底保留（清单暂缺不消失），且同样参与草稿去重', () => {
    const openedShell = {
      ...DRAFT,
      deviceSession: TWIN_ENTRY, // 打开过的设备会话在 conversations 里
      messages: [{ role: 'user', content: '帮我看看构建', status: 'done' }],
    };
    // 清单为空（agent 离线回 200 空表）：打开态壳不能消失
    expect(mergeSidebarConversations([openedShell], [], shellFor)).toHaveLength(1);
    // 同一会话的草稿 + 打开态壳并存（清单暂缺窗口）：草稿剔除、壳保留
    const merged = mergeSidebarConversations([DRAFT, openedShell], [], shellFor);
    expect(merged).toHaveLength(1);
    expect(merged[0].deviceSession).toBe(TWIN_ENTRY);
    expect(merged[0].messages).toHaveLength(1); // 打开态消息不被草稿顶掉
  });

  it('脏输入安全：非数组/脏条目/脏壳映射不炸', () => {
    expect(mergeSidebarConversations(null, null, shellFor)).toEqual([]);
    expect(mergeSidebarConversations([null, 42, DRAFT], [null, { nope: 1 }, TWIN_ENTRY], shellFor)).toHaveLength(1);
    expect(mergeSidebarConversations([DRAFT], [TWIN_ENTRY], null)).toHaveLength(1); // shellForEntry 缺省只丢壳
  });
});

describe('draftMatchesDeviceSession（草稿↔设备壳同会话判定）', () => {
  it('会话归属三键命中', () => {
    expect(draftMatchesDeviceSession(DRAFT, TWIN_ENTRY)).toBe(true);
    expect(draftMatchesDeviceSession(DRAFT, { id: 'ai_x', source_session_id: 'local-k3x9z1' })).toBe(true);
    expect(draftMatchesDeviceSession(DRAFT, { id: 'ai_y', managed_conversation_id: 'local-k3x9z1' })).toBe(true);
  });

  it('未命中/脏输入判否', () => {
    expect(draftMatchesDeviceSession(DRAFT, { id: 'other' })).toBe(false);
    expect(draftMatchesDeviceSession({ id: 'd1' }, TWIN_ENTRY)).toBe(false); // 无 aiSessionId
    expect(draftMatchesDeviceSession({ id: 'd1', aiSessionId: '  ' }, TWIN_ENTRY)).toBe(false);
    expect(draftMatchesDeviceSession(null, TWIN_ENTRY)).toBe(false);
    expect(draftMatchesDeviceSession(DRAFT, null)).toBe(false);
    expect(draftMatchesDeviceSession(DRAFT, 'str')).toBe(false);
  });
});
