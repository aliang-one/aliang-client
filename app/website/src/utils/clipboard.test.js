import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText } from './clipboard';

// jsdom 无 navigator.clipboard;每例用 defineProperty(configurable)注入,
// afterEach 摘除还原真实环境。document.execCommand 同理打桩。
function stubClipboard(value) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true });
}

function stubExecCommand(impl) {
  Object.defineProperty(document, 'execCommand', { value: impl, configurable: true });
}

afterEach(() => {
  // 只摘自己打的桩:jsdom 基线本无 clipboard;execCommand 若 jsdom 自带也一并还原
  try { delete navigator.clipboard; } catch (_) { /* 已不存在 */ }
  try { delete document.execCommand; } catch (_) { /* 已不存在 */ }
});

describe('copyText(http 局域网直连兜底,SshIngressSettings.vue 同语义)', () => {
  it('安全上下文:clipboard.writeText 存在则走 API,成功返 true', async () => {
    const writeText = vi.fn().mockResolvedValue();
    stubClipboard({ writeText });
    await expect(copyText('hello')).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith('hello');
  });

  it('writeText 拒绝时返 false,不向外抛错(调用方以布尔判定反馈)', async () => {
    stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) });
    await expect(copyText('hello')).resolves.toBe(false);
  });

  it('回归:user 员工页 TypeError 现场——clipboard 为 undefined 时不抛'
    + '"Cannot read properties of undefined (reading \'writeText\')",退 execCommand 兜底', async () => {
    stubClipboard(undefined);
    const execCommand = vi.fn(() => true);
    stubExecCommand(execCommand);
    await expect(copyText('sk-abcdef123456')).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith('copy');
  });

  it('兜底路径把文本写入隐藏 textarea 再摘除', async () => {
    stubClipboard(undefined);
    stubExecCommand(() => true);
    const before = document.querySelectorAll('textarea').length;
    await copyText('payload-text');
    expect(document.querySelectorAll('textarea').length).toBe(before);
  });

  it('execCommand 返 false(复制被拒)时返 false', async () => {
    stubClipboard(undefined);
    stubExecCommand(() => false);
    await expect(copyText('hello')).resolves.toBe(false);
  });

  it('execCommand 抛错时返 false,不向外抛', async () => {
    stubClipboard(undefined);
    stubExecCommand(() => { throw new Error('not implemented'); });
    await expect(copyText('hello')).resolves.toBe(false);
  });

  it('无 document(SSR 极端)时安全返 false', async () => {
    stubClipboard(undefined);
    const original = globalThis.document;
    // eslint-disable-next-line no-global-assign
    globalThis.document = undefined;
    try {
      await expect(copyText('hello')).resolves.toBe(false);
    } finally {
      globalThis.document = original;
    }
  });
});
