# 网页终端文件管理补全(导航+文件操作)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 补全文件浏览器导航 UX(面包屑/上级/当前目录列表双视图)与文件操作(mkdir/rename/delete 含递归删除强确认),全部走既有闸门链+agentFSResolve 守卫+审计。

**Architecture:** 导航=纯前端(复用 list API,双视图:左树+右面包屑列表/预览);文件操作=Go 三端点(mkdir/rename/delete,守卫复用 agentFSResolve,审计 fs_mkdir/fs_rename/fs_delete)+前端确认弹窗。

**Tech Stack:** 沿用阶段 3 全套,零新依赖。

**Spec:** 用户拍板(2026-11-22):递归删除支持+弹窗强确认;双视图采纳推荐。范围=mkdir/rename/delete;移动/搜索不在本批。

---

## 契约要点(实现者必读)

- 所有 FS 操作走:core 闸门②双闸+proxyFSIfNeeded(upload/download)/proxyIfNeeded(mkdir/rename/delete 走 POST 小 body,可 proxyIfNeeded 但**严禁 core 腿解析 body**——阶段 1 勘误 #10,proxy-first)→ runtime enabled+token 403 门
- 路径守卫:agentFSResolve(Clean→InsideDirectory→EvalSymlinks→复检)全端点必经
- delete:文件=os.Remove;目录=os.RemoveAll(递归,前端强确认前置);**拒删守卫根自身**;审计 fs_delete 携 recursive 标志
- rename:同目录内 os.Rename;新名走上传同款消毒(Base/控制字符/Windows 保留字符);目标已存在→409 语义错误(前端可见)
- mkdir:单层 os.Mkdir;重名→already_exists 错误;审计 fs_mkdir
- 无权限/ENOSPC/ENAMETOOLONG 等系统错误→HTTP 403/400 信封带系统错误文案(前端错误行直显)
- origin 真源:X-Aliang-WT-Loopback 标记(P3.6 已落)三新端点同样 marking

## 文件结构

```
改   app/http/services/agent_fs.go            mkdir/rename/delete 服务函数
改   app/http/services/agent_fs_test.go       攻击矩阵扩展(../ rename/mkdir/delete)
改   app/http/handlers/agent_handler_web_terminal.go  三 POST 端点+marking
改   app/http/handlers/agent_handler_web_terminal_test.go  门矩阵扩展
改   app/http/routes/routes.go + app/agentruntime/server.go  双注册
改   app/website/src/services/agentApi.js     fsMkdir/fsRename/fsDelete
新建 app/website/src/components/terminal/FileBreadcrumb.vue      面包屑+上级
新建 app/website/src/components/terminal/FileListView.vue        当前目录条目列表
改   app/website/src/components/terminal/FileBrowserWindow.vue   双视图组装+操作钮+确认弹窗
改   app/website/src/composables/useFileBrowser.js               cd/parent 动作+currentDir 状态
改   app/website/src/composables/useFileBrowser.test.js          导航/操作测试
改   app/website/src/i18n/zh.js / en.js                          fs_mkdir 等词条
新建 app/website/src/components/TerminalWorkspace.filemanager.test.js  操作接线测试
```

---

### Task A: 导航补全(面包屑+上级+当前目录列表,纯前端)

useFileBrowser 增:`currentDir` ref(setRoot/listDir 进入时更新)+`cd(path)`(进入目录=listDir+currentDir 更新+树 expanded 同步)+`cdUp()`(parent=currentDir dirname,根不越界)。FileBrowserWindow 右栏改双视图:顶部 FileBreadcrumb(面包屑段可点击+↑按钮)+当前目录条目列表(目录行双击/点击箭头进入,文件行点击预览;列表行含 size/mtime);FilePreview 改为选中文件后的切换视图(带「返回列表」钮)。测试:cd/越根防护/面包屑段渲染/列表点击语义。i18n:fs_up/fs_home/fs_back_to_list 词条双侧。

### Task B: Go mkdir/rename/delete 端点

服务三函数+守卫+审计+错误映射(already_exists/not-empty/permission 带系统文案);handler 三 POST(proxy-first+marking+runtime 门);路由双注册;测试:攻击矩阵(rename 逃逸/delete 守卫根/mkdir 非法名)、重名、无权限(EACCES 模拟:chmod 000 目录)、递归删目录、删除后列表闭环。

### Task C: 前端文件操作 UI+接线

agentApi 三函数;FileBrowserWindow 工具条扩:新建文件夹(行内输入+确认)/删除(选中项,**递归删红色双段确认弹窗**:第一段=确认意图,第二段=输入条目名匹配才可执行)/重命名(行内输入);操作后 refreshNode+错误行呈现;delete 递归标志透传。测试:弹窗门控(未匹配名不可执行)/成功闭环/错误呈现。

### Task D: 全量回归+终审+提交方案

Go 全目标包+前端全套件+build;i18n 交叉核对;终审(journey=进入目录/新建/重命名/递归删除全链)。

## 验收(=spec §11 阶段 3.5)

面包屑导航+上级按钮可用;新建文件夹/重命名/删除端到端可用且落审计;遍历攻击全拒;递归删除双段确认;无权限操作明确报错不执行。

## 已知留待(非本批)

移动/剪切粘贴、搜索、隐藏文件开关、Windows 路径显示美化(阶段后批)。
