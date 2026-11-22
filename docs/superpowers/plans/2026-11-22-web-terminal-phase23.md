# 网页终端阶段 2+3(多窗浮窗+文件浏览)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在阶段 1(已并入 master)之上交付 spec §11 的阶段 2(多窗浮窗终端,像 aliangboard:浮窗+任务栏+最小化保活+刷新恢复)与阶段 3(文件浏览:左懒加载树+右预览+上传+下载),附两项阶段 1 残余优化(xterm 动态导入、审计真源标记)。

**Architecture:** 前端全部工作(浮窗管理纯前端;文件浏览需新增 Go FS 端点)。多窗=纯前端状态机(每窗独立 WS,后端零改动);FS=Go 四端点(闸门链 ①-④ 全过,路径守卫 Clean+InsideDirectory+EvalSymlinks)+ 前端树/预览/上传下载。

**Tech Stack:** 沿用阶段 1 全套;新增依赖=零(highlight.js 已装、Blob/Fetch 原生)。

**Spec:** `docs/superpowers/specs/2026-11-21-web-terminal-design.md` §7.3/§7.4/§6.3/§11(阶段 2/3)

---

## 执行前置与纪律(同阶段 1)

- worktree:`.claude/worktrees/web-terminal-p23`,分支 `feat/web-terminal-p23`,基点 master(6bb177ff)
- **全程零 commit**(仓库铁律);阶段收尾出提交方案待用户
- vitest 在 `app/website` 下跑;Go 测试仓库根跑
- AliangBoard 参照源码(只读):`/Users/mac/MyProgram/AiProgram/AliangBoard/src/`(stores/terminals.js、components/common/FloatingWindow.vue、terminal/TerminalTaskbar.vue、styles/zScale.js、common/FileBrowserBody.vue、common/FileTree.vue、common/FileTreeNode.vue)——结构参照,Pinia→本仓模块级 composable 换皮,勿引 Pinia
- 帧契约(阶段 1 已核):output/replay {session_id,data};created{resumed,pty};error{error};auth 首帧+每帧复验(每窗独立 WS 各自 auth)

## 文件结构

```
改   app/website/src/composables/useTerminalWindows.js(新建)   窗注册表+持久化+z序
新建 app/website/src/composables/useTerminalWindows.test.js
新建 app/website/src/components/terminal/FloatingWindow.vue     浮窗壳(拖拽/最小化/最大化/关闭)
新建 app/website/src/components/terminal/TerminalTaskbar.vue    底部任务栏
改   app/website/src/components/TerminalWorkspace.vue           单窗→多窗编排
改   app/website/src/components/terminal/TerminalPane.vue       props 加 windowId(标题/单窗不变语义)
新建 app/http/services/agent_fs.go                              FS list/read/download/upload+守卫+审计
新建 app/http/services/agent_fs_test.go
改   app/http/handlers/agent_handler_web_terminal.go            FS 四端点 handler(闸门+透传)
改   app/http/handlers/agent_handler_web_terminal_test.go       FS 门+守卫测试
改   app/http/routes/routes.go + app/agentruntime/server.go     /api/agent/fs/* 双注册
新建 app/website/src/composables/useFileBrowser.js(+test)      懒加载树状态+dirCache
新建 app/website/src/components/terminal/FileTree.vue           递归树
新建 app/website/src/components/terminal/FileTreeNode.vue       树节点
新建 app/website/src/components/terminal/FilePreview.vue        预览(高亮/截断/二进制/下载)
新建 app/website/src/components/terminal/FileBrowserWindow.vue  浮窗文件浏览组合
改   app/website/src/services/agentApi.js                       fs API 客户端
改   app/website/src/i18n/zh.js / en.js                         fs_/win_ 词条
改   app/http/services/agent_web_terminal.go                    审计真源标记(残余优化:origin header)
```

---

## 阶段 2 任务(P2.1-P2.5)

### P2.1 useTerminalWindows 窗注册表 composable

模块级单例(同 useWebTerminal 形态):windows ref 数组 `[{id, title, status:'open'|'minimized', zIndex, cascade}]`;动作 `create(title)`(id=`web-<time36><rand>`,z=takeZ()+1,cascade 递进错位)/`minimize(id)`/`restore(id)`(z=takeZ()+1 置顶)/`close(id)`(从数组移除)/`focus(id)`;持久化 localStorage 键 `aliang-web-terminal-windows`(safeStorage 模式,JSON 序列化,照 MeshSettings.vue 先例):**刷新恢复全部为 minimized**;z 序分配器(越界 renumber,参照 aliangboard zScale);测试:创建/最小化/恢复 z 序/关闭/持久化往返/恢复为 minimized。

### P2.2 FloatingWindow 浮窗壳

props:{title,x,y,width,height,zIndex,active,minimized};v-show 由父层控制(壳自身只管几何);头部拖拽(pointerdown/move/up,边界钳制视口内)+双击头部切换最大化;右上三钮(最小化/最大化/关闭)emit 给父层;active 高亮边框;**保留 TerminalPane 活着=绝不 v-if 内容**(父层 v-show)。测试:拖拽位移钳制/按钮 emit。轻测试即可(拖拽逻辑纯函数化以便测:offset 计算抽函数)。

### P2.3 TerminalTaskbar 任务栏

底部条:windows 列表按钮(title+状态点),点击=最小化中则 restore/否则 focus;active 高亮;关闭小钮。轻测试:点击 emit 语义。

### P2.4 TerminalWorkspace 多窗编排(核心集成)

重构:PinGuard 过闸后主体=浮窗层(v-for windows + FloatingWindow 壳内 TerminalPane,**v-show 不销毁**)+任务栏;「新建终端」钮(头部+任务栏尾)→create→新窗;每窗独立 sessionId(TerminalPane 内已按窗随机)+独立 WS;onDead 窗级处理(该窗状态置 dead 徽章,不牵连其他窗;仍走 wt.invalidateToken+refresh→若整箱重锁则全窗回落密码盘——注意区分:单窗 dead=该窗网络问题,全局重锁=闸门失效,判据=wt.status.locked);lock/killall/总开关关→closeAll 所有窗+清注册表;ESC 层不变;「关闭」=退出工作台(注册表保留,重进恢复 minimized)。测试:编排逻辑以 useTerminalWindows 为主,组件层轻测。

### P2.5 i18n + 回归

win_ 词条(zh/en):win_new/win_minimize/win_maximize/win_close/win_restore 等;全量回归+build。

**阶段 2 验收(=spec §11):** 同屏开 3 窗互不干扰;最小化窗口 WS 与 xterm 缓冲保活(切回不闪断);刷新恢复为 minimized;锁定/急停/开关关闭全部窗口即灭。

---

## 阶段 3 任务(P3.1-P3.6)

### P3.1 Go FS 四端点(services+守卫+审计)

`app/http/services/agent_fs.go`:
- `AgentFSList(dir string) ([]map[string]interface{}, error)`:os.ReadDir 一级列表,条目 {name,type:'dir'|'file'|'symlink',size,mtime,mode};目录按 name 排序,目录优先
- `AgentFSRead(path string, maxBytes int64) (content string, truncated bool, binary bool, err error)`:binary 探测=前 8KiB 含 NUL;超 2MiB 截断
- `AgentFSDownload`/`AgentFSOpen(path)`:*os.File+FileInfo(ServeContent 用,handler 关文件)
- `AgentFSUpload(dir, filename string, src []byte) (finalPath string, err error)`:文件名清洗(filepath.Base+拒空+拒控制字符+同名追加时间戳),上限 100MiB
- **路径守卫统一前置 `agentFSResolve(raw string) (string, error)`:filepath.Clean→拒绝对路径外逃→`agentPathInsideDirectory`(复用 agent_execution_guard.go:502)→`filepath.EvalSymlinks` 后再过一次 InsideDirectory(symlink 出守卫根即拒)**。守卫根=用户 home(起始浏览点),阶段 3 不做根白名单配置(YAGNI,守卫防的是遍历攻击不是授权——授权在闸门链)
- 审计:fs_upload/fs_download 落 journal(session web-terminal,summary 带 path+bytes);list/read 不审计
- 测试(核心):遍历攻击矩阵(`../`、绝对路径、URL 编码、symlink 指向根外、`..` 深层嵌套)、上传文件名注入(`../../x`、控制字符)、同名冲突时间戳、binary 探测、截断上限

### P3.2 HTTP 四端点+双注册

handlers 追加(fs 端点走与 web-terminal 相同闸门链 ①②③④:双闸+令牌头+proxyIfNeeded;真执行在 runtime):GET `/api/agent/fs/list?path=`、GET `/api/agent/fs/read?path=`、GET `/api/agent/fs/download?path=`(http.ServeContent,模板=HandleAttachmentGet 的 nosniff+Content-Disposition attachment)、POST `/api/agent/fs/upload`(multipart,≤100MiB,守卫后写盘)。审计真源标记(残余优化):core 腿 proxyIfNeeded 前设 `X-Aliang-Origin-Loopback: 1`(IsLoopbackRequest 时),runtime 侧 WebTerminalRunCommand/FS 审计的 origin 改读此头(有=loopback,无=cookie)——修正阶段 1 终审 Finding B(审计 origin 恒标 loopback)。测试:遍历攻击经 HTTP 全拒、download 流式头、upload 超限 400。双注册 `/api/agent/fs/*`(parity 自动护栏)。

### P3.3 前端 fs API+useFileBrowser

agentApi.js:+fsList/fsRead/fsDownload(Blob)/fsUpload(multipart FormData,带令牌头)。`useFileBrowser.js`:dirCache Map `path→entries`,懒加载 `toggleNode`(首次展开拉 listDir,失败可重试),expanded Set+selected ref,refreshNode(上传/删除后强刷父目录)。测试:懒加载缓存命中/失败重试/强刷。

### P3.4 FileTree/FileTreeNode 递归树

自研递归树(全仓先例=零,照 aliangboard FileTree/FileTreeNode 结构):目录行(展开箭头/图标/名)、懒加载 spinner、错误行可重试;单选高亮;@select 事件上抛。轻测试:渲染+展开事件。

### P3.5 FilePreview+FileBrowserWindow

FilePreview:文本=highlight.js 只读(复用 utils/chatMarkdown.js 已注册语言集)、truncated/binary 徽章、下载按钮(Blob)、上传进度;二进制/图片类型 content-type 嗅探出 icon。FileBrowserWindow=FloatingWindow 壳(860×540)+左 FileTree(250px)+右 FilePreview(SplitPane 用 grid 双栏,同 ConfigDiffEditors 先例);上传/下载走 fs API。集成:工作台头部「文件」钮→开文件浏览浮窗(同多窗管理层,cascade 错位)。测试:预览分支(文本/binary/truncated)轻测。

### P3.6 i18n+全量回归

fs_/win_ 词条补齐(zh/en 双侧,程序化交叉核对);全量 vitest+build+Go 全仓。

**阶段 3 验收(=spec §11):** 遍历攻击用例全拒;大文件截断预览+完整下载;上传成功且落审计;终端/文件浏览浮窗并存互不干扰。

---

## 收尾(两阶段后)

- 全仓 Go+前端回归;整体终审(同阶段 1 模式)
- 提交方案备好(**待用户明示**,不自动提交);真机冒烟清单追加(多窗并存/文件浏览遍历攻击手测/上传落审计)
