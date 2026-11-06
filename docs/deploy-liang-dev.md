# liang-dev 部署管线（alianggate core/agent）

> 2026-10-16 因「仪表盘反复白屏」事故重写本管线约定，见 §3。
> 服务器：liang-dev = 172.16.222.1（wb_ssh 名「本地vibecoding-dev服务器」），用户 liang。
> 服务为 **user 级 systemd 单元**：`systemctl --user {aliang, aliang-agent}`，
> 二进制 `/home/liang/aliang`，日志 `~/aliang.log` / `~/aliang-agent.log`。

## 1. 标准流程

1. **本地构建前端**：`cd app/website && npm run build`
   （build 末尾自动跑 `scripts/check-dist.mjs`，校验 index.html 引用的 hash 文件齐全，不一致直接失败）。
2. **打包构建树**（git 跟踪文件 + 整个 dist 目录，二者缺一不可）：
   ```bash
   { git ls-files; find app/website/dist -type f; } | sort -u > /tmp/deploy-list
   tar -czf /tmp/alianggate-deploy.tgz -T /tmp/deploy-list
   ```
3. **上传换树**：
   ```bash
   scp /tmp/alianggate-deploy.tgz liang@172.16.222.1:/tmp/
   ssh liang@172.16.222.1 'rm -rf ~/alianggate-head.new && mkdir -p ~/alianggate-head.new && tar -xzf /tmp/alianggate-deploy.tgz -C ~/alianggate-head.new'
   ```
4. **服务器构建**（CGO 必须开，代理用 goproxy.cn）：
   ```bash
   ssh liang@172.16.222.1 'cd ~/alianggate-head.new && CGO_ENABLED=1 GOPROXY=https://goproxy.cn,direct go build -o aliang-new ./cmd/aliang'
   ```
5. **换装重启**（先备份；启动顺序先 agent 后 core）：
   ```bash
   ssh liang@172.16.222.1 'systemctl --user stop aliang-agent aliang \
     && cp -a ~/aliang ~/aliang.bak-$(date +%Y%m%d-%H%M%S) \
     && mv ~/alianggate-head.new/aliang-new ~/aliang && chmod +x ~/aliang \
     && systemctl --user start aliang-agent && sleep 2 && systemctl --user start aliang'
   ```
6. **验收（必做，白屏事故的哨兵）**：
   ```bash
   ssh liang@172.16.222.1 'grep -oE "assets/[^\"]+" <(curl -s http://127.0.0.1:56431/) | while read a; do printf "%s -> " $a; curl -s -o /dev/null -w "%{http_code} %{size_download}\n" http://127.0.0.1:56431/$a; done'
   ```
   每个 assets 必须 **200 且 size 与本地 dist 一致**（js ~950KB / css ~82KB）。
   返回 200 但 size=1775（index.html 大小）= SPA 兜底 = 文件实际缺失。

## 2. 换树提交点

构建验收通过后 `mv ~/alianggate-head ~/alianggate-head.old-$(date +%m%d) && mv ~/alianggate-head.new ~/alianggate-head`。

## 3. 白屏事故根因与铁律（2026-10-16）

**根因**：`.gitignore` 忽略 `app/website/dist`，但 `dist/index.html` 历史上被 git 跟踪过（ignore 不会 untrack，`git add -A` 还会持续更新它）。从 git 树组装的构建树里 dist 只剩陈旧 index.html、`assets/` 整个缺失，`//go:embed website/dist`（app/embed.go）因目录非空**照样构建成功**，产出引用不存在资源的白屏二进制；SPA 兜底又让缺失路径返回 200+index.html，肉眼难辨。每次前端重构 hash 变化，手工补 assets 的救火下次必然再断。

**铁律**：
1. `app/website/dist` 全目录**永不入 git**（index.html 已于 2026-10-16 `git rm --cached` 拔除）。dist 缺失时 go:embed 会直接构建失败——这是期望行为，不要绕过。
2. 打包必须「git 文件 + 整个 dist 目录」两条腿（§1.2），**禁止手挑文件清单**。
3. `scripts/check-dist.mjs` 是出包前硬闸，本地 build 已自动挂接；任何直连构建在 `go build` 前手动跑一遍。
4. 验收必须按 §1.6 逐 assets 核对 200+size，不许只看「页面能打开」。
