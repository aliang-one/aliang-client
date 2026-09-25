import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import en from './en';
import zh from './zh';

const currentDir = dirname(fileURLToPath(import.meta.url));
const srcDir = resolve(currentDir, '..');

// 递归收集 src 下除 i18n 目录外的源码内容（.vue/.js），供死键断言
function collectSource(dir) {
  let out = '';
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === 'i18n') {
        continue;
      }
      out += collectSource(full);
    } else if (entry.name.endsWith('.vue') || entry.name.endsWith('.js')) {
      out += readFileSync(full, 'utf8');
    }
  }
  return out;
}

describe('i18n locale parity', () => {
  it('keeps zh and en key sets identical', () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort());
  });

  it('has no unreferenced quick-setup key outside the locale files', () => {
    const source = collectSource(srcDir);
    // 仅认带引号的引用形式（'key' / "key"），避免 qs_restore 被 qs_restore_confirm_title
    // 这类前缀键的裸子串命中而永远判不死
    const dead = Object.keys(zh).filter(
      (key) => key.startsWith('qs_') && !source.includes(`'${key}'`) && !source.includes(`"${key}"`),
    );
    expect(dead).toEqual([]);
  });
});
