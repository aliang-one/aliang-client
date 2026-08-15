import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const currentDir = dirname(fileURLToPath(import.meta.url));

describe('frontend authentication bootstrap', () => {
  it('mounts first and starts the coordinated management-session bootstrap', () => {
    const source = readFileSync(resolve(currentDir, './main.js'), 'utf8');
    expect(source).toMatch(/mount\(['"]#app['"]\)/);
    expect(source).toMatch(/initializeAuthSession\s*\(/);
    expect(source).not.toMatch(/await\s+initializeAuthSession/);
  });

  it('keeps the pure session read bounded and classifies transport failures', () => {
    const source = readFileSync(resolve(currentDir, './services/authApi.js'), 'utf8');
    expect(source).toMatch(/request\('\/api\/auth\/session', \{ method: 'GET' \}, 5000\)/);
    expect(source).toMatch(/type: 'transport_unavailable'/);
    expect(source).toMatch(/class AuthRequestError/);
  });
});
