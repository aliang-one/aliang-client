import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 subagent.test 模式：不装配 composable，测纯函数）。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

import { getAgentSessionDetail } from '../services/agentApi';
import {
  draftNeedsJournalReconcile,
  fetchFullJournalTranscript,
  DEVICE_SESSION_DETAIL_TTL_MS,
} from './useAliangChat';

// 2026-11-08 vm459 事故（spec §chat-local-session-journal-reconcile）：本地
// 草稿对象在手机端续聊后永不与 journal 对账——旧拼接稿遮蔽权威数据。本组
// 测试钉死对账判定的全部分支与分页拉全纯函数的行为。

describe('draftNeedsJournalReconcile（草稿打开时对账判定）', () => {
  const base = { id: 'c1', aiSessionId: 'local-abc' };

  it('aiSessionId 缺失（纯本地未发送）：不对账', () => {
    expect(draftNeedsJournalReconcile({ id: 'c1' }, [{ id: 'local-abc' }])).toBe(false);
  });

  it('running 中：不对账（流式气泡保护，run 终态后由既有快照对账接管）', () => {
    expect(draftNeedsJournalReconcile({ ...base, running: true }, [{ id: 'local-abc' }])).toBe(false);
  });

  it('running 标记缺失但有 running 气泡：不对账（2026-11-09 审计——该标记只在设备壳上置位，本地草稿流式中恒缺，重建会把直播气泡换成全 done 块级快照）', () => {
    const conv = {
      ...base,
      messages: [
        { role: 'user', content: '问题', status: 'done', ts: 1 },
        { role: 'assistant', content: '', status: 'running', ts: 2 },
      ],
    };
    expect(draftNeedsJournalReconcile(conv, [{ id: 'local-abc' }])).toBe(false);
  });

  it('journal 清单无同 id 条目：不对账（后端无会话）', () => {
    expect(draftNeedsJournalReconcile(base, [{ id: 'other' }])).toBe(false);
    expect(draftNeedsJournalReconcile(base, [])).toBe(false);
    expect(draftNeedsJournalReconcile(base, null)).toBe(false);
  });

  it('TTL 内已装载：不对账（同 openDeviceSession 5 分钟语义）', () => {
    const conv = { ...base, deviceLoadedAt: Date.now() - DEVICE_SESSION_DETAIL_TTL_MS + 60_000 };
    expect(draftNeedsJournalReconcile(conv, [{ id: 'local-abc' }])).toBe(false);
  });

  it('全条件满足：对账', () => {
    expect(draftNeedsJournalReconcile(base, [{ id: 'local-abc' }])).toBe(true);
  });
});

describe('fetchFullJournalTranscript（详情分页拉全，openDeviceSession 同源实现）', () => {
  beforeEach(() => {
    // resetAllMocks（非 clear）：清掉上一用例未消费的 mockResolvedValueOnce
    // 排队项，否则「封顶截断」用例剩的页会泄漏进下一用例的 mock 队列。
    vi.resetAllMocks();
  });

  const detail = (rows, hasMore, nextBefore) => ({
    transcript: rows,
    transcript_page: {
      has_more: hasMore,
      next_before_message_id: nextBefore,
    },
  });

  it('单页 has_more=false：返回该页，不再续拉', async () => {
    getAgentSessionDetail.mockResolvedValueOnce(
      detail([{ id: 'a', role: 'user', content: 'hi' }], false, ''),
    );
    const { transcript, pageError } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail);
    expect(pageError).toBe(null);
    expect(transcript).toHaveLength(1);
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(1);
    expect(getAgentSessionDetail.mock.calls[0][0]).toBe('local-abc');
  });

  it('两页翻页：时间正序拼接（后页前插），limit 参数逐页透传', async () => {
    getAgentSessionDetail
      .mockResolvedValueOnce(detail([{ id: 'late' }], true, 'tool-0009'))
      .mockResolvedValueOnce(detail([{ id: 'early' }], false, ''));
    const { transcript } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail, { limit: 100 });
    expect(transcript.map(r => r.id)).toEqual(['early', 'late']);
    expect(getAgentSessionDetail.mock.calls[0]).toEqual(['local-abc', { limit: 100, before: undefined }]);
    expect(getAgentSessionDetail.mock.calls[1]).toEqual(['local-abc', { limit: 100, before: 'tool-0009' }]);
  });

  it('maxPages 封顶：超长转录接受截断（防异常服务端拖死打开）', async () => {
    for (let i = 0; i < 5; i++) {
      getAgentSessionDetail.mockResolvedValueOnce(detail([{ id: `p${i}` }], true, `cur-${i}`));
    }
    const { transcript } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail, { maxPages: 3 });
    expect(transcript.map(r => r.id)).toEqual(['p2', 'p1', 'p0']);
  });

  it('中途页失败：降级返回已取到的部分转录并带 pageError', async () => {
    getAgentSessionDetail
      .mockResolvedValueOnce(detail([{ id: 'ok-page' }], true, 'cur-0'))
      .mockRejectedValueOnce(new Error('boom'));
    const { transcript, pageError } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail);
    expect(transcript.map(r => r.id)).toEqual(['ok-page']);
    expect(pageError).toBeInstanceOf(Error);
  });

  it('首页即失败：空转录+pageError', async () => {
    getAgentSessionDetail.mockRejectedValueOnce(new Error('down'));
    const { transcript, pageError } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail);
    expect(transcript).toEqual([]);
    expect(pageError).toBeInstanceOf(Error);
  });

  it('页 rows 为空即停（防异常服务端空页死循环）', async () => {
    getAgentSessionDetail
      .mockResolvedValueOnce(detail([{ id: 'only' }], true, 'cur-0'))
      .mockResolvedValueOnce(detail([], true, 'cur-1'));
    const { transcript } = await fetchFullJournalTranscript('local-abc', getAgentSessionDetail);
    expect(transcript).toHaveLength(1);
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(2);
  });
});
