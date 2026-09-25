// 组合模板渲染与占位符预检（quick-config v3）。

// 组合模板渲染：单次正则替换（与 findUnresolvedPlaceholders/后端校验同一空白容忍
// 语义）；替换回调的返回值不会被再次扫描——变量值含 {{...}} 也不会二次展开。
export function renderComboContent(content, variables) {
  const vars = variables || {};
  return String(content ?? '').replace(
    /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g,
    (m, key) => (Object.prototype.hasOwnProperty.call(vars, key) ? String(vars[key] ?? '') : m),
  );
}

// 列出内容中未替换的占位符名（如 ['api_key']）——apply 前端预检用。
export function findUnresolvedPlaceholders(content) {
  const re = /\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g;
  const names = [];
  let m;
  while ((m = re.exec(String(content ?? ''))) !== null) {
    names.push(m[1]);
  }
  return names;
}

// git 风格对齐行 diff：返回 [{ left, right, type, leftIndex, rightIndex }]。
// left=当前在用配置（旧），right=渲染预览（新）。type 语义（供统一流染色）：
//   same    — 两侧同显相同行
//   removed — 该行只在在用配置中（apply 后将被移除），right=null
//   added   — 该行只在渲染预览中（apply 将写入），left=null
//   changed — removed/added 相邻成对合并为一行（统一流中拆回相邻 -/+ 两行）
// 行索引（供 diff 行点击采纳/剔除）：
//   leftIndex  — 该行内容在 left 原文的行下标（same/removed/changed 的左行有）
//   rightIndex — 在 right 原文的行下标；removed/changed 的右行 = 插入位语义
//                （采纳该 left 内容时插到预览这一行之前）；added 不带 leftIndex
// 算法：去公共前后缀 → 余部有界 LCS（任一侧超 800 行即退化，不做 DP）→
// 退化分支按「余部左侧全 removed / 右侧全 added」输出，再做相邻 run 成对合并，
// 最后按对齐序列保序回填行索引。
// nullish 输入按空内容处理。
export function diffRowsAligned(leftContent, rightContent) {
  const leftLines = String(leftContent ?? '').split('\n');
  const rightLines = String(rightContent ?? '').split('\n');
  let prefix = 0;
  while (
    prefix < leftLines.length &&
    prefix < rightLines.length &&
    leftLines[prefix] === rightLines[prefix]
  ) {
    prefix += 1;
  }
  let suffix = 0;
  while (
    suffix < leftLines.length - prefix &&
    suffix < rightLines.length - prefix &&
    leftLines[leftLines.length - 1 - suffix] === rightLines[rightLines.length - 1 - suffix]
  ) {
    suffix += 1;
  }
  const sameRow = (line) => ({ left: line, right: line, type: 'same' });
  const midLeft = leftLines.slice(prefix, leftLines.length - suffix);
  const midRight = rightLines.slice(prefix, rightLines.length - suffix);
  const midRows =
    midLeft.length > 800 || midRight.length > 800
      ? [
          ...midLeft.map((line) => ({ left: line, right: null, type: 'removed' })),
          ...midRight.map((line) => ({ left: null, right: line, type: 'added' })),
        ]
      : pairAdjacentRuns(lcsAlignRows(midLeft, midRight));
  return withRowIndices([
    ...leftLines.slice(0, prefix).map(sameRow),
    ...midRows,
    ...leftLines.slice(leftLines.length - suffix).map(sameRow),
  ]);
}

// 对齐序列保序回填行索引：按序扫一遍，左右各自计数——same 同进、removed 只进左、
// added 只进右、changed 成对同进。removed（与 changed 左行）的 rightIndex 取当时
// 的 right 计数 = 该 left 内容若被采纳时应插入预览的位置（插到这行之前）。
function withRowIndices(rows) {
  let leftCount = 0;
  let rightCount = 0;
  for (const row of rows) {
    if (row.type === 'added') {
      row.rightIndex = rightCount;
      rightCount += 1;
    } else {
      row.leftIndex = leftCount;
      row.rightIndex = rightCount;
      leftCount += 1;
      if (row.type !== 'removed') {
        rightCount += 1;
      }
    }
  }
  return rows;
}

// 余部 LCS 对齐：dp 自底向上（Uint16Array，800×800 约 1.3MB），回溯产出
// same/removed/added 原子行序列（removed 先于 added，与 tie-break 一致）
function lcsAlignRows(a, b) {
  const n = a.length;
  const m = b.length;
  const dp = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const rows = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      rows.push({ left: a[i], right: b[j], type: 'same' });
      i += 1;
      j += 1;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ left: a[i], right: null, type: 'removed' });
      i += 1;
    } else {
      rows.push({ left: null, right: b[j], type: 'added' });
      j += 1;
    }
  }
  while (i < n) {
    rows.push({ left: a[i], right: null, type: 'removed' });
    i += 1;
  }
  while (j < m) {
    rows.push({ left: null, right: b[j], type: 'added' });
    j += 1;
  }
  return rows;
}

// 相邻 removed/added run 成对合并为 changed 行（两种先后顺序都处理），
// 多出的一侧保持原样（left/right 为 null 的原子行）
function pairAdjacentRuns(rows) {
  const out = [];
  let i = 0;
  while (i < rows.length) {
    const kind = rows[i].type;
    if (kind !== 'removed' && kind !== 'added') {
      out.push(rows[i]);
      i += 1;
      continue;
    }
    const other = kind === 'removed' ? 'added' : 'removed';
    let firstEnd = i;
    while (firstEnd < rows.length && rows[firstEnd].type === kind) {
      firstEnd += 1;
    }
    let secondEnd = firstEnd;
    while (secondEnd < rows.length && rows[secondEnd].type === other) {
      secondEnd += 1;
    }
    const firstCount = firstEnd - i;
    const secondCount = secondEnd - firstEnd;
    if (!secondCount) {
      out.push(...rows.slice(i, firstEnd));
      i = firstEnd;
      continue;
    }
    const paired = Math.min(firstCount, secondCount);
    for (let k = 0; k < paired; k += 1) {
      const removedRow = kind === 'removed' ? rows[i + k] : rows[firstEnd + k];
      const addedRow = kind === 'removed' ? rows[firstEnd + k] : rows[i + k];
      out.push({ left: removedRow.left, right: addedRow.right, type: 'changed' });
    }
    out.push(...rows.slice(i + paired, firstEnd));
    out.push(...rows.slice(firstEnd + paired, secondEnd));
    i = secondEnd;
  }
  return out;
}
