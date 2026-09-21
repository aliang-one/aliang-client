# 设计:发布产物推送腾讯云 COS

- 日期:2026-09-20
- 状态:已与用户逐节确认
- 相关文件:`.github/workflows/tag-build-release.yml`、`.github/workflows/cleanup-releases.yml`

## 1. 背景与目标

当前发布流程:push tag → `tag-build-release.yml` 构建 5 平台产物(linux amd64/arm64、windows amd64、macOS amd64/arm64;压缩包 + deb/pkg/msi)→ `release` job 生成 SHA256SUMS 并发布 GitHub Release → 每日 `cleanup-releases.yml` 清理旧 Release 只留最新。

问题:产物唯一存放点是 GitHub Release,国内下载慢且单点。

目标:打 tag 发布时,在 GitHub Release 发布之后,**追加**把全部产物同步到腾讯云 COS 的固定前缀 `software/`(覆盖式,桶内始终只保留最新一套产物),下载 URL 形如 `https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/software/aliang-linux-amd64.tar.gz`。

GitHub 现有流程零改动,两边并存。

## 2. 范围

**范围内**

- `tag-build-release.yml` 新增 `publish-cos` job
- GitHub 仓库 Secrets/Variables 配置清单
- 桶目录结构、公开访问 URL 约定
- 本地预演验证方案 + 首次发布验证清单

**范围外(后续可另立项)**

- CDN 加速域名接入
- COS 生命周期规则(旧版本转低频/归档存储)
- 应用内更新逻辑改造(当前代码无任何 GitHub Release 下载引用,本设计不触碰应用代码)

## 3. 关键技术事实(已经源码/官方文档交叉核验)

实现时**不要再按网上教程**做以下几件事,均已被证伪:

1. **`coscli` 不支持 `COS_SECRET_ID`/`COS_SECRET_KEY` 环境变量认证**。源码 `cmd/root.go` 无任何密钥环境变量读取(该写法属于 hadoop-cos/老 Python coscmd 生态)。认证只有两条路:命令行 flag(`-i`/`-k`/`-e`)或 YAML 配置文件(默认 `~/.cos.yaml`,或 `-c` 指定路径,必须 `.yaml` 结尾)。CI 选配置文件方案。
2. **手写配置文件必须加 `disableencryption: "true"`**(cos.base 下,yaml key 全小写)。coscli 默认把配置文件里的密钥当 AES 密文解密(硬编码密钥 `coscli-secret`,仅为混淆),手写明文不加此字段会读出错值。
3. **网上流传的 `tencentyun/cos-action` 仓库不存在(404,正确 org 是 `TencentCloud`)**;而真实存在的 `TencentCloud/cos-action` 用已废弃的 node12 运行时且停更,大概率跑不起来。不引入任何第三方 Action,直接调用官方 CLI。
4. **`sync` 上传目录必须加 `-r`**(默认不递归,与 ossutil 不同);**`sync` 须加 `--force`**(跳过交互确认,防 CI 挂死)。`cp` 没有 `--force` flag(T1 实测报 `unknown flag: --force`),同名覆盖即其默认行为(仅 `--forbid-overwrite` 能禁止),无需任何额外 flag。
5. `sync` 幂等机制:按同名对象 crc64 比对,相同则跳过,重跑安全。
6. **coscli 固定版本 v1.0.9**(2026-08-25 发布),sha256:`a07de5ba2800147a700ed29036b0c76a4229088cee68e1682d0eae19b638a915`,二进制约 14MB。从 GitHub releases 固定 URL 下载(勿用国内 CDN 固定链接,它始终指向最新版、不可复现;也从美国 runner 拉国内 CDN 慢)。
   下载地址:`https://github.com/tencentyun/coscli/releases/download/v1.0.9/coscli-v1.0.9-linux-amd64`
7. `cos://` 后跟桶全名(`name-appid`)或已注册别名;桶条目写入配置文件(region/endpoint 字段)后无需再传 `-e`。不配 alias 时,alias 默认等于桶全名。
8. coscli 自带错误重试(`--err-retry-num` 默认 5);约 30MB 单文件不触发分块,直接 PUT。

来源:官方文档 `cloud.tencent.com/document/product/436/63144`(安装配置)、`436/63669`(cp)、`436/63670`(sync)、`436/71763`(通用选项),源码 `github.com/tencentyun/coscli`(`cmd/root.go`、`cmd/sync.go`、`util/types.go`、`util/secret.go`)。

## 4. 腾讯云侧现状与前提

| 项 | 值 |
|---|---|
| 桶名(APPID 后缀) | `aliang-1305838434` |
| 地域 | `ap-nanjing`(南京) |
| Endpoint | `cos.ap-nanjing.myqcloud.com` |
| 访问权限 | 需为**公有读私有写**(用户已确认桶用于分发下载;开启/核对步骤见 §9) |

下载 URL 形态:

```
https://aliang-1305838434.cos.ap-nanjing.myqcloud.com/software/<asset>
```

## 5. GitHub 仓库配置(Settings → Secrets and variables → Actions)

| 类型 | 名称 | 内容 | 示例 |
|---|---|---|---|
| Secret | `COS_SECRET_ID` | 腾讯云 SecretId | AKID… |
| Secret | `COS_SECRET_KEY` | 腾讯云 SecretKey | … |
| Variable | `COS_BUCKET` | 桶全名(含 APPID) | `aliang-1305838434` |
| Variable | `COS_REGION` | 地域 | `ap-nanjing` |

桶名/地域非敏感,放 Variables(日志可见、便于排查);仅密钥放 Secrets(日志自动掩码)。建议密钥使用**子账号(CAM)最小权限**:仅需该桶的 `cos:PutObject`/`cos:GetObject`/`cos:HeadObject`/`cos:GetBucket`/`cos:ListBucket`,不用主账号密钥。

## 6. 桶目录结构

```
cos://aliang-1305838434/
└── software/            ← 覆盖式,桶内始终只保留最新一套产物
    ├── aliang-linux-amd64.tar.gz
    ├── aliang-linux-amd64.deb
    ├── aliang-linux-arm64.tar.gz
    ├── aliang-linux-arm64.deb
    ├── aliang-windows-amd64.zip
    ├── aliang-windows-amd64.msi
    ├── aliang-darwin-amd64.tar.gz
    ├── aliang-darwin-amd64.pkg
    ├── aliang-darwin-arm64.tar.gz
    ├── aliang-darwin-arm64.pkg
    ├── SHA256SUMS
    └── version.txt       ← 单行文本,内容为当前 tag(如 v1.1.33)
```

每次发布共产出 12 个对象(10 产物 + SHA256SUMS + version.txt)。`version.txt` 供自动化探测最新版本号与发布完整性核对。不保留历史版本(用户决策,2026-09-21);旧版本回溯依赖 GitHub Release 清理前的窗口期或另行归档。

## 7. Workflow 改动设计

`.github/workflows/tag-build-release.yml` 末尾新增独立 job,链在 `release` 之后。tag 不在 master 时,`build`/`release` 被跳过,`publish-cos` 随上游自动跳过,无需重复写 `if`。

```yaml
  publish-cos:
    name: Publish assets to Tencent COS
    needs: release
    runs-on: ubuntu-latest
    steps:
      - name: Check COS configuration       # secrets/vars 缺失时立即失败并给明确提示
                                              # (不能写在 if: 里,secrets 不允许用于 if 表达式)
      - name: Download packaged artifacts    # actions/download-artifact@v4
                                              # pattern: aliang-*, merge-multiple: true, path: release-assets
                                              # (与 release job 完全相同,website-dist 天然被 pattern 排除)
      - name: Generate checksums             # cd release-assets && sha256sum * > SHA256SUMS
                                              # 与 release job 相同算法、相同文件集 → 结果逐字节一致
      - name: Install coscli v1.0.9          # curl 固定版本 URL + sha256 校验 + chmod +x
      - name: Write temp coscli config       # mktemp -d 下生成 .cos.yaml;
                                              # secrets 经 env 注入 heredoc(不在 if/命令行参数中出现);
                                              # 含 disableencryption: "true";
                                              # 桶条目: name=aliang-1305838434, region=ap-nanjing,
                                              #          endpoint=cos.ap-nanjing.myqcloud.com(不配 alias)
      - name: Sync to software/              # coscli -c $CFG sync release-assets/ cos://桶/software/ -r --force
      - name: Write software/version.txt     # echo $TAG > version.txt 后单文件 cp(默认覆盖,cp 无 --force)
      - name: Verify and summarize           # coscli ls cos://桶/software/ 核对对象数(应为 12:10 产物+SHA256SUMS+version.txt);
                                              # 把全部公开下载 URL 写入 GITHUB_STEP_SUMMARY
```

命令细节约定:

- 所有 coscli 命令统一 `-c "$COS_CFG"` 指向临时配置(不写 `~/.cos.yaml`,避免 runner 状态依赖)
- 密钥只进临时文件与进程内存,不进命令行参数、不出现在日志
- `sync` 不带 `--delete`:同名对象按默认覆盖语义更新(产物文件名固定,无增量残留)
- 并发边界:短时间连续打两个 tag 时,两次 `publish-cos` 可能交错写 `software/`,终态为后完成者;当前单人发布节奏下不构成实际问题,不为此加并发控制

## 8. 错误处理

| 场景 | 行为 |
|---|---|
| secrets/vars 未配置 | 首步显式失败,报错信息指明缺哪个配置项 |
| coscli 下载/sha256 校验失败 | 步骤失败,job 标红 |
| 单文件上传失败 | coscli 内置重试 5 次;最终失败 → job 标红 |
| COS 整体失败 | **GitHub Release 不受影响**(已先行发布);run 标红引人注意;修复后在 Actions 页单重跑 `publish-cos` job,sync 按 crc64 幂等续传 |
| 重跑同版本 | 幂等:crc64 相同的对象跳过,`software/` 覆盖为同内容 |
| 桶非公有读 | 上传仍成功,但下载 URL 返回 403 → 依赖 §10 验证清单暴露 |

不做告警系统集成;job 红绿即状态。

## 9. 公有读开启/核对(一次性,人工操作)

1. 控制台 → 对象存储 → 存储桶 `aliang-1305838434` → 权限管理
2. 「存储桶访问权限」确认为 **公有读,私有写**;若是「私有读写」改为公有读并保存
3. 核对方法:浏览器直接打开任一对象 URL 应能下载;未上传产物前可传一个测试对象验证
4. 风险说明:公有读意味着桶内所有对象对全网可匿名下载(本产品安装包本就是公开发布物,可接受);**不要**把任何凭据类文件放进该桶

## 10. 测试计划

**T1 本地预演(实现阶段,发布前)**

1. 本机(macOS)装 darwin 版 coscli v1.0.9,用同一套密钥、同样结构的临时配置文件
2. 向 `test/` 前缀试传小文件 → `ls` 核对 → 下载校验 → `rm` 清理
3. 重点验证:配置文件 `disableencryption: "true"` 写法、明文密钥可被正确读取(研究发现的最大易错点)

**T2 首次真实发布后验证**

1. curl 全部 12 个对象 URL(`software/` 前缀:10 产物 + SHA256SUMS + version.txt),断言 HTTP 200 且字节数与 GitHub Release 资产一致
2. 下载 `software/SHA256SUMS` 全量 `sha256sum -c`
3. `curl software/version.txt` 输出当前 tag

**T3 幂等验证**

在 Actions 页对 `publish-cos` 重跑一次,确认秒级完成(全部跳过)且 `software/` 内容不变。

## 11. 后续可选(不在本期)

- CDN 加速域名绑定 `software/`
- 若未来需要历史版本回溯,再增加 `releases/<tag>/` 归档前缀(当前按用户决策不保留)
- 在 README/官网放固定 `software/` 下载链接
