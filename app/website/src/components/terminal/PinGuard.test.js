// 保险箱密码盘（Task 14, plan 2026-11-21-web-terminal-phase1 §Task 14）：轻组件
// 测试。仓内手法 = createApp + h 挂载（同 SubagentPanel.test.js）；mock 掉
// services/agentApi，让真实 useWebTerminal 单例状态机流过（verify 失败不落
// token / setup ok 才 refresh）。文案期望经 t() 动态取——vault_* 键 Task 16
// 才入词表，缺失时 t() 回落键名本身（src/i18n/index.js 既定行为），断言两侧
// 同源故先后皆成立。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import PinGuard from './PinGuard.vue';
import { useI18n } from '../../i18n';
import { resetForTests } from '../../composables/useWebTerminal';
import { webTerminalCommand } from '../../services/agentApi';

vi.mock('../../services/agentApi', () => ({
  getWebTerminalState: vi.fn(async () => ({})),
  webTerminalCommand: vi.fn(),
}));

const { t } = useI18n();

async function flushMicrotasks() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
  await nextTick();
}

async function mount(mode) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const events = { unlocked: 0, setupDone: 0 };
  const app = createApp({
    render: () => h(PinGuard, {
      mode,
      onUnlocked: () => { events.unlocked += 1; },
      onSetupDone: () => { events.setupDone += 1; },
    }),
  });
  app.mount(el);
  await nextTick();
  return { el, events, app };
}

function setPin(el, testid, value) {
  const input = el.querySelector(`[data-testid="${testid}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

async function clickSubmit(el) {
  el.querySelector('[data-testid="vault-submit"]').click();
  await flushMicrotasks();
}

beforeEach(() => {
  vi.clearAllMocks();
  resetForTests();
});

afterEach(() => {
  document.body.innerHTML = '';
});

describe('PinGuard', () => {
  it('unlock 模式挂载即聚焦 PIN 输入', async () => {
    const m = await mount('unlock');
    const input = m.el.querySelector('[data-testid="vault-pin-input"]');
    expect(document.activeElement).toBe(input);
  });

  it('unlock：错 PIN → verify 载荷正确 + 错误文案含剩余次数', async () => {
    webTerminalCommand.mockResolvedValue({ ok: false, reason: 'bad_pin', attempts_left: 2 });
    const m = await mount('unlock');
    await setPin(m.el, 'vault-pin-input', '1111');
    await clickSubmit(m.el);
    expect(webTerminalCommand).toHaveBeenCalledWith({ action: 'verify', pin: '1111' }, '');
    const err = m.el.querySelector('[data-testid="vault-error"]');
    expect(err).toBeTruthy();
    expect(err.textContent).toContain(t('vault_wrong_pin'));
    expect(err.textContent).toContain(t('vault_attempts_left'));
    expect(err.textContent).toContain('2');
    expect(m.events.unlocked).toBe(0);
  });

  it('unlock：ok+token → 上抛 unlocked 一次（不重复）', async () => {
    webTerminalCommand.mockResolvedValue({ ok: true, token: 'tok_1' });
    const m = await mount('unlock');
    await setPin(m.el, 'vault-pin-input', '1234');
    await clickSubmit(m.el);
    expect(m.events.unlocked).toBe(1);
    expect(m.events.setupDone).toBe(0);
  });

  it('unlock：locked → 倒计时横幅 + 输入/提交禁用 + 卸载清 ticker', async () => {
    vi.useFakeTimers();
    try {
      webTerminalCommand.mockResolvedValue({ ok: false, reason: 'locked', lock_remaining_seconds: 30 });
      const m = await mount('unlock');
      await setPin(m.el, 'vault-pin-input', '0000');
      await clickSubmit(m.el);
      const banner = m.el.querySelector('[data-testid="vault-lock-banner"]');
      expect(banner).toBeTruthy();
      expect(banner.textContent).toContain(t('vault_locked'));
      expect(banner.textContent).toContain('30');
      expect(m.el.querySelector('[data-testid="vault-pin-input"]').disabled).toBe(true);
      expect(m.el.querySelector('[data-testid="vault-submit"]').disabled).toBe(true);
      // 2s 后倒数到 28
      vi.advanceTimersByTime(2000);
      await nextTick();
      expect(m.el.querySelector('[data-testid="vault-lock-banner"]').textContent).toContain('28');
      // 卸载清 ticker：推进到归零不抛错（清理路径无泄漏定时器）
      m.app.unmount();
      vi.advanceTimersByTime(60000);
      expect(m.el.querySelector('[data-testid="vault-lock-banner"]')).toBe(null);
    } finally {
      vi.useRealTimers();
    }
  });

  it('setup：两次输入不一致 → mismatch 错误且不触网络', async () => {
    webTerminalCommand.mockResolvedValue({ ok: true });
    const m = await mount('setup');
    await setPin(m.el, 'vault-pin-input', '1234');
    await setPin(m.el, 'vault-pin-confirm-input', '5678');
    await clickSubmit(m.el);
    expect(webTerminalCommand).not.toHaveBeenCalled();
    const err = m.el.querySelector('[data-testid="vault-error"]');
    expect(err.textContent).toContain(t('vault_mismatch'));
  });

  it('setup：bad_pin_form → 表单错误文案', async () => {
    webTerminalCommand.mockResolvedValue({ ok: false, reason: 'bad_pin_form' });
    const m = await mount('setup');
    await setPin(m.el, 'vault-pin-input', '12a4');
    await setPin(m.el, 'vault-pin-confirm-input', '12a4');
    await clickSubmit(m.el);
    const err = m.el.querySelector('[data-testid="vault-error"]');
    expect(err.textContent).toContain(t('vault_bad_form'));
    expect(m.events.setupDone).toBe(0);
  });

  it('setup：ok → 上抛 setup-done', async () => {
    webTerminalCommand.mockResolvedValue({ ok: true });
    const m = await mount('setup');
    await setPin(m.el, 'vault-pin-input', '4321');
    await setPin(m.el, 'vault-pin-confirm-input', '4321');
    await clickSubmit(m.el);
    expect(m.events.setupDone).toBe(1);
    expect(m.events.unlocked).toBe(0);
  });
});
