import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// 仪表盘监听器入口接线护栏(源码扫描,Go TestListenerProxyRoutesWired 同族):
// 桌面 + 移动两入口都必须在——漏移动端=评审必打回的已知事故类。不深 mount
// DashboardPage(其依赖树重;仓内先例=main.test.js 源码断言粒度)。
const currentDir = dirname(fileURLToPath(import.meta.url));
const source = readFileSync(resolve(currentDir, './DashboardPage.vue'), 'utf8');

// 注释行剥除后再断言,防「注释里的接线」假绿(Go 同族测试同款防御)。
const code = source
  .replace(/<!--[\s\S]*?-->/g, '')
  .split('\n')
  .filter((line) => !line.trim().startsWith('//'))
  .join('\n');

function countOccurrences(haystack, needle) {
  return haystack.split(needle).length - 1;
}

describe('DashboardPage 监听器入口接线(桌面+移动)', () => {
  it('桌面快捷工具区有 listener-entry-sidebar 入口', () => {
    expect(code).toContain('data-testid="listener-entry-sidebar"');
  });

  it('移动控制面板有 listener-entry-panel 入口(漏移动端=必打回)', () => {
    expect(code).toContain('data-testid="listener-entry-panel"');
  });

  it('两个入口都接 openListenerPanel(只接线一处=单端假绿)', () => {
    expect(countOccurrences(code, '@click="openListenerPanel"')).toBe(2);
  });

  it('面板以 v-if 挂载、close 事件回落开合 ref(可见才轮询,隐藏即停)', () => {
    expect(code).toMatch(/<ListenerSettings\s+v-if="listenerPanelOpen"\s+@close="listenerPanelOpen = false"\s*\/>/);
  });

  it('入口徽标接线:active 计数来自 getListeners 轮询(refs + 刷新函数)', () => {
    expect(code).toContain('listenerActiveCount');
    expect(code).toContain('refreshListenerBadge');
    expect(code).toMatch(/import \{ getListeners \} from '\.\.\/services\/agentApi'/);
    expect(code).toMatch(/import \{ normalizeListenerList \} from '\.\.\/utils\/listenerFormat'/);
  });

  it('Esc 收层面接线(与 login-modal/tun-start-modal 同栈)', () => {
    expect(code).toContain("useEscLayer('listener-panel'");
  });
});
