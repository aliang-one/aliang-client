export function createLatestRenderGuard() {
  let sequence = 0;
  return {
    begin() {
      sequence += 1;
      return sequence;
    },
    invalidate() {
      sequence += 1;
    },
    isCurrent(requestId) {
      return requestId === sequence;
    },
    canCommit(requestId, dirty) {
      return requestId === sequence && !dirty;
    },
  };
}

export function isBuiltInQuickSetupSoftware(software) {
  return Boolean(software && !software.isCustom);
}

export function snapshotQuickSetupFiles(files) {
  return Array.isArray(files) ? files.map((file) => ({ ...file })) : [];
}

// 过滤出本机已安装的内置 software；custom 模板始终保留（spec §9.1）。
// list 项：{ code, installed?, isCustom? }。custom 判定兼容 isCustom 标记与 custom- 前缀。
export function filterInstalledQuickSetupSoftwares(list) {
  return (list || []).filter((s) => {
    const isCustom = s.isCustom || String(s.code || '').startsWith('custom-');
    return isCustom || s.installed === true;
  });
}

// 每 software 独立的接入模式状态（local/public），默认 public（spec §2）。
export function createQuickSetupModeState() {
  const modes = new Map();
  return {
    modeOf(code) {
      return modes.get(code) || 'public';
    },
    setMode(code, mode) {
      if (mode !== 'local' && mode !== 'public') return;
      modes.set(code, mode);
    },
  };
}
