#!/usr/bin/env node
// 构建前校验：app/website/dist 下每个 HTML 入口(index.html + panel.html)
// 引用的每个 hash 文件必须真实存在。
//
// 背景（2026-10-16 定案的反复白屏事故）：dist 整体被 .gitignore 忽略，但
// dist/index.html 历史上被 git 跟踪过——从 git 树组装的构建树里 dist 只剩这个
// 陈旧 index.html、assets/ 整个缺失，`//go:embed website/dist` 因目录非空照样
// 构建成功，产出「index.html 引用不存在资源」的白屏二进制，且多次复发。
//
// 2026-11-27 托盘面板(Task 7)新增第二入口 panel.html:校验逻辑与 index.html
// 完全同型(复制原分支逻辑成 verifyEntry),两页各自校验引用链。本脚本挂在
// app/website 的 npm run build 之后，部署管线打包前也必须跑一遍：
// 宁可构建失败，绝不静默出白屏包。
import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const distDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'app', 'website', 'dist');
const fail = (msg) => {
  console.error(`[check-dist] ✗ ${msg}`);
  process.exit(1);
};

if (!existsSync(distDir)) {
  fail(`${distDir} 不存在：先在 app/website 执行 npm run build（部署管线须整目录同步 dist）`);
}

// verifyEntry 校验单个 HTML 入口的 assets/* 引用链,返回引用文件数。
function verifyEntry(entryName) {
  const entryPath = join(distDir, entryName);
  if (!existsSync(entryPath)) fail(`dist/${entryName} 不存在`);
  const html = readFileSync(entryPath, 'utf8');
  const refs = [...html.matchAll(/(?:src|href)="\.?\/?(assets\/[^"]+)"/g)].map((m) => m[1]);
  if (refs.length === 0) fail(`${entryName} 里找不到任何 assets/* 引用，dist 疑似异常产物`);
  if (!refs.some((r) => r.endsWith('.js'))) fail(`${entryName} 未引用任何 .js 入口文件`);
  const missing = refs.filter((r) => !existsSync(join(distDir, r)));
  if (missing.length) {
    fail(`${entryName} 引用的文件不存在（hash 不一致，部署后必白屏）：\n  ${missing.join('\n  ')}`);
  }
  console.log(`[check-dist] ✓ ${entryName} 引用的 ${refs.length} 个文件全部存在：${refs.join(', ')}`);
  return refs.length;
}

verifyEntry('index.html');
verifyEntry('panel.html');
console.log('[check-dist] ✓ 全部 HTML 入口校验通过');
