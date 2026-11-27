// OpenVikingSettings 卡 → SettingsPage 归一化链透传测试(2026-11-26 默认开
// 翻转随批)。钉两条:
// ① 卡片载荷经 buildCustomerSavePatch 必须产出 openviking diff(翻转前白名单
//    漏键,保存 200 但永不落盘——Phase2 预存缺口,对抗评审坐实);
// ② GET 回显:normalizeCustomerConfig 对后端 customer.openviking 段原样透出
//    (缺键=不挂键,后端 nil=默认开;非布尔 enabled 不进 patch 不清用户配置)。
import { describe, expect, it } from 'vitest';
import { buildCustomerSavePatch } from '../../SettingsPage.vue';
import { normalizeOpenVikingConfig } from '../../../utils/agentCoreSettings';

describe('SettingsPage openviking 透传链(卡片载荷→提交 patch)', () => {
  it('OpenVikingSettings 卡片载荷产出 openviking diff(关闭:enabled:false+保留 mode)', () => {
    // 卡片 off 态 save():{enabled:false, mode:'managed', endpoint:'', api_key:''}
    const patch = buildCustomerSavePatch(
      { openviking: { enabled: false, mode: 'managed', endpoint: '', api_key: '' } },
      {}
    );
    expect(patch.openviking).toEqual({ enabled: false, mode: 'managed' });
  });

  it('BYO 载荷四键全量 diff(managed→byo+endpoint+api_key)', () => {
    const patch = buildCustomerSavePatch(
      { openviking: { enabled: true, mode: 'byo', endpoint: 'https://ov.example/mcp', api_key: 'sk-x' } },
      { openviking: { enabled: false, mode: 'managed' } }
    );
    expect(patch.openviking).toEqual({
      enabled: true, mode: 'byo', endpoint: 'https://ov.example/mcp', api_key: 'sk-x'
    });
  });

  it('其余卡载荷(代理三段)不得把 openviking 段归零(父层合并兜底)', () => {
    const current = {
      proxy: { enable: true, type: 'ssh', server: 'old:22' },
      openviking: { enabled: false, mode: 'byo', endpoint: 'https://ov.example/mcp', api_key: 'sk-x' }
    };
    const patch = buildCustomerSavePatch(
      { proxy: { enable: true, type: 'http', server: 'new:8080' }, ai_rules: {}, proxy_rules: [] },
      current
    );
    expect(patch.openviking).toBeUndefined();
    expect(patch.proxy.server).toBe('new:8080');
  });

  it('GET 回显形态白名单透传:已知段原样出、缺段/异形=undefined(不挂键)', () => {
    expect(normalizeOpenVikingConfig({
      enabled: true, mode: 'byo', endpoint: 'https://ov.example/mcp', api_key: 'k'
    })).toEqual({
      enabled: true, mode: 'byo', endpoint: 'https://ov.example/mcp', api_key: 'k'
    });
    expect(normalizeOpenVikingConfig(undefined)).toBeUndefined();
    // mode 空白不产出(enabled 空缺时全空段不进 patch,后端 nil=默认开)
    expect(normalizeOpenVikingConfig({ mode: '  ' })).toBeUndefined();
  });

  it('非布尔 enabled / 未知形态不进 patch(三态;不清用户配置)', () => {
    expect(normalizeOpenVikingConfig({ enabled: 'yes' })).toBeUndefined();
    expect(normalizeOpenVikingConfig(null)).toBeUndefined();
    expect(normalizeOpenVikingConfig({ mode: 'managed' })).toEqual({ mode: 'managed' });
  });
});
