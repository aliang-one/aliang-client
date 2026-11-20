# 本仓对 getlantern/systray 的内嵌修补版

## 基线

上游 `github.com/getlantern/systray` **v1.2.2**(2023-03-27 发布)逐字节拷贝,
Apache-2.0(LICENSE 原样保留)。本目录的 go.mod 保持上游 module path 不变,
由主仓 go.mod 的 `replace github.com/getlantern/systray => ./third_party/systray`
接管解析。

## 为什么内嵌(而非升级)

- 上游自 v1.2.2 后 2 年半无发版,master 分支同样带漏——GDI 泄漏上游不会修。
- fyne-io/systray v1.12.2(活跃分支)已验尸:泄漏逐行相同,换分支无解。
- 事故现场:2026-11-20 0.196,companion 托盘 AI 菜单每 2s×4 providers 调
  MenuItem.SetIcon,HBITMAP 泄漏 ~83min 耗尽 10k GDI 配额,托盘图标消失
  (WER GDIObjectLeak ×3 实锤)。

## 相对上游的改动

除本节所列外,其余文件逐字节 = 上游 v1.2.2。所有补丁处均有 `本仓补丁`
注释锚点(升级时 grep 此词重放补丁)。

### systray_windows.go(3 处)

1. proc 表新增 `pDeleteObject = g32.NewProc("DeleteObject")`。
2. `iconToBitmap`:`pDrawIconEx` 失败路径上游直接 `return 0`,已创建的
   hMemBmp 无人接管 = 泄漏;补丁在返回前销毁。
3. `MenuItem.SetIcon`:上游覆盖 `wt.menuItemIcons[id]` 前不销毁旧句柄,
   每次调用泄漏一个 HBITMAP(主泄漏点);补丁在覆盖前 `DeleteObject` 旧值。

### 删除的文件

- `example/`、`webview_example/`:演示目录,含 package main,保留会拖累
  主仓 `go build ./...`。
- `systray_windows_test.go`:上游自带坏测试(v1.2.2 里该文件调用的还是旧
  API 签名 `addOrUpdateMenuItem(int32,string,bool,bool)`,而库已改为
  `(uint32,uint32,string,bool,bool)`),windows/amd64 下编译不过,零测试
  价值,删除。

### 格式豁免(刻意不做)

vendored 文件保留上游旧式 `// +build` 构建标签与 `ioutil` 等写法,不跑
gofmt 现代化——本目录以「逐字节忠实于上游 + 仅功能补丁」为纪律,仓库级
`gofmt -l` 会点名本目录属预期行为。

## 升级流程

上游若发新版:替换本目录为新版源码 → `grep -n 本仓补丁 systray_windows.go`
按上文 3 处重放补丁 → 重新评估是否恢复被删文件 → 主仓
`GOOS=windows go build ./third_party/systray/` + darwin `go build ./app/tray/`
双验证。

## 验证方式

GDI 行为无法在 darwin 上运行时验证,分三层:
1. 编译层:`GOOS=windows GOARCH=amd64 go build ./third_party/systray/`。
2. 审阅层:补丁 diff 仅 3 处、语义直白(销毁即将被覆盖/丢弃的句柄)。
3. 实机层:部署后在真机用 GetGuiResources 观测 companion 的 GDI 对象数,
   AI 状态翻转下应保持平坦(不再线性增长)。
