# SSH 入口一键生成并启用(keygen)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一键完成「生成 ed25519 密钥对 → 公钥入列 → 自动开启并保存 → 私钥弹窗展示+下载」,把新装用户启用 SSH 入口的门槛压到一个按钮。

**Architecture:** 后端新增无状态 keygen 端点(双注册 core/user-agent),生成纯函数放 inbound/ssh(与 host key 同款套路),私钥只返回一次不落盘;公钥入库仍走既有 `/api/config/customer` 保存链(单写路径不动)。前端卡片加一键按钮+私钥一次性弹窗+折叠态自动展开;回流语义 v3:error 一概不挂 watch,keygenPending 跨保存失败保留,success 回流时校验公钥在列才弹窗(防恢复链吞私钥/无主私钥交付)。

**Tech Stack:** Go(x/crypto/ssh 已在依赖)、Vue 2 式 Options API SFC、vitest 2、go test。

**Spec:** `docs/superpowers/specs/2026-11-12-ssh-ingress-oneclick-keygen-design.md`(实施前先读,本计划不重复其 rationale)

**工作目录:** 本 worktree(`.claude/worktrees/ssh-ingress-oneclick-keygen`,分支 feat/ssh-ingress-oneclick-keygen)。所有命令都在 worktree 根执行。

---

## 环境事实(执行者必读)

- Go 测试:`go test ./<pkg>/ -run <Name> -v`;全量 `go build ./... && go test ./...`
- 前端测试:在 `app/website/` 下 `npx vitest run src/components/settings/SshIngressSettings.test.js`
- 前端测试范式(照抄现有文件头):mock `../../i18n`(t 原样回键)、`vi.stubGlobal('fetch', ...)`,jsdom 坑见测试文件内注释(checkbox 不发 change、多 app 并存时用 data 属性选择器)
- 提交信息全中文,标题用「新增:/测试:/文档:」前缀,正文说明动机与关键决策
- gossh = `golang.org/x/crypto/ssh`,import 别名 `gossh "golang.org/x/crypto/ssh"`

---

### Task 1: inbound/ssh 生成函数 GenerateIngressKey

**Files:**
- Create: `inbound/ssh/keygen.go`
- Test: `inbound/ssh/keygen_test.go`

- [ ] **Step 1: 写失败测试**

创建 `inbound/ssh/keygen_test.go`:

```go
package ssh

import (
	"encoding/pem"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// 一键生成人用密钥(spec 2026-11-12 §1.1/§4):格式可解析、公私互证、
// 熵独立、文件名保守清洗。
func TestGenerateIngressKey(t *testing.T) {
	key, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("GenerateIngressKey: %v", err)
	}

	// 公钥行:gossh.ParseAuthorizedKey 可解析,注释前缀可辨
	pub, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		t.Fatalf("public key line unparseable: %v", err)
	}
	if !strings.HasPrefix(comment, "aliang-ingress-") {
		t.Fatalf("comment = %q, want prefix aliang-ingress-", comment)
	}

	// 私钥 PEM:ParsePrivateKey 回读,指纹与公钥互证
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		t.Fatalf("private key PEM undecodable")
	}
	priv, err := gossh.ParsePrivateKey([]byte(key.PrivateKey))
	if err != nil {
		t.Fatalf("private key unparseable: %v", err)
	}
	if gossh.FingerprintSHA256(priv.PublicKey()) != gossh.FingerprintSHA256(pub) {
		t.Fatalf("pubkey/privkey fingerprint mismatch")
	}
	if key.Fingerprint != gossh.FingerprintSHA256(pub) {
		t.Fatalf("Fingerprint field = %q, want %q", key.Fingerprint, gossh.FingerprintSHA256(pub))
	}
	if key.Algorithm != pub.Type() {
		t.Fatalf("Algorithm = %q, want %q", key.Algorithm, pub.Type())
	}

	// 文件名:无路径分隔符、.pem 后缀
	if key.SuggestedFilename == "" ||
		strings.ContainsAny(key.SuggestedFilename, `/\`) ||
		!strings.HasSuffix(key.SuggestedFilename, ".pem") {
		t.Fatalf("SuggestedFilename = %q", key.SuggestedFilename)
	}
}

func TestGenerateIngressKeyEntropyIndependent(t *testing.T) {
	a, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if a.PublicKey == b.PublicKey {
		t.Fatalf("two calls produced identical public keys")
	}
}

// hostname 清洗:特殊字符折叠为 '-',清洗后为空回落 "node"(防空段/前导破折号)。
func TestGenerateIngressKeyFilenameSanitize(t *testing.T) {
	if got := sanitizeIngressHost(`bad host/..\\x`); got != strings.Trim(`bad-host-..-x`, "-") && got != "node" {
		t.Fatalf("sanitizeIngressHost = %q", got)
	}
	if got := sanitizeIngressHost("///"); got != "node" {
		t.Fatalf("all-unsafe hostname should fall back to node, got %q", got)
	}
	if got := sanitizeIngressHost("My-PC_01"); got != "My-PC_01" {
		t.Fatalf("safe hostname must be kept verbatim, got %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./inbound/ssh/ -run TestGenerateIngressKey -v`
Expected: 编译失败,`undefined: GenerateIngressKey` / `undefined: sanitizeIngressHost`

- [ ] **Step 3: 最小实现**

创建 `inbound/ssh/keygen.go`:

```go
package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// GeneratedIngressKey 是「一键生成并启用」的人用密钥对载荷(spec
// 2026-11-12 §1.1)。json tag 一律 snake_case,与 hostkey 载荷同口径
// (handler 测试断言线格式字段名,防 Go 字段名直出)。
type GeneratedIngressKey struct {
	PublicKey         string `json:"public_key"`         // authorized_keys 单行,含注释尾巴
	PrivateKey        string `json:"private_key"`        // OpenSSH PEM 私钥块原文
	Fingerprint       string `json:"fingerprint"`        // SHA256:... base64(与 host key 指纹同口径)
	Algorithm         string `json:"algorithm"`          // 恒 "ssh-ed25519"
	Comment           string `json:"comment"`            // aliang-ingress-<hostname>-<时间戳>
	SuggestedFilename string `json:"suggested_filename"` // aliang-ingress-<hostname>-<日期>.pem
}

// ingressFilenameUnsafe 把 hostname 里不适合进文件名/Content-Disposition 的
// 字符折叠为 '-'(保守白名单:字母数字点下划线破折号)。
var ingressFilenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeIngressHost 清洗 hostname 供注释与文件名使用:非法段折叠 '-',
// 清洗后为空(全非法字符/纯分隔符)回落 "node",并掐掉首尾 '-'。
func sanitizeIngressHost(host string) string {
	host = strings.TrimSpace(host)
	cleaned := ingressFilenameUnsafe.ReplaceAllString(host, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "node"
	}
	return cleaned
}

// GenerateIngressKey 生成一对 ed25519 人用密钥:公钥编为标准 authorized_keys
// 单行(注释 aliang-ingress-<hostname>-<时间戳>,密钥列表里一眼可辨),私钥
// 编为 OpenSSH PEM。与 generateHostKey 同源同款套路(ed25519+MarshalPrivateKey)。
// 纯函数无副作用:不读不写任何文件、不碰 config、不计数——调用方(端点)
// 只把结果返回一次,私钥服务端不落盘(spec §2 已拍板,丢了重新生成)。
func GenerateIngressKey() (*GeneratedIngressKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("ingress key signer: %w", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "aliang ingress user key")
	if err != nil {
		return nil, fmt.Errorf("marshal ingress key: %w", err)
	}

	host := "node"
	if h, herr := os.Hostname(); herr == nil && strings.TrimSpace(h) != "" {
		host = sanitizeIngressHost(h)
	}
	now := time.Now()
	comment := fmt.Sprintf("aliang-ingress-%s-%s", host, now.Format("20060102150405"))
	pubLine := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer.PublicKey()))) + " " + comment

	return &GeneratedIngressKey{
		PublicKey:         pubLine,
		PrivateKey:        string(pem.EncodeToMemory(block)),
		Fingerprint:       gossh.FingerprintSHA256(signer.PublicKey()),
		Algorithm:         signer.PublicKey().Type(),
		Comment:           comment,
		SuggestedFilename: fmt.Sprintf("aliang-ingress-%s-%s.pem", host, now.Format("20060102")),
	}, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./inbound/ssh/ -run TestGenerateIngressKey -v`
Expected: 3 个测试全 PASS

- [ ] **Step 5: 提交**

```bash
git add inbound/ssh/keygen.go inbound/ssh/keygen_test.go
git commit -m "$(cat <<'EOF'
新增:SSH 入口人用密钥生成 GenerateIngressKey——ed25519 纯函数零落盘

一键生成并启用的后端产料函数(spec 2026-11-12 §1.1):公钥编标准
authorized_keys 行(注释 aliang-ingress-<host>-<时间戳>),私钥 OpenSSH
PEM 只在返回值中出现一次,服务端不落盘不托管;hostname 保守清洗防
Content-Disposition 注入。与 generateHostKey 同源同款套路。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: keygen 端点 handler

**Files:**
- Create: `app/http/handlers/ssh_ingress_keygen_handler.go`
- Test: `app/http/handlers/ssh_ingress_keygen_handler_test.go`

- [ ] **Step 1: 写失败测试**

创建 `app/http/handlers/ssh_ingress_keygen_handler_test.go`:

```go
package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// keygen 端点(spec 2026-11-12 §1.2/§4):POST-only 405、Success 信封、
// snake_case 线格式六字段、熵独立。
func TestSSHIngressKeygenHandler(t *testing.T) {
	t.Run("POST returns snake_case wire envelope", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		rec := httptest.NewRecorder()
		h.HandleKeygen(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh-ingress/keygen", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("POST code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Code int `json:"code"`
			Data struct {
				Key struct {
					PublicKey         string `json:"public_key"`
					PrivateKey        string `json:"private_key"`
					Fingerprint       string `json:"fingerprint"`
					Algorithm         string `json:"algorithm"`
					Comment           string `json:"comment"`
					SuggestedFilename string `json:"suggested_filename"`
				} `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		key := resp.Data.Key
		// snake_case 断言即上面 struct tag 本身——空串=tag 直出失败/字段缺失
		if key.PublicKey == "" || key.PrivateKey == "" || key.Fingerprint == "" ||
			key.Algorithm == "" || key.Comment == "" || key.SuggestedFilename == "" {
			t.Fatalf("wire fields empty: %+v", key)
		}
		if _, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey)); err != nil {
			t.Fatalf("public_key line unparseable: %v", err)
		}
	})

	t.Run("two POSTs produce independent keys", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		read := func() string {
			rec := httptest.NewRecorder()
			h.HandleKeygen(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh-ingress/keygen", nil))
			var resp struct {
				Data struct {
					Key struct {
						PublicKey string `json:"public_key"`
					} `json:"key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			return resp.Data.Key.PublicKey
		}
		if read() == read() {
			t.Fatalf("two POSTs produced identical public keys")
		}
	})

	t.Run("non-POST is 405 via http.Error", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := httptest.NewRecorder()
			h.HandleKeygen(rec, httptest.NewRequest(method, "/api/proxy/ssh-ingress/keygen", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s code = %d, want 405", method, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Method not allowed") {
				t.Fatalf("%s body = %q, want http.Error plain text", method, rec.Body.String())
			}
		}
	})
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./app/http/handlers/ -run TestSSHIngressKeygenHandler -v`
Expected: 编译失败,`undefined: NewSSHIngressKeygenHandler`

- [ ] **Step 3: 最小实现**

创建 `app/http/handlers/ssh_ingress_keygen_handler.go`:

```go
package handlers

import (
	"net/http"

	"aliang.one/nursorgate/app/http/common"
	sshin "aliang.one/nursorgate/inbound/ssh"
)

// SSHIngressKeygenHandler 一键生成 SSH 入口人用密钥对
// (POST /api/proxy/ssh-ingress/keygen,spec 2026-11-12 §1.2):
// 纯生成不落盘——私钥只在本次响应里出现一次,服务端内存即弃;
// 公钥入库仍走 /api/config/customer 保存链,本端点不碰配置(单写路径)。
// 无状态,两进程(core/user-agent)各持实例等价,与 hostkey/known-hosts
// handler 同惯例。双注册另一处 agentruntime server.go registerAgentRoutes
// (缺任一侧=404 透传,2026-10-21 教训;parity 测试
// app/agentruntime/route_parity_test.go 为护栏)。
type SSHIngressKeygenHandler struct{}

// NewSSHIngressKeygenHandler 无字段构造(端点无状态)。
func NewSSHIngressKeygenHandler() *SSHIngressKeygenHandler {
	return &SSHIngressKeygenHandler{}
}

// HandleKeygen POST=密钥信封;非 POST 一律 405(http.Error 直传状态码,
// 同 SSHIngressHandler.HandleHostkey 先例——走 common.Error 业务码会被
// ErrorCodeToHTTPStatus 折成 500)。
func (h *SSHIngressKeygenHandler) HandleKeygen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key, err := sshin.GenerateIngressKey()
	if err != nil {
		// 环境级故障(rand/编码),报错优于编造
		common.ErrorInternalServer(w, "Failed to generate SSH ingress key", map[string]interface{}{"error": err.Error()})
		return
	}
	common.Success(w, map[string]interface{}{"key": key})
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./app/http/handlers/ -run TestSSHIngressKeygenHandler -v`
Expected: 3 个子测试全 PASS

- [ ] **Step 5: 提交**

```bash
git add app/http/handlers/ssh_ingress_keygen_handler.go app/http/handlers/ssh_ingress_keygen_handler_test.go
git commit -m "$(cat <<'EOF'
新增:keygen 端点 handler——POST-only 405/Success 信封/snake_case 线格式

POST /api/proxy/ssh-ingress/keygen(spec 2026-11-12 §1.2):无状态纯生成,
私钥只在响应出现一次;非 POST 走 http.Error 真实 405(绕开业务码映射折 500
的坑,同 hostkey handler 先例);测试钉死六字段 snake_case 线格式与熵独立。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 双注册(core routes.go + user-agent registerAgentRoutes)

**Files:**
- Modify: `app/http/routes/routes.go:21`(Handlers struct)、`routes.go:80` 附近(newHandlers)、`routes.go:133` 附近(register 行)
- Modify: `app/agentruntime/server.go:207` 附近(registerAgentRoutes)

- [ ] **Step 1: core 侧三处接线**

`routes.go`:

1. Handlers struct 在 `SSHIngress` 行后加:
```go
	SSHIngressKeygen *handlers.SSHIngressKeygenHandler
```

2. newHandlers 在 `SSHIngress: handlers.NewSSHIngressHandlerFromCache(),` 后加:
```go
		SSHIngressKeygen:   handlers.NewSSHIngressKeygenHandler(),
```
(对齐该 struct 字面量现有对齐空格)

3. RegisterRoutes 在 hostkey register 行后加:
```go
	// SSH 入口一键生成(spec 2026-11-12 §1.2):POST 无状态纯生成,私钥只在
	// 响应出现一次;双注册另一处 agentruntime server.go registerAgentRoutes
	// (缺任一侧=404 透传,2026-10-21 教训;route_parity_test.go 为护栏)。
	register("/api/proxy/ssh-ingress/keygen", h.SSHIngressKeygen.HandleKeygen, http.MethodPost)
```

- [ ] **Step 2: user-agent 侧接线**

`app/agentruntime/server.go` registerAgentRoutes 里,`sshIngressHandler` 块之后加:

```go
	// SSH 入口一键生成(spec 2026-11-12 §1.2):POST 无状态纯生成,私钥只在
	// 响应出现一次,服务端不落盘。无状态实例两进程各持等价。双注册另一处
	// routes.go(缺任一侧=404 透传,2026-10-21 教训)。
	mux.HandleFunc("/api/proxy/ssh-ingress/keygen", handlers.NewSSHIngressKeygenHandler().HandleKeygen)
```

- [ ] **Step 3: 编译 + parity 护栏**

Run: `go build ./... && go test ./app/agentruntime/ -run TestAgentRouteParityBetweenCoreAndUserAgent -v`
Expected: BUILD OK;parity 测试 PASS(它比对 routes.go 与 registerAgentRoutes 的路径集合,漏一侧必红)

- [ ] **Step 4: 提交**

```bash
git add app/http/routes/routes.go app/agentruntime/server.go
git commit -m "$(cat <<'EOF'
新增:keygen 端点双注册——core routes.go + user-agent registerAgentRoutes

POST /api/proxy/ssh-ingress/keygen 两侧同路径接线(spec 2026-11-12 §1.2);
2026-10-21 教训:缺任一侧=404 透传,route_parity_test.go 护栏验证已过。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: 前端一键流(TDD)

**Files:**
- Modify: `app/website/src/i18n/zh.js:965` 附近(sshIngress_* 段末尾)、`app/website/src/i18n/en.js:966` 附近
- Modify: `app/website/src/components/settings/SshIngressSettings.vue`
- Test: `app/website/src/components/settings/SshIngressSettings.test.js`

- [ ] **Step 1: i18n 八键(zh + en 同步加)**

`zh.js` 的 `sshIngress_save` 行后加:

```js
  sshIngress_oneClick: '一键生成并启用',
  sshIngress_privKeyTitle: '私钥（仅此一次）',
  sshIngress_privKeyOnceWarning: '私钥仅展示这一次，关闭即弃；丢失请重新生成并删除列表中的旧钥匙。',
  sshIngress_privKeyDownload: '下载 .pem',
  sshIngress_privKeyCopy: '复制私钥',
  sshIngress_privKeyCopied: '已复制',
  sshIngress_privKeyConnectExample: '连接示例：ssh -D 1080 -i <下载的.pem> 任意用户名@<节点IP> -p 56432（用户名随意，鉴权只认公钥）',
  sshIngress_keygenFailed: '密钥生成失败，请重试。',
```

(标点全角:()、,、;、:——文案口径铁律,见记忆 rule-syntax 同族约定。)

`en.js` 同位置加:

```js
  sshIngress_oneClick: 'Generate & enable in one click',
  sshIngress_privKeyTitle: 'Private key (shown once)',
  sshIngress_privKeyOnceWarning: 'This private key is shown only once. Close means gone — regenerate and delete the old key from the list if lost.',
  sshIngress_privKeyDownload: 'Download .pem',
  sshIngress_privKeyCopy: 'Copy private key',
  sshIngress_privKeyCopied: 'Copied',
  sshIngress_privKeyConnectExample: 'Connect: ssh -D 1080 -i <downloaded .pem> anyuser@<node IP> -p 56432 (username is arbitrary; auth is pubkey-only)',
  sshIngress_keygenFailed: 'Key generation failed, please retry.',
```

- [ ] **Step 2: 写失败测试**

`SshIngressSettings.test.js` 文件末尾追加 describe(新 mount helper 用 reactive props 以便模拟父层 error/successMessage 回流;不动现有 mountCard,防扰动存量测试):

```js
// —— 一键生成并启用(spec 2026-11-12 §2)——
// mountCardLive 与 mountCard 同手法,但 props 用 reactive 以模拟父层
// error/successMessage 回流(父层每次保存开头清空二者、结尾再置值)。
const HUMAN_KEY2 = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHUMAN2 human@pc';
const keygenEnvelope = (key) => ({ code: 0, msg: '', data: { key } });
const keygenPayload = () => ({
  public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIONECLICK aliang-ingress-node-20261112120000',
  private_key: '-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n',
  fingerprint: 'SHA256:oneclickfingerprint0000000000000000000',
  algorithm: 'ssh-ed25519',
  comment: 'aliang-ingress-node-20261112120000',
  suggested_filename: 'aliang-ingress-node-20261112.pem'
});

// fetch 分流:GET hostkey 走默认信封,POST keygen 走注入的响应
function stubFetchRouted(keygenImpl) {
  vi.stubGlobal('fetch', vi.fn((url, opts) => {
    if ((opts?.method || 'GET') === 'POST' && String(url).includes('/ssh-ingress/keygen')) {
      return Promise.resolve(keygenImpl());
    }
    return Promise.resolve(jsonResponse(hostkeyEnvelope(hostkeyPayload())));
  }));
}

describe('SshIngressSettings 一键生成并启用', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(hostkeyEnvelope(hostkeyPayload()))));
  });

  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('一键流:keygen → 公钥插队头+enable=true → emit save(整配置透传)', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const saved = [];
    const props = reactive({ config: baseConfig(), error: '', successMessage: '' });
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({ render: () => h(SshIngressSettings, { ...props, onSave: (p) => saved.push(p) }) });
    // 注意:reactive props 展开进 h() 后是快照、非响应——本例只断言 emit 载荷
    // 不受影响;需要模拟父层回流(error/successMessage)的用例见下,逐键读
    // reactive 对象(render 闭包内读 props.error 等)才是响应写法
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();

    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();

    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.enable).toBe(true);
    expect(saved[0].ssh_ingress.authorized_keys[0]).toBe(keygenPayload().public_key);
    expect(saved[0].proxy).toEqual(baseConfig().proxy); // 整配置透传不丢段
  });

  it('保存成功回流(successMessage 变非空+pending 在)→ 私钥弹窗渲染全文+下载+复制', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const props = reactive({ config: baseConfig(), error: '', successMessage: '' });
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({
      render: () => h(SshIngressSettings, { config: props.config, error: props.error, successMessage: props.successMessage, onSave: () => {} })
    });
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();

    props.successMessage = '已保存';
    await flush();

    const modal = el.querySelector('[data-ssh-privkey-modal]');
    expect(modal).not.toBeNull();
    expect(el.querySelector('[data-ssh-privkey-text]').textContent).toContain('BEGIN OPENSSH PRIVATE KEY');
    expect(el.querySelector('a[data-ssh-privkey-download]')).not.toBeNull();
    expect(el.querySelector('button[data-ssh-privkey-copy]')).not.toBeNull();
  });

  it('恢复链(v3 核心):一键保存失败 pending 保留 → 手工再存成功 → 弹窗补交付原钥', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const props = reactive({ config: baseConfig(), error: '', successMessage: '' });
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({
      render: () => h(SshIngressSettings, { config: props.config, error: props.error, successMessage: props.successMessage, onSave: () => {} })
    });
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click(); // 一键 → pending=K
    await flush();

    props.error = 'boom';   // 保存失败:error 一概不动 pending(v3)——只不弹窗
    await flush();
    expect(el.querySelector('[data-ssh-privkey-modal]')).toBeNull();

    // 表单不回滚(spec §2.2):此时 enabled=true 且钥匙在,一键按钮已隐藏——
    // 用户自然恢复路径=手工再点保存按钮。勿在此处点 data-ssh-oneclick(null)。
    props.error = '';       // 重试保存开头清空(error 空串跃迁无 watch)
    await flush();
    props.successMessage = '已保存';
    await flush();
    const modal = el.querySelector('[data-ssh-privkey-modal]');
    expect(modal).not.toBeNull();  // 弹窗补交付原钥(pending 跨失败保留)
    expect(el.querySelector('[data-ssh-privkey-text]').textContent).toContain('BEGIN OPENSSH PRIVATE KEY');
  });

  it('无主私钥防交付:保存失败后公钥已不在列表 → success 回流不弹窗', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const props = reactive({ config: baseConfig(), error: '', successMessage: '' });
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({
      render: () => h(SshIngressSettings, { config: props.config, error: props.error, successMessage: props.successMessage, onSave: () => {} })
    });
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();
    props.error = 'boom';
    await flush();

    // 用户把配置换掉(deep config watch 重置表单;pending 存活)——
    // 新配置里没有生成的公钥
    props.config = baseConfig({ enable: true, authorized_keys: [HUMAN_KEY2] });
    await flush();
    props.successMessage = '已保存';
    await flush();
    expect(el.querySelector('[data-ssh-privkey-modal]')).toBeNull(); // 不交付无主私钥
  });

  it('折叠态联动:enabled false→true 且无钥匙 → 正文自动展开', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.querySelector('[data-ssh-ingress-body]').style.display).toBe('none');
    setToggle(el, 'sshIngressEnable', true);
    await flush();
    expect(el.querySelector('[data-ssh-ingress-body]').style.display).not.toBe('none');
    expect(el.querySelector('button[data-ssh-oneclick]')).not.toBeNull();
  });

  it('已启用且有钥匙 → 一键按钮不渲染(归手工流)', async () => {
    const { el } = mountCard(baseConfig({ enable: true, authorized_keys: [HUMAN_KEY2] }));
    await flush();
    expect(el.querySelector('button[data-ssh-oneclick]')).toBeNull();
  });
});
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd app/website && npx vitest run src/components/settings/SshIngressSettings.test.js`
Expected: 新 describe 前 5 例红(`data-ssh-oneclick` 找不到=null → `Cannot read properties of null`);末例「已启用且有钥匙→按钮不渲染」预存即绿(纯断言,合理);存量 describe 全绿

- [ ] **Step 4: 实现 SshIngressSettings.vue**

四处改动:

**(a) data 增加**:

```js
      keygenPending: null,   // 一键流暂存:keygen 结果,保存成功后转 keygenResult
      keygenResult: null,    // 弹窗展示中的私钥载荷;关闭即清,零持久化
      privKeyModal: false,
      privKeyCopied: false,
      privKeyCopyTimer: null,
      localError: ''         // keygen 请求自身失败的红框(与父层 error prop 并列)
```

**(b) watch 调整**(现有 `successMessage` watch 扩展 + 新增一个 `form.enabled`;**error 一概不挂 watch**——spec §2.2 v3):

```js
    // 一键流回流(spec §2.2 v3):父层每次保存开头清空 error/success、结尾
    // 再置值——跃迁必触发。successMessage 只认「变为非空」;error 一概不挂
    // watch,pending 跨保存失败保留——否则一键保存失败后(表单已带钥匙、
    // 一键按钮已隐藏)用户手工再存成功时,丢 pending 会永久吞掉私钥且无报错
    // (恢复链单测钉死此语义)。
    successMessage(message) {
      if (message) {
        this.loadHostkey();
        this.localError = '';
        if (this.keygenPending) {
          // 入列校验:公钥已不在列表(用户删过/换过配置)就不弹窗——不交付无主私钥
          if (this.form.authorizedKeys.includes(this.keygenPending.public_key)) {
            this.keygenResult = this.keygenPending;
            this.privKeyModal = true;
          }
          this.keygenPending = null;
        }
      }
    },
    // 折叠态联动(spec §2.1):拨头部开关且无钥匙 → 自动展开正文,
    // 一键按钮与红字提示当场入眼(头部常驻提示已列明确不做)。
    'form.enabled'(enabled) {
      if (enabled && !this.form.authorizedKeys.length) {
        this.bodyExpanded = true;
      }
    },
```

**(c) 模板四处**:

1. 错误框条件改并列(`error` prop 与 keygen 本地失败共用顶部红框):
```html
    <div
      v-if="error || localError"
      class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
      role="alert"
    >
      {{ error || localError }}
    </div>
```

2. 正文(`data-ssh-ingress-body` 开标签后、端口 grid 前)插一键按钮:
```html
    <!-- 一键生成并启用(spec §2.1):keygen→公钥插队头→开启→走既有保存链;
         已启用且有钥匙时隐藏(归手工流)。禁用不挂 validationError——无钥匙
         状态它恒真会锁死按钮;只挂 portError(红字同屏可见)。 -->
    <button
      v-if="!form.enabled || !form.authorizedKeys.length"
      class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
      data-ssh-oneclick
      type="button"
      :disabled="loading || saving || !!portError"
      @click="oneClickSetup"
    >
      <span class="material-symbols-outlined text-sm">bolt</span>
      {{ t('sshIngress_oneClick') }}
    </button>
```

3. 卡片根 div 末尾(`</div>` 闭合前)加私钥弹窗:
```html
    <!-- 私钥一次性弹窗(spec §2.3):全文+下载+复制+连接示例;关闭即清零
         持久化(state 同步清,不写 localStorage/日志)。 -->
    <div
      v-if="privKeyModal"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      data-ssh-privkey-modal
      role="dialog"
      aria-modal="true"
      @click.self="closePrivKeyModal"
    >
      <div class="flex w-full max-w-xl flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-xl dark:border-slate-700 dark:bg-slate-900">
        <div class="flex items-center justify-between">
          <h4 class="font-semibold text-slate-900 dark:text-white">{{ t('sshIngress_privKeyTitle') }}</h4>
          <button
            type="button"
            class="text-slate-400 transition hover:text-slate-600 dark:hover:text-slate-200"
            data-ssh-privkey-close
            :aria-label="t('sshIngress_privKeyTitle')"
            @click="closePrivKeyModal"
          >
            <span class="material-symbols-outlined">close</span>
          </button>
        </div>
        <p class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
          {{ t('sshIngress_privKeyOnceWarning') }}
        </p>
        <p class="text-xs text-slate-500 dark:text-slate-400">
          {{ keygenResult?.algorithm }} · <code class="break-all font-mono">{{ keygenResult?.fingerprint }}</code>
        </p>
        <pre
          class="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-slate-200 bg-slate-50 p-3 font-mono text-[11px] text-slate-700 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-200"
          data-ssh-privkey-text
        >{{ keygenResult?.private_key }}</pre>
        <div class="flex flex-wrap items-center justify-end gap-3">
          <a
            class="inline-flex min-h-11 items-center gap-1.5 rounded border border-slate-200 px-3 text-xs font-semibold text-slate-600 transition hover:border-primary/30 hover:text-primary dark:border-slate-700 dark:text-slate-300"
            data-ssh-privkey-download
            download
            :href="privKeyDownloadHref"
            :download="keygenResult?.suggested_filename || 'aliang-ingress-key.pem'"
          >
            <span class="material-symbols-outlined text-sm">download</span>
            {{ t('sshIngress_privKeyDownload') }}
          </a>
          <button
            type="button"
            class="inline-flex min-h-11 items-center gap-1.5 rounded border border-slate-200 px-3 text-xs font-semibold text-slate-600 transition hover:border-primary/30 hover:text-primary dark:border-slate-700 dark:text-slate-300"
            data-ssh-privkey-copy
            @click="copyPrivKey"
          >
            <span class="material-symbols-outlined text-sm">content_copy</span>
            {{ privKeyCopied ? t('sshIngress_privKeyCopied') : t('sshIngress_privKeyCopy') }}
          </button>
        </div>
        <p class="text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('sshIngress_privKeyConnectExample') }}</p>
      </div>
    </div>
```

**(d) methods 增加 + beforeUnmount 清理**:

computed 加:
```js
    // 下载 href 用 data URL(Blob URL 在 jsdom/部分沙箱不可用;data URL 两端通吃)
    privKeyDownloadHref() {
      if (!this.keygenResult) {
        return '#';
      }
      return 'data:application/octet-stream,' + encodeURIComponent(this.keygenResult.private_key);
    },
```

methods 加:
```js
    // 一键生成并启用(spec §2.2):keygen → 公钥插队头 → enable=true →
    // 复用现款 save() 契约(整配置透传+ssh_ingress 覆盖)。keygen 自身失败
    // 走 localError 红框;保存结果由父层 error/successMessage props 回流,
    // watch 方向语义见 watch 注释。
    async oneClickSetup() {
      if (this.loading || this.saving) {
        return;
      }
      // 双击防重:loading/saving 是父层保存态,keygen fetch 在途时不置位——
      // 在途 keygenPending 直接 return,防双击产生两把钥匙把第一把挤成无主
      if (this.keygenPending) {
        return;
      }
      this.localError = '';
      try {
        const res = await fetch('/api/proxy/ssh-ingress/keygen', { method: 'POST', credentials: 'same-origin' });
        const json = await res.json().catch(() => null);
        const key = json && json.data ? json.data.key : null;
        if (!res.ok || !key || !key.public_key || !key.private_key) {
          throw new Error('keygen payload incomplete');
        }
        this.keygenPending = key;
        this.form.authorizedKeys = [key.public_key, ...this.form.authorizedKeys.filter((k) => k !== key.public_key)];
        this.form.enabled = true;
        this.save();
      } catch (err) {
        this.localError = this.t('sshIngress_keygenFailed');
      }
    },
    closePrivKeyModal() {
      this.privKeyModal = false;
      this.keygenResult = null;
      this.privKeyCopied = false;
      if (this.privKeyCopyTimer) {
        clearTimeout(this.privKeyCopyTimer);
        this.privKeyCopyTimer = null;
      }
    },
    async copyPrivKey() {
      if (!this.keygenResult) {
        return;
      }
      const ok = await copyText(this.keygenResult.private_key);
      this.privKeyCopied = ok;
      if (this.privKeyCopyTimer) {
        clearTimeout(this.privKeyCopyTimer);
      }
      this.privKeyCopyTimer = setTimeout(() => {
        this.privKeyCopied = false;
      }, 2000);
    },
```

beforeUnmount 现有清理处补 `this.closePrivKeyModal()` 前的 timer 逻辑已有,直接把 privKeyCopyTimer 合入现有清理(或调 closePrivKeyModal)。

- [ ] **Step 5: 跑测试确认通过**

Run: `cd app/website && npx vitest run src/components/settings/SshIngressSettings.test.js`
Expected: 存量 + 新增 6 例全 PASS

- [ ] **Step 6: 提交**

```bash
git add app/website/src/components/settings/SshIngressSettings.vue app/website/src/components/settings/SshIngressSettings.test.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "$(cat <<'EOF'
新增:SSH 入口一键生成并启用——keygen 按钮私钥弹窗折叠联动

一键=keygen→公钥插队头→开启→走既有保存链(spec 2026-11-12 §2 v3);私钥
一次性弹窗全文+下载(data URL)+复制,关闭即清零持久化;折叠态拨开关自动
展开正文(§1 头部症状正面修复);回流语义 v3:error 一概不挂 watch,pending
跨保存失败保留,success 回流时校验公钥仍在列表才弹窗——否则一键保存失败
后用户手工再存成功会永久吞掉私钥且无报错(恢复链+无主防交付两单测钉死)。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: 全量回归 + spec 入库

**Files:**
- Commit: `docs/superpowers/specs/2026-11-12-ssh-ingress-oneclick-keygen-design.md`(已在 worktree 外的主检出创建——若不在本 worktree,先复制进来)

- [ ] **Step 1: 确认 spec 文档在 worktree 内**

Run: `ls docs/superpowers/specs/2026-11-12-ssh-ingress-oneclick-keygen-design.md`
若缺失:从主检出 `/Users/mac/MyProgram/GoProgram/nursor/alianggate/docs/superpowers/specs/` 复制同名文件进来。

- [ ] **Step 2: Go 全量构建+相关包回归**

Run: `go build ./... && go test ./inbound/ssh/ ./app/http/handlers/ ./app/agentruntime/ ./app/http/services/ -count=1`
Expected: 全 PASS(handlers/services 包大,偶发预存 flake 见记忆 go-test 全仓预存 flake——单包复跑即绿,复跑仍红才算回归)

- [ ] **Step 3: 前端全量回归**

Run: `cd app/website && npx vitest run 2>&1 | tail -15`
Expected: 全 PASS(基线=上一发布;若有预存红,与 master 主检出同命令对比确认非本次引入)

- [ ] **Step 4: 提交文档**

```bash
git add docs/superpowers/specs/2026-11-12-ssh-ingress-oneclick-keygen-design.md docs/superpowers/plans/2026-11-12-ssh-ingress-oneclick-keygen.md
git commit -m "$(cat <<'EOF'
文档:SSH 入口一键 keygen 设计 spec(v3)与实施计划入库

spec 三轮收敛:v1→v2 钉死 watch 方向+补折叠态联动;plan 评审抓出恢复链
缺陷后 v3 再修正——error 一概不挂 watch,pending 跨失败保留,success 回流
校验公钥在列才弹窗(否则手工再存成功会永久吞私钥)。安全边界明写:私钥经
56431 无 TLS LAN 回传与入口暴露面同级,服务端不落盘。

🤖 Generated with [Claude Code](https://claude.com/claude-code)

Co-Authored-By: Claude Code <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 5: 汇报,不推送不合并**

向用户汇报:分支 `feat/ssh-ingress-oneclick-keygen` 全绿,N 个提交;合并 master 与推送均需用户明示(CLAUDE.md 铁律)。
