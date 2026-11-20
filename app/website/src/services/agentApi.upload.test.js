// uploadChatAttachment 前后端契约（2026-11-20 审计⑤）：Go 信封 attachment
// 形状 = {ref,path,name,mime,bytes}（path 是 ai.message 载荷的硬依赖——宿主
// resolveAgentAIAttachments 要求 path 或 url 二选一）。旧 Go 端点只回
// ref/name/mime/bytes，前端强制校验 path 即 100% 抛「Malformed upload
// response」——本文件把契约两端都钉住：path 在场放行、缺失拒绝。
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../stores/auth', () => ({ syncUnauthenticatedAuthState: vi.fn() }));
vi.mock('./authFailure', () => ({ handleAuthenticationFailure: vi.fn() }));

import { uploadChatAttachment } from './agentApi';

function stubFetch(payload) {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    json: async () => payload,
  });
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

const goodEnvelope = {
  code: 0,
  msg: '',
  data: {
    attachment: { ref: 'sess/shot.png', path: '/cache/sess/shot.png', name: 'shot.png', mime: 'image/png', bytes: 3 },
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('uploadChatAttachment 契约（audit ⑤）', () => {
  it('完整信封（含 path）放行，原样返回 attachment', async () => {
    stubFetch(goodEnvelope);
    const attachment = await uploadChatAttachment('sess', new File(['x'], 'shot.png', { type: 'image/png' }));
    expect(attachment.ref).toBe('sess/shot.png');
    expect(attachment.path).toBe('/cache/sess/shot.png');
  });

  it('缺 path 的旧信封拒绝（Malformed upload response）——契约回归钉', async () => {
    const { path: _dropped, ...legacy } = goodEnvelope.data.attachment;
    stubFetch({ code: 0, msg: '', data: { attachment: legacy } });
    await expect(uploadChatAttachment('sess', new File(['x'], 'shot.png', { type: 'image/png' })))
      .rejects.toThrow('Malformed upload response');
  });

  it('缺 ref 同样拒绝；失败信封透传 msg', async () => {
    stubFetch({ code: 0, msg: '', data: { attachment: { path: '/x', name: 'a.png' } } });
    await expect(uploadChatAttachment('sess', new File(['x'], 'a.png', { type: 'image/png' })))
      .rejects.toThrow('Malformed upload response');
    stubFetch({ code: 500, msg: 'Attachment exceeds 10 MiB', data: null });
    await expect(uploadChatAttachment('sess', new File(['x'], 'a.png', { type: 'image/png' })))
      .rejects.toThrow('Attachment exceeds 10 MiB');
  });
});
