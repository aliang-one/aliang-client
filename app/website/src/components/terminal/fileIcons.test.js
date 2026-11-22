// iconFor 纯函数直测(P3.6 图标嗅探):映射档位+防御分支。纯函数无挂载,
// 同仓纯函数测试先例(cascadePos/floatingWindowGeom)。
import { describe, it, expect } from 'vitest';
import { iconFor } from './fileIcons';

describe('iconFor 扩展名嗅探', () => {
  it('映射档位:图片→image / 压缩包→folder_zip / 代码→code / 其余→description;目录→folder', () => {
    expect(iconFor({ name: 'logo.PNG', type: 'file' })).toBe('image'); // 扩展名大小写不敏感
    expect(iconFor({ name: 'photo.jpeg', type: 'file' })).toBe('image');
    expect(iconFor({ name: 'bundle.zip', type: 'file' })).toBe('folder_zip');
    expect(iconFor({ name: 'backup.tar.gz', type: 'file' })).toBe('folder_zip');
    expect(iconFor({ name: 'main.go', type: 'file' })).toBe('code');
    expect(iconFor({ name: 'App.vue', type: 'file' })).toBe('code');
    expect(iconFor({ name: 'notes.txt', type: 'file' })).toBe('description');
    expect(iconFor({ name: 'report.pdf', type: 'file' })).toBe('description');
    expect(iconFor({ name: 'Documents', type: 'dir' })).toBe('folder');
  });

  it('防御:无扩展/点文件/尾点/无名/非对象/symlink 目标不可判定→通用图标', () => {
    expect(iconFor({ name: 'Makefile', type: 'file' })).toBe('description'); // 无扩展
    expect(iconFor({ name: '.gitignore', type: 'file' })).toBe('description'); // 点文件(dot≤0)
    expect(iconFor({ name: 'weird.', type: 'file' })).toBe('description'); // 尾点
    expect(iconFor({ type: 'file' })).toBe('description'); // 缺名
    expect(iconFor(null)).toBe('description');
    expect(iconFor('junk')).toBe('description');
    expect(iconFor({ name: 'link', type: 'symlink' })).toBe('description'); // 目标不可判定
  });
});
