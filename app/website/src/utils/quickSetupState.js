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
