# COS 发布产物同步(publish-cos job)实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 打 tag 发布时,在 GitHub Release 之后把全部产物同步到腾讯云 COS(`releases/<tag>/` 永久归档 + `latest/` 固定路径)。

**Architecture:** 在 `.github/workflows/tag-build-release.yml` 末尾追加独立 job `publish-cos`(needs: release):重新下载 `aliang-*` artifacts、重算 SHA256SUMS、安装固定版 coscli(sha256 校验)、secrets 渲染临时配置文件、两次 `sync -r` + version.txt、HTTP HEAD 全量核对并输出下载 URL 到 Step Summary。不引入第三方 Action,不改任何 Go/前端代码。

**Tech Stack:** GitHub Actions、腾讯云 coscli v1.0.9(linux-amd64,sha256 固定校验)、COS 桶 `aliang-1305838434`(ap-nanjing,公有读)。

**规格文档:** `docs/superpowers/specs/2026-09-20-cos-release-publishing-design.md`(已批准;§7 的「coscli ls 核对数量」在计划中升级为 HTTP HEAD 全量核对——更强:同时验证对象数、公有读、字节数,且不依赖 coscli ls 输出格式)。

**发布链路事实(执行者必读):** 本仓库 origin 是 GitLab(`gitlab.liang.home:2222/aliang/aliang-client.git`),由 GitLab 服务端 push mirror 同步到 GitHub `aliang-one/aliang-client`,**Actions 在 GitHub 侧运行** → Secrets/Variables 必须配在 GitHub 仓库(Task 4)。合并/打 tag 流程与现状完全一致,本计划不改动它。

**测试策略说明(为何无传统单测):** 本特性是纯 CI-infra 变更,无应用代码。测试替代物:TDD 的「先写失败测试」对应 **Task 2 本地真实验证**——在写 workflow 代码之前,先用本机 coscli 真实演练「配置文件格式 → 上传 → 公有读下载 → 幂等 → 清理」全链路(规格 §10 T1,这是研究发现的最大风险点:`disableencryption` 字段);Task 3 的静态校验(actionlint + YAML 结构断言)对应「跑测试」;Task 5 是发布日端到端清单(规格 T2/T3,无法在合并前执行)。

---

## 文件结构

| 文件 | 操作 | 职责 |
|---|---|---|
| `.github/workflows/tag-build-release.yml` | 修改(末尾追加 ~100 行) | 新增 `publish-cos` job,唯一的代码变更 |
| `docs/superpowers/specs/2026-09-20-cos-release-publishing-design.md` | 移动(进入 worktree) | 已批准规格,随分支提交 |
| `docs/superpowers/plans/2026-09-21-cos-release-publishing.md` | 移动(进入 worktree) | 本计划,随分支提交 |

不创建任何脚本文件——全部逻辑内联在 workflow YAML 中(单一职责,无处复用,拆文件反增复杂度)。

**工作目录约定:** Task 1 之后,所有路径均相对 worktree 根 `.worktrees/cos-release-publishing/`(下称 `$WT`)。执行者应 `cd` 到 worktree 内工作。

---

### Task 1: 创建 worktree 分支并随分支提交文档

**Files:**
- Create(分支上):`docs/superpowers/specs/2026-09-20-cos-release-publishing-design.md`(从主工作区移入)
- Create(分支上):`docs/superpowers/plans/2026-09-21-cos-release-publishing.md`(从主工作区移入)

- [ ] **Step 1: 创建 worktree 与分支**

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/alianggate
git worktree add .worktrees/cos-release-publishing -b feat/cos-release-publishing
```

Expected: 输出 `branch 'feat/cos-release-publishing' set up to track...`/`Preparing worktree`(基于 master=v1.1.34,干净)。

- [ ] **Step 2: 移入规格与计划文档**

```bash
cd .worktrees/cos-release-publishing
mkdir -p docs/superpowers/specs docs/superpowers/plans
mv ../../docs/superpowers/specs/2026-09-20-cos-release-publishing-design.md docs/superpowers/specs/
mv ../../docs/superpowers/plans/2026-09-21-cos-release-publishing.md docs/superpowers/plans/
git status --short
git branch --show-current
```

Expected: `git status` 显示两个 `?? docs/superpowers/...`;分支名 `feat/cos-release-publishing`。注意:主工作区还有两对**其他功能**的未跟踪文档(2026-09-09、2026-09-21 agent-auth-recovery),**不要动它们**。

- [ ] **Step 3: 提交文档**

```bash
git add docs/superpowers/specs/2026-09-20-cos-release-publishing-design.md \
        docs/superpowers/plans/2026-09-21-cos-release-publishing.md
git commit -m "$(cat <<'EOF'
文档：COS 发布产物同步——设计规格与实现计划

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

Expected: 提交成功,报告 `2 files changed`(规格 + 计划两个新文件)。

---

### Task 2: 本地真实验证(T1,测试先行——必须先于 Task 3)

**目的:** 在写一行 CI 代码之前,用本机(macOS arm64)coscli 验证四个高风险事实:①手写配置文件 + `disableencryption: "true"` 能被正确读取;②密钥/桶/地域有效;③桶公有读;④上传/覆盖幂等。

**Files:**
- Create: `/tmp/cos-test/`(临时目录,不入库)

- [ ] **Step 1: 下载 darwin-arm64 coscli v1.0.9**

先确认资产名(GitHub release 资产列表为准,勿凭记忆猜文件名):

```bash
gh api repos/tencentyun/coscli/releases/tags/v1.0.9 --jq '.assets[].name'
```

Expected: 列表包含 `coscli-v1.0.9-darwin-arm64` 与 `sha256sum.log`。若 darwin 资产名不同,以下命令相应替换。

```bash
mkdir -p /tmp/cos-test
curl --fail --silent --show-error --location \
  -o /tmp/cos-test/coscli \
  https://github.com/tencentyun/coscli/releases/download/v1.0.9/coscli-v1.0.9-darwin-arm64
chmod +x /tmp/cos-test/coscli
/tmp/cos-test/coscli --version

# 额外:下载 linux-amd64 并核对 workflow 中硬编码的 sha256(合并前验证,而非首发日才发现)
curl --fail --silent --show-error --location \
  -o /tmp/cos-test/coscli-linux \
  https://github.com/tencentyun/coscli/releases/download/v1.0.9/coscli-v1.0.9-linux-amd64
shasum -a 256 /tmp/cos-test/coscli-linux | grep -c a07de5ba2800147a700ed29036b0c76a4229088cee68e1682d0eae19b638a915
```

Expected: `--version` 输出含 `v1.0.9`(若因版本打印方式不同而报错,记录现象即可,不阻塞——sha256 才是权威校验);`grep -c` 输出 `1`。若 GitHub 直连失败,备用镜像:`https://cosbrowser.cloud.tencent.com/software/coscli/coscli-darwin-arm64`(注意:该链接始终指向最新版,验证完 `--version` 若非 v1.0.9 需记录)。

- [ ] **Step 2: 写临时配置文件(用户协助录入密钥——密钥不得经过 AI 对话或写入代码)**

请用户在输入框用 `!` 前缀执行下面这一行(交互输入 SecretId/SecretKey,直接写入临时配置,不回显):

```
! mkdir -p /tmp/cos-test && read -rsp 'SecretId: ' SID && echo && read -rsp 'SecretKey: ' SK && echo && printf 'cos:\n  base:\n    secretid: %s\n    secretkey: %s\n    sessiontoken: ""\n    protocol: https\n    disableencryption: "true"\n  buckets:\n    - name: aliang-1305838434\n      region: ap-nanjing\n      endpoint: cos.ap-nanjing.myqcloud.com\n' "$SID" "$SK" > /tmp/cos-test/.cos.yaml && chmod 600 /tmp/cos-test/.cos.yaml && echo "config written"
```

Expected: 输出 `config written`。**若 `!` 环境下 stdin 非 TTY 导致 `read` 失败**(命令链在写文件前中断、无输出),退路:让用户用任意编辑器手写 `/tmp/cos-test/.cos.yaml`,内容模板如下(密钥两行由用户自行填入,不经过 AI 对话):

```yaml
cos:
  base:
    secretid: <用户填>
    secretkey: <用户填>
    sessiontoken: ""
    protocol: https
    disableencryption: "true"
  buckets:
    - name: aliang-1305838434
      region: ap-nanjing
      endpoint: cos.ap-nanjing.myqcloud.com
```

执行后立即验证文件结构(不打印密钥行):

```bash
grep -v 'secretid\|secretkey' /tmp/cos-test/.cos.yaml
```

Expected: 显示 yaml 其余行,缩进与计划 §「完整 YAML」中 job 内配置一致。

- [ ] **Step 3: 上传测试对象到 test/ 前缀**

```bash
echo "cos-t1-test $(date)" > /tmp/cos-test/hello.txt
/tmp/cos-test/coscli -c /tmp/cos-test/.cos.yaml cp /tmp/cos-test/hello.txt \
  "cos://aliang-1305838434/test/hello.txt"
/tmp/cos-test/coscli -c /tmp/cos-test/.cos.yaml ls "cos://aliang-1305838434/test/"
```

Expected: cp 成功无报错;ls 列出 `test/hello.txt`。**若报解密/密钥类错误** → `disableencryption` 写法问题,检查 yaml 键是否全小写、位置在 `cos.base` 下,修正后重试(这正是本任务要暴露的风险)。

- [ ] **Step 4: 验证公有读**

```bash
curl -fsS "https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/test/hello.txt"
```

Expected: 输出 hello.txt 内容,HTTP 200。**若 403** → 桶未开公有读:控制台 → 存储桶 `aliang-1305838434` → 权限管理 → 改「公有读,私有写」→ 重试本步(规格 §9)。

- [ ] **Step 5: 验证覆盖幂等**

```bash
/tmp/cos-test/coscli -c /tmp/cos-test/.cos.yaml cp /tmp/cos-test/hello.txt \
  "cos://aliang-1305838434/test/hello.txt" && echo "overwrite ok"
curl -fsS "https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/test/hello.txt"
```

Expected: 二次上传成功,内容不变。

- [ ] **Step 6: 清理测试对象与临时目录**

```bash
/tmp/cos-test/coscli -c /tmp/cos-test/.cos.yaml rm "cos://aliang-1305838434/test/hello.txt" --force
curl -sS -o /dev/null -w '%{http_code}\n' "https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/test/hello.txt"
rm -rf /tmp/cos-test
```

Expected: rm 成功;curl 返回 `404`;本地目录已删。若 `rm` 的 `--force` flag 不被识别,改用 `coscli rm "cos://aliang-1305838434/test/hello.txt"` 重试;仍失败则到控制台手动删除 `test/` 前缀(必须删净,桶是公有分发桶,不留垃圾对象)。

**Gate:本 Task 全部通过前,禁止进入 Task 3。**

(T1 实测结论:cp 无 `--force` flag,默认覆盖;sync 支持 `--force`/`-r`;rm 支持 `--force`。以上结论已回填至本计划与规格。)

---

### Task 3: 实现 publish-cos job 并静态校验

**Files:**
- Modify: `.github/workflows/tag-build-release.yml`(在文件末尾 `release` job 之后追加)

- [ ] **Step 1: 在 `tag-build-release.yml` 末尾追加完整 job**

追加内容(逐字使用,注意 heredoc `EOF` 单独成行、其内 yaml 缩进按原样保留):

```yaml

  publish-cos:
    name: Publish assets to Tencent COS
    needs: release
    runs-on: ubuntu-latest
    timeout-minutes: 15
    permissions:
      contents: read
      actions: read
    env:
      COS_BUCKET: ${{ vars.COS_BUCKET }}
      COS_REGION: ${{ vars.COS_REGION }}
      TAG: ${{ github.ref_name }}
    steps:
      - name: Check COS configuration
        env:
          COS_SECRET_ID: ${{ secrets.COS_SECRET_ID }}
          COS_SECRET_KEY: ${{ secrets.COS_SECRET_KEY }}
        shell: bash
        run: |
          missing=0
          for name in COS_BUCKET COS_REGION COS_SECRET_ID COS_SECRET_KEY; do
            if [ -z "$(printenv "$name")" ]; then
              echo "::error::$name 未配置(Settings → Secrets and variables → Actions)"
              missing=1
            fi
          done
          exit "$missing"

      - name: Download packaged artifacts
        uses: actions/download-artifact@v4
        with:
          pattern: aliang-*
          path: release-assets
          merge-multiple: true

      - name: Generate checksums
        shell: bash
        run: |
          cd release-assets
          sha256sum * > SHA256SUMS

      - name: Install coscli v1.0.9
        shell: bash
        run: |
          mkdir -p "$HOME/.local/bin"
          curl --fail --silent --show-error --location \
            -o "$HOME/.local/bin/coscli" \
            https://github.com/tencentyun/coscli/releases/download/v1.0.9/coscli-v1.0.9-linux-amd64
          echo 'a07de5ba2800147a700ed29036b0c76a4229088cee68e1682d0eae19b638a915  '"$HOME"'/.local/bin/coscli' | sha256sum -c -
          chmod +x "$HOME/.local/bin/coscli"
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"

      - name: Write temp coscli config
        env:
          COS_SECRET_ID: ${{ secrets.COS_SECRET_ID }}
          COS_SECRET_KEY: ${{ secrets.COS_SECRET_KEY }}
        shell: bash
        run: |
          COS_CFG="$(mktemp -d)/.cos.yaml"
          cat > "$COS_CFG" <<EOF
          cos:
            base:
              secretid: ${COS_SECRET_ID}
              secretkey: ${COS_SECRET_KEY}
              sessiontoken: ""
              protocol: https
              disableencryption: "true"
            buckets:
              - name: ${COS_BUCKET}
                region: ${COS_REGION}
                endpoint: cos.${COS_REGION}.myqcloud.com
          EOF
          echo "COS_CFG=$COS_CFG" >> "$GITHUB_ENV"

      - name: Sync versioned prefix
        shell: bash
        run: coscli -c "$COS_CFG" sync release-assets/ "cos://${COS_BUCKET}/releases/${TAG}/" -r --force

      - name: Sync latest prefix
        shell: bash
        run: coscli -c "$COS_CFG" sync release-assets/ "cos://${COS_BUCKET}/latest/" -r --force

      - name: Write latest version marker
        shell: bash
        run: |
          echo "${TAG}" > version.txt
          coscli -c "$COS_CFG" cp version.txt "cos://${COS_BUCKET}/latest/version.txt"

      - name: Verify public URLs and summarize
        shell: bash
        run: |
          base="https://${COS_BUCKET}.cos.${COS_REGION}.myqcloud.com"
          fail=0
          for f in release-assets/* version.txt; do
            name="$(basename "$f")"
            local_size="$(stat -c%s "$f")"
            headers="$(curl -fsSI "${base}/latest/${name}")" || { echo "::error::HEAD failed: ${base}/latest/${name}(检查桶公有读)"; fail=1; continue; }
            remote_size="$(printf '%s\n' "$headers" | grep -i '^content-length:' | tail -1 | tr -dc '0-9' || true)"
            if [ "${remote_size}" != "${local_size}" ]; then
              echo "::error::size mismatch ${name}: local=${local_size} remote=${remote_size}"
              fail=1
            fi
          done
          curl -fsSI "${base}/releases/${TAG}/SHA256SUMS" > /dev/null || { echo "::error::releases/${TAG}/SHA256SUMS 不可访问"; fail=1; }
          [ "$fail" -eq 0 ] || exit 1
          {
            echo "## COS 发布完成:${TAG}"
            echo
            echo "固定最新版前缀:\`${base}/latest/\`"
            echo
            echo "| 产物 | latest URL |"
            echo "|---|---|"
            for f in release-assets/* version.txt; do
              name="$(basename "$f")"
              echo "| ${name} | ${base}/latest/${name} |"
            done
          } >> "$GITHUB_STEP_SUMMARY"
```

设计要点(实现者勿"优化"掉):`-c` 全局 flag 在子命令前;`sync -r` 必须有(默认不递归);`sync` 加 `--force`(不提示确认,防 CI 挂死,与覆盖无关);`cp` 勿加 `--force`(无此 flag,同名覆盖即默认);secrets 只出现在 step `env` 与临时文件,不进命令行参数、不进 `if:`;`mktemp -d` 配置用完即弃(runner 销毁,无需清理步骤);`SHA256SUMS` 算法与 release job 逐字相同 → 输出一致;校验用 HTTP HEAD 而非 `coscli ls`(见计划头部说明)。

- [ ] **Step 2: 安装 actionlint 并校验**

```bash
brew install actionlint
actionlint .github/workflows/tag-build-release.yml
```

Expected: 退出码 0,无输出(或仅有与本次改动无关的既有告警——若有,先在改动前的文件上跑一遍对比,确认非新增)。若 brew 安装失败,退路:`ruby -ryaml -e 'YAML.load_file(".github/workflows/tag-build-release.yml"); puts "yaml ok"'` 只验语法(不验表达式)。

- [ ] **Step 3: 结构断言**

```bash
ruby -ryaml -e 'y=YAML.load_file(".github/workflows/tag-build-release.yml"); puts y["jobs"].keys.join(",")'
grep -n "needs: release" .github/workflows/tag-build-release.yml
```

Expected: 第一条输出含 `guard,frontend,build,release,publish-cos`(顺序不限,5 个都在);第二条**恰命中 1 行**——`publish-cos` job 的 `needs: release`(release job 自身是 `needs: build`,不会命中)。

- [ ] **Step 4: 审阅 diff**

```bash
git diff --stat
git diff .github/workflows/tag-build-release.yml
```

Expected: 仅 1 个文件变更,纯追加(文件头部/既有 job 零改动),新增块首行是空行 + `  publish-cos:`(与 `release:` 同级 2 空格缩进)。

- [ ] **Step 5: 提交**

```bash
git add .github/workflows/tag-build-release.yml
git commit -m "$(cat <<'EOF'
新增：发布产物同步腾讯云 COS——tag-build-release 追加 publish-cos job

release 发布后新增独立 job：固定版 coscli（sha256 校验）+ secrets 渲染临时
配置（disableencryption 明文）+ sync 幂等同步 releases/<tag>/ 与 latest/
双前缀 + version.txt 标记 + HTTP HEAD 全量核对（公有读/字节数），下载 URL
写入 Step Summary。密钥仅经 env 与临时文件流转，不进命令行与 if 表达式。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Sonnet 4.5 <noreply@anthropic.com>
EOF
)"
```

Expected: 提交成功,`git log --oneline -3` 可见两个新提交(文档 + workflow)。

---

### Task 4: GitHub 仓库配置 Secrets/Variables(人工操作,浏览器)

**位置:https://github.com/aliang-one/aliang-client/settings/secrets/actions(需 aliang-one 账号)**

- [ ] **Step 1: 添加 Secrets(Tab: Secrets)**

| Name | Value |
|---|---|
| `COS_SECRET_ID` | 腾讯云 SecretId(建议用 CAM 子账号密钥,最小权限:该桶的 `cos:PutObject/GetObject/HeadObject/GetBucket/ListBucket`;若直接用主账号密钥,自担风险) |
| `COS_SECRET_KEY` | 对应 SecretKey |

- [ ] **Step 2: 添加 Variables(Tab: Variables)**

| Name | Value |
|---|---|
| `COS_BUCKET` | `aliang-1305838434` |
| `COS_REGION` | `ap-nanjing` |

- [ ] **Step 3: 验证配置存在**

```bash
gh secret list -R aliang-one/aliang-client
gh variable list -R aliang-one/aliang-client
```

Expected: secrets 列表含 `COS_SECRET_ID`/`COS_SECRET_KEY`(值不可见,只看名字);variables 列表含 `COS_BUCKET=aliang-1305838434`、`COS_REGION=ap-nanjing`。若 gh token(liangsqrt)无该 repo admin 权限报 404/403,则浏览器页面目视确认即可。

**顺序警告:本 Task 必须在分支合并进 master(经镜像到 GitHub)之前完成。** 否则下一个 tag 发布时 `publish-cos` 会在第一步报错退出(GitHub Release 不受影响,但 run 标红)。

---

### Task 5: 发布日端到端验证清单(T2/T3,合并后首次打 tag 时执行)

本任务**无法在合并前执行**(依赖真实 tag 触发),执行计划时原样转交用户,作为下次发版的 checklist:

- [ ] **Step 1: 观察 Actions run** — https://github.com/aliang-one/aliang-client/actions 中 tag 对应 run 的 `publish-cos` job 绿色,Summary 含「COS 发布完成:<tag>」与 12 行 URL 表(11 产物 + version.txt)。

- [ ] **Step 2: 全量下载校验(latest/)**

```bash
mkdir -p /tmp/cos-verify && cd /tmp/cos-verify
base="https://aliang-1305838434.cos.ap-nanjing.myqcloud.com"
curl -fsSLO "${base}/latest/SHA256SUMS"
for f in aliang-linux-amd64.tar.gz aliang-linux-amd64.deb \
         aliang-linux-arm64.tar.gz aliang-linux-arm64.deb \
         aliang-windows-amd64.zip aliang-windows-amd64.msi \
         aliang-darwin-amd64.tar.gz aliang-darwin-amd64.pkg \
         aliang-darwin-arm64.tar.gz aliang-darwin-arm64.pkg; do
  curl -fsSLO "${base}/latest/${f}"
done
sha256sum -c SHA256SUMS
curl -fsS "${base}/latest/version.txt"   # 期望输出:当前 tag
cd - && rm -rf /tmp/cos-verify
```

Expected: `sha256sum -c` 输出 10 行 `OK`(SHA256SUMS 自身不在校验清单内——glob 在重定向创建文件之前展开,故 `sha256sum *` 不会把 SHA256SUMS 算进去);version.txt 输出当前 tag。

- [ ] **Step 3: 版本归档抽查** — `curl -fsSI "${base}/releases/<tag>/aliang-darwin-arm64.pkg"` 返回 200,确认 `releases/<tag>/` 前缀同步成功。

- [ ] **Step 4: 幂等验证(T3)** — 在 Actions 页对同一次 run 手动 **Re-run** `publish-cos` job,期望明显快于首次(crc64 命中全部跳过),Summary 重新生成,`latest/` 内容不变。

---

### Task 6: 收尾汇报与推送门控

- [ ] **Step 1: 汇总分支状态**

```bash
git log --oneline master..HEAD
git status --short
```

Expected: 2 个提交(文档 + workflow),工作区干净。

- [ ] **Step 2: 向用户汇报并等待决定(不得自行 push)**

汇报内容:两个提交、Task 4 是否已配置、Task 5 checklist 转交。然后询问用户:是否 `git push -u origin feat/cos-release-publishing` 到 GitLab 并开 MR(或用户偏好的合并方式)。**合并顺序:先确认 Task 4 已完成,再合并。** 合并进 master 经镜像到 GitHub 后,打 tag 即全链路生效;流程与 v1.1.34 完全一致。

---

## 已知限制与后续(规格 §11)

- 桶公有读 = 全网可匿名下载桶内所有对象(分发用途可接受,严禁放凭据类文件)
- `releases/` 只增不删,无生命周期规则(量级:~165MB/版本;后续可用 COS 生命周期规则把 90 天前对象转低频存储)
- 两个 tag 极短间隔并发发布时 `latest/` 终态为后完成者(规格 §7 已声明,不加并发控制)
