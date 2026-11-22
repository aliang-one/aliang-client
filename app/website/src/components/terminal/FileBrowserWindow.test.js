// 文件浏览窗(P3.6)轻组件测试:createApp + h 挂载(仓内手法,同 PinGuard.test.js)。
// mock services/agentApi 四端点(fsList 供树根引导/refreshNode,fsUpload 供上传,
// fsRead 供上传后选中新文件的预览触发);FloatingWindow/FileTree/FilePreview 用
// 真身(壳交互/树结构/预览分支各有独立测试,此处只钉上传闭环与目标目录语义)。
// 文案期望经 t() 动态取,断言两侧同源(fs_uploading/fs_upload_error 随 P3.6 入词表)。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import FileBrowserWindow from './FileBrowserWindow.vue';
import { useI18n } from '../../i18n';

const mockFsList = vi.fn();
const mockFsUpload = vi.fn();
const mockFsRead = vi.fn();
const mockFsMkdir = vi.fn();
const mockFsRename = vi.fn();
const mockFsDelete = vi.fn();
vi.mock('../../services/agentApi', () => ({
  fsList: (...a) => mockFsList(...a),
  fsUpload: (...a) => mockFsUpload(...a),
  fsRead: (...a) => mockFsRead(...a),
  fsMkdir: (...a) => mockFsMkdir(...a),
  fsRename: (...a) => mockFsRename(...a),
  fsDelete: (...a) => mockFsDelete(...a),
}));

const { t } = useI18n();

const ROOT = '/Users/mac';
const rootEntries = [
  { name: 'Documents', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' },
  { name: 'notes.txt', type: 'file', size: 12, mtime: 2, mode: '-rw-r--r--' },
  { name: 'link', type: 'symlink', size: 5, mtime: 3, mode: 'lrwxr-xr-x' },
];
const CHILD = `${ROOT}/Documents`;

function defaultList(path) {
  if (path === '' || path === ROOT) return { path: ROOT, entries: rootEntries };
  return { path, entries: [] };
}

async function flushMicrotasks() {
  for (let i = 0; i < 8; i += 1) await Promise.resolve();
  await nextTick();
}

let app = null;

async function mountWindow() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  app = createApp({
    render: () => h(FileBrowserWindow, { token: 'tk', x: 10, y: 20, zIndex: 5, active: true, minimized: false }),
  });
  app.mount(el);
  await flushMicrotasks();
  const q = (sel) => el.querySelector(sel);
  return { el, q };
}

// hidden file input 喂档:jsdom 的 input.files 只读,Object.defineProperty 注入
// 数组形态(组件只读 files[0],与 FileList 等价)
function feedFile(input, file) {
  Object.defineProperty(input, 'files', { value: [file], configurable: true });
  input.dispatchEvent(new Event('change'));
}

function makeFile(name, content = 'data') {
  return new File([content], name, { type: 'text/plain' });
}

function click(el, testid) {
  el.querySelector(`[data-testid="${testid}"]`)?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mockFsList.mockImplementation((path) => defaultList(path));
  mockFsRead.mockResolvedValue({ path: '/x', content: 'x', truncated: false, binary: false });
  // 缺省上传成功:回包终名=dir/原名(同名冲突的时间戳后缀由 Go 侧落,前端只认
  // 回包 path——「终名优先」语义在首个用例以显式 Once 覆盖戳名验证)
  mockFsUpload.mockImplementation(async (dir, file) => ({ path: `${dir}/${file?.name || 'f'}`, bytes: 4 }));
  // 文件操作批(Task C)缺省:成功回包 {path}(契约形状;组件只关心不抛)
  mockFsMkdir.mockResolvedValue({ path: '/made' });
  mockFsRename.mockResolvedValue({ path: '/renamed' });
  mockFsDelete.mockResolvedValue({ path: '/deleted' });
});

afterEach(() => {
  if (app) {
    app.unmount();
    app = null;
  }
  document.body.innerHTML = '';
});

describe('FileBrowserWindow 上传(P3.6)', () => {
  it('上传成功:目标=守卫根(未选目录)→fsUpload(root,file,token);refreshNode 重拉根;新文件按回包终名选中并触发预览', async () => {
    // 回包终名带冲突时间戳(Go 侧语义),前端选中只认清包 path
    mockFsUpload.mockImplementationOnce(async () => ({ path: `${ROOT}/hello_153045.txt`, bytes: 4 }));
    const m = await mountWindow();
    expect(mockFsList).toHaveBeenCalledTimes(1); // 根引导
    const input = m.q('[data-testid="fs-upload-input"]');
    feedFile(input, makeFile('hello.txt'));
    await flushMicrotasks();
    // 上传目标=根(未选中目录)
    expect(mockFsUpload).toHaveBeenCalledWith(ROOT, expect.any(File), 'tk');
    // refreshNode(ROOT):根在 expanded → fsList 按规范根重拉(引导 1 + 强刷 1)
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
    expect(mockFsList).toHaveBeenCalledTimes(2);
    // 新文件按回包终名选中(同名冲突由 Go 加时间戳,前端只认清包 path)
    expect(mockFsRead).toHaveBeenLastCalledWith(`${ROOT}/hello_153045.txt`, 'tk');
    // 成功后无错误行、上传态退场
    expect(m.q('[data-testid="fs-upload-error"]')).toBe(null);
    expect(m.q('[data-testid="fs-upload-uploading"]')).toBe(null);
  });

  it('上传目标随 currentDir(导航补全批):树目录点击只展开不切列表;列表「进入」后才换目标', async () => {
    const m = await mountWindow();
    // 引导即 currentDir=根:右栏列表=当前目录条目
    expect(m.q('[data-testid="file-list"]')).toBeTruthy();
    expect(m.q('[data-testid="file-list-row-Documents"]')).toBeTruthy();
    // 拍板:树目录行点击=toggleNode 展开(懒拉子级),不 cd——面包屑无子段
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk'); // 展开懒拉
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null); // 未进子目录
    // 右栏列表「进入」:目录行点击→cd(CHILD),面包屑出现子段
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')?.textContent).toContain('Documents');
    // 上传目标=当前目录 CHILD
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('doc.pdf'));
    await flushMicrotasks();
    expect(mockFsUpload).toHaveBeenCalledWith(CHILD, expect.any(File), 'tk');
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk'); // refreshNode(CHILD)
    // 连传:currentDir 粘滞(选中已移到新文件),目标仍 CHILD 不退守卫根
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('doc2.pdf'));
    await flushMicrotasks();
    expect(mockFsUpload).toHaveBeenLastCalledWith(CHILD, expect.any(File), 'tk');
  });

  it('上传失败→错误行呈现(fs_upload_error+服务端 msg);再传成功后错误退场', async () => {
    mockFsUpload.mockRejectedValueOnce(new Error('File exceeds 100 MiB'));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('huge.bin'));
    await flushMicrotasks();
    const err = m.q('[data-testid="fs-upload-error"]');
    expect(err).not.toBe(null);
    expect(err.textContent).toContain(t('fs_upload_error'));
    expect(err.textContent).toContain('File exceeds 100 MiB');
    expect(mockFsList).toHaveBeenCalledTimes(1); // 失败不刷新目录
    // 再传成功:错误行退场
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('ok.txt'));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-error"]')).toBe(null);
    expect(mockFsList).toHaveBeenCalledTimes(2);
  });

  it('uploading 态:进行中钮禁用+spinner 文案;回包后恢复', async () => {
    let resolveUpload;
    mockFsUpload.mockImplementationOnce(() => new Promise((resolve) => { resolveUpload = resolve; }));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('slow.txt'));
    await nextTick();
    await nextTick();
    expect(m.q('[data-testid="fs-upload-uploading"]').textContent).toContain(t('fs_uploading'));
    expect(m.q('[data-testid="fs-upload-button"]').disabled).toBe(true);
    resolveUpload({ path: `${ROOT}/slow.txt`, bytes: 4 });
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-uploading"]')).toBe(null);
    expect(m.q('[data-testid="fs-upload-button"]').disabled).toBe(false);
  });

  it('空令牌拒绝同走错误行(透传服务端 msg),不炸不刷新', async () => {
    mockFsUpload.mockRejectedValueOnce(new Error('Invalid web terminal token'));
    const m = await mountWindow();
    feedFile(m.q('[data-testid="fs-upload-input"]'), makeFile('x.txt'));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-upload-error"]').textContent).toContain('Invalid web terminal token');
  });
});

describe('FileBrowserWindow 壳几何透传(P3.6 props 化)', () => {
  it('width/height props 进壳(默认 860×540 契约保持:不传即缺省)', async () => {
    const m = await mountWindow();
    const shell = m.q('[data-testid="floating-window"]');
    expect(shell).toBeTruthy();
    expect(shell.style.width).toBe('860px');
    expect(shell.style.height).toBe('540px');
  });
});

describe('FileBrowserWindow 右栏双视图(导航补全批)', () => {
  it('列表文件行点击→FilePreview;「← 返回列表」清 selected 回列表(当前目录不丢)', async () => {
    const m = await mountWindow();
    // 初始=列表视图(未选中)
    expect(m.q('[data-testid="file-list"]')).toBeTruthy();
    expect(m.q('[data-testid="file-preview"]')).toBe(null);
    // 列表文件行点击→选中文件→切预览(+返回钮),列表退场
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-preview"]')).toBeTruthy();
    expect(mockFsRead).toHaveBeenLastCalledWith(`${ROOT}/notes.txt`, 'tk');
    expect(m.q('[data-testid="file-list"]')).toBe(null);
    // 返回列表:清 selected,列表回来(当前目录未变,条目仍在)
    m.q('[data-testid="fs-back-to-list"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-preview"]')).toBe(null);
    expect(m.q('[data-testid="file-list"]')).toBeTruthy();
    expect(m.q('[data-testid="file-list-row-Documents"]')).toBeTruthy();
  });

  it('面包屑:根段=home;列表进入子目录→子段出现;段点击/↑ 跳转切 currentDir', async () => {
    const m = await mountWindow();
    // 引导后=根单段,↑ 禁用
    expect(m.q('[data-testid="file-breadcrumb-seg-0"]')?.textContent).toContain('home');
    expect(m.q('[data-testid="file-breadcrumb-up"]').disabled).toBe(true);
    // 进入子目录(cd 落 CHILD 缓存=defaultList 空表 → 空目录提示)
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')?.textContent).toContain('Documents');
    expect(m.q('[data-testid="file-breadcrumb-up"]').disabled).toBe(false);
    // ↑ 回根:根条目列表回来,子段退场
    m.q('[data-testid="file-breadcrumb-up"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null);
    expect(m.q('[data-testid="file-list-row-notes.txt"]')).toBeTruthy();
    // 再进子目录后点根段(home)直接跳回根
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-breadcrumb-seg-0"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null);
    expect(m.q('[data-testid="file-list-row-Documents"]')).toBeTruthy();
  });

  it('符号链接行点击→预览(评审 Fix 2:symlink 目标由 fsRead 服务端解析,不得静默无操作)', async () => {
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-link"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-preview"]')).toBeTruthy();
    expect(mockFsRead).toHaveBeenLastCalledWith(`${ROOT}/link`, 'tk');
    expect(m.q('[data-testid="file-list"]')).toBe(null); // 列表退场=预览态
  });

  it('cd 失败不跳转:错误横幅呈现服务端 msg,列表/面包屑维持当前目录;失败路径经树展开重试成功后横幅自动退场(评审 Fix 3b)', async () => {
    mockFsList.mockImplementation((path) => {
      if (path === CHILD) return Promise.reject(new Error('boom listing'));
      return defaultList(path);
    });
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const banner = m.q('[data-testid="fs-nav-error"]');
    expect(banner?.textContent).toContain(t('fs_load_error'));
    expect(banner?.textContent).toContain('boom listing');
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null); // 未跳转
    expect(m.q('[data-testid="file-list-row-notes.txt"]')).toBeTruthy(); // 列表仍是根条目
    // 横幅派生自 errors[失败 path]:树展开同一目录仍失败 → 横幅维持
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-nav-error"]')).toBeTruthy();
    // 恢复缺省后再经树展开重试成功(listDir 清 errors[CHILD])→ 横幅自动退场
    // (无任何 cd/再点目录行——钉「树重试也清横幅」的派生语义)
    mockFsList.mockImplementation((path) => defaultList(path));
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="fs-nav-error"]')).toBe(null);
    // 之后列表「进入」即正常跳转
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')?.textContent).toContain('Documents');
  });
});

describe('FileBrowserWindow 文件操作(Task C:mkdir/rename/delete)', () => {
  // v-model 文本输入赋值(jsdom 不自动派发 input;仓内同款手法 PinGuard.test.js)
  function setInput(input, value) {
    input.value = value;
    input.dispatchEvent(new Event('input'));
  }

  it('新建文件夹:行内输入→fsMkdir {dir:currentDir,name};成功 refreshNode(currentDir)+输入收起', async () => {
    const m = await mountWindow();
    // 先进子目录:mkdir 目标随 currentDir(与上传同语义)
    m.q('[data-testid="file-list-row-Documents"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const listCallsBefore = mockFsList.mock.calls.length;
    m.q('[data-testid="file-op-mkdir"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const input = m.q('[data-testid="file-op-input"]');
    expect(input).toBeTruthy();
    setInput(input, 'docs');
    m.q('[data-testid="file-op-input-confirm"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 载荷:dir=规范 currentDir(CHILD),name=输入名,令牌随 header 参数
    expect(mockFsMkdir).toHaveBeenCalledTimes(1);
    expect(mockFsMkdir).toHaveBeenCalledWith(CHILD, 'docs', 'tk');
    // 成功:refreshNode(CHILD) 重拉;行内输入收起;无错误行
    expect(mockFsList).toHaveBeenLastCalledWith(CHILD, 'tk');
    expect(mockFsList.mock.calls.length).toBe(listCallsBefore + 1);
    expect(m.q('[data-testid="file-op-input"]')).toBe(null);
    expect(m.q('[data-testid="file-op-error"]')).toBe(null);
  });

  it('新建文件夹重名:错误行直显服务端 msg(fs_op_failed 前缀);输入保留可改后重试;不刷新目录', async () => {
    mockFsMkdir.mockRejectedValueOnce(new Error('Target already exists'));
    const m = await mountWindow();
    m.q('[data-testid="file-op-mkdir"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    setInput(m.q('[data-testid="file-op-input"]'), 'Documents');
    m.q('[data-testid="file-op-input-confirm"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const err = m.q('[data-testid="file-op-error"]');
    expect(err).not.toBe(null);
    expect(err.textContent).toContain(t('fs_op_failed'));
    expect(err.textContent).toContain('Target already exists');
    // 失败:不刷新目录、输入保留(改个名字即可重试)
    expect(mockFsList).toHaveBeenCalledTimes(1);
    expect(m.q('[data-testid="file-op-input"]')).toBeTruthy();
    expect(m.q('[data-testid="file-op-input"]').value).toBe('Documents');
  });

  it('重命名:选中文件→预填原名→fsRename {dir,old_name,new_name};成功 refreshNode(父目录)+选中清理回列表', async () => {
    const m = await mountWindow();
    // 未选中:重命名/删除禁用
    expect(m.q('[data-testid="file-op-rename"]').disabled).toBe(true);
    expect(m.q('[data-testid="file-op-delete"]').disabled).toBe(true);
    // 选中文件(列表行点击→预览态)
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-op-rename"]').disabled).toBe(false);
    m.q('[data-testid="file-op-rename"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 预填原名
    const input = m.q('[data-testid="file-op-input"]');
    expect(input.value).toBe('notes.txt');
    setInput(input, 'notes2.txt');
    m.q('[data-testid="file-op-input-confirm"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 载荷:dir=选中项所在目录(根),旧名→新名
    expect(mockFsRename).toHaveBeenCalledTimes(1);
    expect(mockFsRename).toHaveBeenCalledWith(ROOT, 'notes.txt', 'notes2.txt', 'tk');
    // 成功:refreshNode(父目录)+选中清理(预览退场回列表)
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(m.q('[data-testid="file-preview"]')).toBe(null);
    expect(m.q('[data-testid="file-list"]')).toBeTruthy();
  });

  it('重命名撞已有名:错误行直显;输入保留', async () => {
    mockFsRename.mockRejectedValueOnce(new Error('Target already exists'));
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-rename"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    setInput(m.q('[data-testid="file-op-input"]'), 'dup.txt');
    m.q('[data-testid="file-op-input-confirm"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-op-error"]').textContent).toContain('Target already exists');
    expect(mockFsList).toHaveBeenCalledTimes(1); // 失败不刷新
    expect(m.q('[data-testid="file-op-input"]').value).toBe('dup.txt');
  });

  it('删除文件:段1 即可执行(无递归警/无段2 输入)→fsDelete {path,recursive:false};成功后弹窗退场+父目录刷新+选中清理', async () => {
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const dialog = m.q('[data-testid="file-confirm-dialog"]');
    expect(dialog).toBeTruthy();
    expect(dialog.textContent).toContain('notes.txt');
    // 文件:无递归警示、无名字匹配输入;提交钮=确认删除(段1 直删)
    expect(m.q('[data-testid="file-confirm-recursive-warn"]')).toBe(null);
    expect(m.q('[data-testid="file-confirm-input"]')).toBe(null);
    expect(m.q('[data-testid="file-confirm-submit"]').textContent).toContain(t('fs_delete_submit'));
    m.q('[data-testid="file-confirm-submit"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).toHaveBeenCalledTimes(1);
    expect(mockFsDelete).toHaveBeenCalledWith(`${ROOT}/notes.txt`, false, 'tk');
    // 成功:弹窗退场、refreshNode(父目录)、选中清理(回列表视图)
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBe(null);
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
    expect(mockFsList).toHaveBeenCalledTimes(2);
    expect(m.q('[data-testid="file-preview"]')).toBe(null);
    expect(m.q('[data-testid="file-list"]')).toBeTruthy();
  });

  it('删除目录(递归双段):段1 红警「非空递归」→段2 名字不匹配=按钮禁用+fs_mismatch;完全匹配才可提交→fsDelete {path,recursive:true}', async () => {
    const m = await mountWindow();
    // 目录选中走树行点击(列表目录行=「进入」,选中语义在树侧)
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-op-delete"]').disabled).toBe(false);
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 段1:标题+名称+递归红警(保守判定:dir 即递归语义);「继续」进段2
    const dialog = m.q('[data-testid="file-confirm-dialog"]');
    expect(dialog.textContent).toContain('Documents');
    expect(m.q('[data-testid="file-confirm-recursive-warn"]').textContent).toContain(t('fs_delete_recursive_warn'));
    expect(mockFsDelete).not.toHaveBeenCalled();
    m.q('[data-testid="file-confirm-continue"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    // 段2:输入名字匹配才亮提交
    const input = m.q('[data-testid="file-confirm-input"]');
    expect(input).toBeTruthy();
    expect(m.q('[data-testid="file-confirm-submit"]').disabled).toBe(true);
    setInput(input, 'Doc');
    await flushMicrotasks(); // v-if/disabled 派生面需渲染刷新(v-model ref 是同步的)
    expect(m.q('[data-testid="file-confirm-submit"]').disabled).toBe(true);
    expect(m.q('[data-testid="file-confirm-mismatch"]').textContent).toContain(t('fs_mismatch'));
    setInput(input, 'Documents'); // 完全匹配
    await flushMicrotasks();
    expect(m.q('[data-testid="file-confirm-mismatch"]')).toBe(null);
    expect(m.q('[data-testid="file-confirm-submit"]').disabled).toBe(false);
    m.q('[data-testid="file-confirm-submit"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).toHaveBeenCalledTimes(1);
    expect(mockFsDelete).toHaveBeenCalledWith(CHILD, true, 'tk');
    // 成功:弹窗退场+父目录(根)强刷+选中清理
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBe(null);
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
    expect(m.q('[data-testid="file-op-rename"]').disabled).toBe(true); // 选中已清
  });

  it('删除取消:零调用;弹窗退场;选中保留(可再发起操作)', async () => {
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBeTruthy();
    m.q('[data-testid="file-confirm-cancel"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).not.toHaveBeenCalled();
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBe(null);
    expect(m.q('[data-testid="file-op-rename"]').disabled).toBe(false); // 选中未动
    // 段2 取消同语义:目录弹窗到段2 后取消亦零调用
    m.q(`[data-testid="file-tree-node"][data-path="${CHILD}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-continue"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-cancel"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).not.toHaveBeenCalled();
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBe(null);
  });

  it('删除失败(权限):错误在弹窗内直显服务端 msg;弹窗保留原因可见', async () => {
    mockFsDelete.mockRejectedValueOnce(new Error('Path is not accessible'));
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-notes.txt"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-submit"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-confirm-dialog"]')).toBeTruthy(); // 弹窗保留
    expect(m.q('[data-testid="file-op-error-dialog"]').textContent).toContain('Path is not accessible');
    expect(mockFsList).toHaveBeenCalledTimes(1); // 失败不刷新
  });

  it('删除目录:旧子树缓存+展开态前缀清扫(评审 Fix 1)——嵌套后代 d/sub 不留陈旧缓存/展开成员,重建同名路径再展开必走网络', async () => {
    // store 不外露,children/expanded 以可观察行为断言:展开箭头 ▸(非展开态)
    // + 行点击发起新 fsList(缓存未命中)= children[d/sub] 无缓存、d/sub 不在 expanded。
    const D = `${ROOT}/d`;
    const SUB = `${D}/sub`;
    mockFsList.mockImplementation((path) => {
      if (path === '' || path === ROOT) return { path: ROOT, entries: [{ name: 'd', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' }] };
      if (path === D) return { path: D, entries: [{ name: 'sub', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' }] };
      return { path, entries: [] };
    });
    const m = await mountWindow(); // call1 根引导
    // 种缓存:树展开 d(call2)→ 树展开 d/sub(call3)→ 再点 d 收起并重选 d
    m.q(`[data-testid="file-tree-node"][data-path="${D}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q(`[data-testid="file-tree-node"][data-path="${SUB}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q(`[data-testid="file-tree-node"][data-path="${D}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList.mock.calls.length).toBe(3);
    // 删除 d(目录双段确认)
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-continue"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const input = m.q('[data-testid="file-confirm-input"]');
    input.value = 'd';
    input.dispatchEvent(new Event('input'));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-submit"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).toHaveBeenCalledWith(D, true, 'tk');
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk'); // refreshNode(父)=call4
    expect(mockFsList.mock.calls.length).toBe(4);
    // 重建同名路径(mock 静态回 d)再展开 d:children[d] 已作废 → 必走网络(call5),
    // 且 d 的子行 sub 箭头=▸(d/sub 展开成员已被清扫;漏扫则 ▾ + 陈旧缓存)
    m.q(`[data-testid="file-tree-node"][data-path="${D}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenLastCalledWith(D, 'tk');
    expect(mockFsList.mock.calls.length).toBe(5);
    const subArrow = m.q(`[data-testid="file-tree-node"][data-path="${SUB}"] [data-testid="file-tree-arrow"]`);
    expect(subArrow.textContent).toBe('▸');
    // 点击 sub 行:无缓存 → 新 fsList(call6;漏扫时 toggle 命中陈旧缓存零请求)
    m.q(`[data-testid="file-tree-node"][data-path="${SUB}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsList).toHaveBeenLastCalledWith(SUB, 'tk');
    expect(mockFsList.mock.calls.length).toBe(6);
  });

  it('删除正所在的目录:currentDir 退回已刷新的父目录(评审 Fix 2 删除变体)', async () => {
    const D = `${ROOT}/d`;
    mockFsList.mockImplementation((path) => {
      if (path === '' || path === ROOT) return { path: ROOT, entries: [{ name: 'd', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' }] };
      return { path, entries: [] };
    });
    const m = await mountWindow();
    // 列表进入 d:currentDir=D(面包屑出现子段)
    m.q('[data-testid="file-list-row-d"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')?.textContent).toContain('d');
    // 树行点击选中 d(选中语义在树侧;cd 的树同步已把 d 收进 expanded,此点即收起无妨)
    m.q(`[data-testid="file-tree-node"][data-path="${D}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-delete"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-continue"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const input = m.q('[data-testid="file-confirm-input"]');
    input.value = 'd';
    input.dispatchEvent(new Event('input'));
    await flushMicrotasks();
    m.q('[data-testid="file-confirm-submit"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsDelete).toHaveBeenCalledWith(D, true, 'tk');
    // currentDir 已退回父(根):子段退场+根条目列表呈现(父目录已强刷)
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null);
    expect(m.q('[data-testid="file-list-row-d"]')).toBeTruthy();
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
  });

  it('重命名正所在的目录:currentDir 同样退回父目录(评审 Fix 2 改名变体)', async () => {
    const D = `${ROOT}/d`;
    mockFsList.mockImplementation((path) => {
      if (path === '' || path === ROOT) return { path: ROOT, entries: [{ name: 'd', type: 'dir', size: 0, mtime: 1, mode: 'drwxr-xr-x' }] };
      return { path, entries: [] };
    });
    const m = await mountWindow();
    m.q('[data-testid="file-list-row-d"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q(`[data-testid="file-tree-node"][data-path="${D}"] [data-testid="file-tree-row-dir"]`)
      .dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    m.q('[data-testid="file-op-rename"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    const input = m.q('[data-testid="file-op-input"]');
    expect(input.value).toBe('d');
    input.value = 'd2';
    input.dispatchEvent(new Event('input'));
    m.q('[data-testid="file-op-input-confirm"]').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    await flushMicrotasks();
    expect(mockFsRename).toHaveBeenCalledWith(ROOT, 'd', 'd2', 'tk');
    // currentDir 从 D 退回根(面包屑子段退场),父目录已强刷
    expect(m.q('[data-testid="file-breadcrumb-seg-1"]')).toBe(null);
    expect(mockFsList).toHaveBeenLastCalledWith(ROOT, 'tk');
  });
});
