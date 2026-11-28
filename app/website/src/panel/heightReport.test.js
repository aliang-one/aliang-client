// heightReport.test.js —— 高度上报器专测(首帧尺寸闪变修复批 P1-1):
// 核心回归=隐藏窗首报。面板壳的出生序列是「先隐藏、首次 resize 到位才
// 上屏」,而 WebKit/Chromium 对未上屏窗口挂起 scripted animations——旧
// 合帧机制押在 rAF 上,首个 resize 大概率永远发不出,面板盲等 1.2s 兜底。
// 合帧已改「pending 标志 + nextTick」(nextTick 在隐藏窗照常执行;
// scrollHeight 读取是同步强制布局,不依赖渲染帧),本文件用「rAF 死桩」
// 锁死该定谳:只要有人把 rAF 接回调度链,用例即红。App.vue 接线锚一并落
// 在本文件(源码文本锚,readFileSync 形态同 main.test.js)。
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { createHeightReporter } from './heightReport';

const currentDir = dirname(fileURLToPath(import.meta.url));

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('createHeightReporter', () => {
  it('隐藏窗首报:rAF 挂起(回调永不执行)下 schedule 仍经 nextTick 发出 resize', async () => {
    // rAF 死桩:被调也不执行回调——模拟未上屏窗口的挂起形态。旧实现
    // (rAF 合帧)在此形态下 resize 永远发不出;新实现必须照发。
    vi.stubGlobal('requestAnimationFrame', () => {});
    const send = vi.fn(() => true);
    const { schedule } = createHeightReporter({ measure: () => 432.4, send });

    schedule();
    await nextTick();

    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith({ type: 'resize', height: 433 });
  });

  it('调度链零 rAF 依赖:report/schedule 全程不触 requestAnimationFrame(回归锚)', async () => {
    const raf = vi.fn(() => {});
    vi.stubGlobal('requestAnimationFrame', raf);
    const send = vi.fn(() => true);
    const { schedule } = createHeightReporter({ measure: () => 380, send });

    schedule();
    await nextTick();

    expect(raf).not.toHaveBeenCalled();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith({ type: 'resize', height: 380 });
  });

  it('合帧:pending 位开启期间的多触发源合并为一次上报,上报后开新一轮', async () => {
    const send = vi.fn(() => true);
    let h = 500;
    const { schedule } = createHeightReporter({ measure: () => h, send });

    schedule();
    schedule();
    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenCalledWith({ type: 'resize', height: 500 });

    // pending 位已清:新一轮 schedule 生效(内容长高了)。
    h = 600;
    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send).toHaveBeenLastCalledWith({ type: 'resize', height: 600 });
  });

  it('≤1px 抖动门:±1px 不上报,超过 1px 才上报(RO 逐帧回调降噪)', async () => {
    const send = vi.fn(() => true);
    let h = 500;
    const { schedule } = createHeightReporter({ measure: () => h, send });

    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(1);

    h = 500.4; // ceil=501,|501-500|=1 → 抖动门拦下
    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(1);

    h = 502; // |502-500|=2 → 上报
    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send).toHaveBeenLastCalledWith({ type: 'resize', height: 502 });
  });

  it('send=false(无宿主/发送失败)不更新 lastSent,恢复后同值仍上报', async () => {
    const send = vi.fn(() => false);
    const { schedule } = createHeightReporter({ measure: () => 450, send });

    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(1);

    // 宿主通道恢复(返回 true):同一高度仍要发出——上次没送达不算数。
    send.mockImplementation(() => true);
    schedule();
    await nextTick();
    expect(send).toHaveBeenCalledTimes(2);
    expect(send).toHaveBeenLastCalledWith({ type: 'resize', height: 450 });
  });

  it('measure 返回 null(根节点未挂载)静默跳过,不 send 不抛', async () => {
    const send = vi.fn(() => true);
    const { report, schedule } = createHeightReporter({ measure: () => null, send });

    expect(() => report()).not.toThrow();
    schedule();
    await nextTick();
    expect(send).not.toHaveBeenCalled();
  });
});

describe('App.vue 接线锚(P1-1 隐藏窗首报链)', () => {
  const source = readFileSync(resolve(currentDir, './App.vue'), 'utf8');

  it('高度链路不再使用 requestAnimationFrame(未上屏窗口 rAF 挂起定谳)', () => {
    expect(source).not.toMatch(/requestAnimationFrame/);
  });

  it('onMounted 首报直调 reportHeight(不经 schedule,同步强布局读取)', () => {
    // ensureHeightObserver 与 reportHeight 之间不得插入 scheduleReportHeight
    // ——首报是兜底链路的「必然发出」腿,不进合帧。
    expect(source).toMatch(
      /ensureHeightObserver\(\);(?:(?!scheduleReportHeight)[\s\S])*?reportHeight\(\);/,
    );
  });

  it('watch(summary) 的跟随走 scheduleReportHeight(nextTick 合帧)', () => {
    expect(source).toMatch(/watch\(summary,\s*\(\) => \{[\s\S]{0,80}?nextTick\(scheduleReportHeight\)/);
  });

  it('RO 增量补充在册(ResizeObserver 挂 scheduleReportHeight)', () => {
    expect(source).toMatch(/new ResizeObserver\(scheduleReportHeight\)/);
  });

  it('reporter 由 measure/send 注入接线(measure=panelEl scrollHeight,send=postMessageHost)', () => {
    expect(source).toMatch(/measure:\s*\(\) => \(panelEl\.value \? panelEl\.value\.scrollHeight : null\)/);
    expect(source).toMatch(/send:\s*postMessageHost/);
  });
});
