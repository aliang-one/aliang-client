import { describe, expect, it } from 'vitest';
import {
  PROXY_SUB_TAB_DEFAULT,
  proxySubTabEscActive,
} from '../SettingsPage.vue';

describe('proxySubTabEscActive 子 tab 收回层闸门', () => {
  it('默认子 tab(proxy)不压收回层——Esc 直接走页面返回层', () => {
    expect(proxySubTabEscActive(PROXY_SUB_TAB_DEFAULT, 'settings')).toBe(false);
  });

  it('非默认子 tab(ingress)且当前在设置页 → 压收回层', () => {
    expect(proxySubTabEscActive('ingress', 'settings')).toBe(true);
  });

  it('非默认子 tab 但不在设置页 → 不压(跨页陈旧层闸)', () => {
    expect(proxySubTabEscActive('ingress', 'agent')).toBe(false);
    expect(proxySubTabEscActive('ingress', 'dashboard')).toBe(false);
    expect(proxySubTabEscActive('ingress', 'chat')).toBe(false);
  });

  it('非法子 tab 值同样压层(Esc 收回动作置回默认,给误入状态一条逃生路)', () => {
    expect(proxySubTabEscActive('bogus', 'settings')).toBe(true);
  });
});
