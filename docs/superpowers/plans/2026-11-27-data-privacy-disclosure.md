# 数据与隐私:知情披露与云端记忆删除 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 按 spec `docs/superpowers/specs/2026-11-27-data-privacy-disclosure-design.md` 落地:仪表盘「数据与隐私」常驻知情弹层 + OpenViking 卡全量删除按钮 + agent/云端两级 purge 端点。

**Architecture:** 三段纵切——alianggate(仪表盘 Vue + Go agent 端点)→ AliangPhoneServer(TS 云端 purge,持 ROOT key 调 OV Admin API)→ 部署验收链。云端契约:200 `{purged:true}` 幂等 / 400 仅 BYO 激活态 / 502 透传 / 504 本地合成(超时)。

**Tech Stack:** Go(alianggate agent)、Vue3 Options API + vitest(仪表盘,无 @vue/test-utils——测试走逻辑层)、Express + TS + vitest(PhoneServer)。

---

## 仓库与工作流约定(执行者必读)

1. **两仓路径**:
   - alianggate:`/Users/mac/MyProgram/GoProgram/nursor/alianggate`(本计划所在仓)
   - AliangPhoneServer:`/Users/mac/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer`
2. **提交纪律(仓库铁律)**:提交步骤是计划的一部分,但**每次 commit 前必须得到用户确认**;提交一律**路径限定**(`git commit -- <路径>`,防并行会话暂存混包——本仓有在途 TDD 污染前科);**全中文提交信息,禁强推**。
3. **并行会话坑**:alianggate 有多会话并行工作,开工前 `git status` 核对,只动本计划列出的文件。
4. **测试基线**:alianggate services 包有预存 flaky(定向 `-run` 验证即可,勿跑全包误判);PhoneServer 全仓 3 个预存 flaky 失败(stash 基线坐实),新增测试必须绿,存量红对照基线。
5. **红线**:headers/凭据值永不进日志;OV ROOT key 只存在于 PhoneServer env。

---

### Task 1: agent 云端 purge 客户端(Go,TDD)

**Files:**
- Modify: `app/http/services/agent_openviking.go`(文件尾追加)
- Create: `app/http/services/agent_openviking_purge_test.go`

- [ ] **Step 1: 写失败测试**

```go
package services

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"aliang.one/nursorgate/processor/config"
)

// 云端 purge 客户端契约(spec §7):头两级回落+设备 ID;非 200 透传;超时本地合成 504。
func TestOpenVikingPurgeCallSendsAuthAndDeviceHeaders(t *testing.T) {
	var gotAuth, gotDevice string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/openviking/purge" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotDevice = r.Header.Get("X-Aliang-Device-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"purged":true}`))
	}))
	defer srv.Close()

	err := openvikingPurgeCall(srv.URL, "Bearer tok123", "dev-1")
	if err != nil {
		t.Fatalf("expect nil, got %v", err)
	}
	if gotAuth != "Bearer tok123" || gotDevice != "dev-1" {
		t.Fatalf("headers missing: auth=%q device=%q", gotAuth, gotDevice)
	}
}

func TestOpenVikingPurgeCallPassthroughNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "openviking unreachable", http.StatusBadGateway)
	}))
	defer srv.Close()

	err := openvikingPurgeCall(srv.URL, "Bearer t", "d")
	pe, ok := err.(*openvikingPurgeError)
	if !ok {
		t.Fatalf("expect *openvikingPurgeError, got %T %v", err, err)
	}
	if pe.Status != http.StatusBadGateway || pe.Text == "" {
		t.Fatalf("status=%d text=%q", pe.Status, pe.Text)
	}
}

func TestOpenVikingPurgeCallTimeoutSynthesizes504(t *testing.T) {
	old := openvikingPurgeTimeout
	openvikingPurgeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { openvikingPurgeTimeout = old })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := openvikingPurgeCall(srv.URL, "", "")
	pe, ok := err.(*openvikingPurgeError)
	if !ok || pe.Status != http.StatusGatewayTimeout {
		t.Fatalf("expect 504 synthesized, got %T %v", err, err)
	}
}

// 空响应体失败:Text 兜底非空(dialog 要显原因)。
func TestOpenVikingPurgeCallEmptyBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	err := openvikingPurgeCall(srv.URL, "", "")
	pe, ok := err.(*openvikingPurgeError)
	if !ok || pe.Status != http.StatusNotFound || pe.Text == "" {
		t.Fatalf("expect non-empty text, got %#v", pe)
	}
}

// OpenVikingPurge 门禁(spec §6.3 评审修):仅 BYO 激活态 400;关闭态放行。
func TestOpenVikingPurgeGateBYOActiveOnly(t *testing.T) {
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "byo"})
	called := false
	old := openvikingPurgeRemoteImpl
	openvikingPurgeRemoteImpl = func() error { called = true; return nil }
	t.Cleanup(func() { openvikingPurgeRemoteImpl = old })

	err := GetSharedAgentService().OpenVikingPurge()
	pe, ok := err.(*openvikingPurgeError)
	if !ok || pe.Status != http.StatusBadRequest || pe.Text != "byo_not_supported" {
		t.Fatalf("expect 400 byo_not_supported, got %T %v", err, err)
	}
	if called {
		t.Fatal("BYO active must not reach remote")
	}
}

func TestOpenVikingPurgeGateOffStatePasses(t *testing.T) {
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(false), Mode: "byo"}) // 关态保留 byo 模式
	called := false
	old := openvikingPurgeRemoteImpl
	openvikingPurgeRemoteImpl = func() error { called = true; return nil }
	t.Cleanup(func() { openvikingPurgeRemoteImpl = old })

	if err := GetSharedAgentService().OpenVikingPurge(); err != nil {
		t.Fatalf("off state must pass gate, got %v", err)
	}
	if !called {
		t.Fatal("off state must reach remote")
	}
}

func TestOpenVikingPurgeGateManagedPasses(t *testing.T) {
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "managed"})
	old := openvikingPurgeRemoteImpl
	openvikingPurgeRemoteImpl = func() error { return nil }
	t.Cleanup(func() { openvikingPurgeRemoteImpl = old })
	if err := GetSharedAgentService().OpenVikingPurge(); err != nil {
		t.Fatalf("managed must pass, got %v", err)
	}
}

// spec §10.3:byo_detected(managed 配置但注册表留有用户自有条目)不设闸——
// purge 只清平台托管数据,与用户自有部署无关。
func TestOpenVikingPurgeGateByoDetectedDoesNotBlock(t *testing.T) {
	setOpenVikingGlobal(t, &config.OpenVikingConfig{Enabled: boolPtr(true), Mode: "managed"})
	openVikingByoDetected.Store(true)
	t.Cleanup(func() { openVikingByoDetected.Store(false) })
	old := openvikingPurgeRemoteImpl
	called := false
	openvikingPurgeRemoteImpl = func() error { called = true; return nil }
	t.Cleanup(func() { openvikingPurgeRemoteImpl = old })
	if err := GetSharedAgentService().OpenVikingPurge(); err != nil {
		t.Fatalf("byo_detected must not gate, got %v", err)
	}
	if !called {
		t.Fatal("byo_detected must reach remote")
	}
}
```

注意:`setOpenVikingGlobal`/`boolPtr` 是 `agent_openviking_test.go` 既有的测试助手(`:18`),同包直接用。`openvikingPurgeRemoteImpl` 是可注入点(见 Step 3),测试换实现。

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./app/http/services/ -run 'TestOpenVikingPurge' -count=1`
Expected: FAIL(undefined: openvikingPurgeCall / openvikingPurgeError)

- [ ] **Step 3: 最小实现**

在 `agent_openviking.go` 文件尾追加:

```go
// ---------------------------------------------------------------------------
// 云端记忆全量删除(spec 2026-11-27 数据与隐私 §6.3)。评审修定的门禁语义:
// 仅 BYO 激活态拒绝;关闭态/托管态一律放行——关闭后 OV 注册条目已被 cleanup
// 清除,若按 registered 门禁会把按钮锁死在唯一需要它的时刻。purge 触碰的
// 永远只是平台托管数据,byo_detected(managed 配置但注册表留有用户自有条目,
// agent_openviking.go:184)不构成拒绝理由。
// ---------------------------------------------------------------------------

// openvikingPurgeTimeout 云端 purge 单次超时。var 非_const:测试注入短超时。
var openvikingPurgeTimeout = 10 * time.Second

// openvikingPurgeRemoteImpl 远端调用注入点(测试换桩)。
var openvikingPurgeRemoteImpl = openvikingPurgeRemote

// openvikingPurgeError 携带 HTTP 状态码的 purge 失败(handler 据此映射响应码)。
type openvikingPurgeError struct {
	Status int
	Text   string
}

func (e *openvikingPurgeError) Error() string { return e.Text }

// OpenVikingPurge 门禁 + 远端调用。
func (s *AgentService) OpenVikingPurge() error {
	cfg := config.GetGlobalConfig()
	if cfg.EffectiveOpenVikingEnabled() && cfg.EffectiveOpenVikingMode() == config.OpenVikingModeBYO {
		return &openvikingPurgeError{Status: http.StatusBadRequest, Text: "byo_not_supported"}
	}
	return openvikingPurgeRemoteImpl()
}

// openvikingPurgeRemote 云端调用,契约逐字段照 fetchOpenVikingCredential
// (同文件):URL=<APIBaseURL>/api/agent/openviking/purge;凭据两级回落
// (转发槽 UserAccessToken → effectiveAgentRegisterAuthHeader);必带
// X-Aliang-Device-ID。云端非 200 原样透传状态码与原因;调用超时(无远端
// 状态码)本地合成 504。
func openvikingPurgeRemote() error {
	base := strings.TrimRight(strings.TrimSpace(config.GetGlobalConfig().APIBaseURL()), "/")
	if base == "" {
		return fmt.Errorf("api_server 未配置")
	}
	authHeader := ""
	if tok := strings.TrimSpace(GetSharedAgentService().UserAccessToken()); tok != "" {
		authHeader = "Bearer " + tok
	} else if h := effectiveAgentRegisterAuthHeader(""); h != "" {
		authHeader = h
	}
	deviceID := strings.TrimSpace(GetSharedAgentService().currentDeviceID())
	return openvikingPurgeCall(base, authHeader, deviceID)
}

// openvikingPurgeCall purge 传输本体(base 可注入,测试指向 httptest)。
func openvikingPurgeCall(base, authHeader, deviceID string) error {
	url := base + "/api/agent/openviking/purge"
	client := &http.Client{Timeout: openvikingPurgeTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if deviceID != "" {
		req.Header.Set("X-Aliang-Device-ID", deviceID)
	}
	resp, err := client.Do(req)
	if err != nil {
		// 超时/连不上:无远端状态码 → 本地合成 504(spec §7)。
		return &openvikingPurgeError{Status: http.StatusGatewayTimeout, Text: fmt.Sprintf("云端 purge 不可达: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		text := strings.TrimSpace(string(body))
		if text == "" {
			text = fmt.Sprintf("云端 purge 失败 status=%d", resp.StatusCode)
		}
		return &openvikingPurgeError{Status: resp.StatusCode, Text: text}
	}
	return nil
}
```

需要补 import:`"io"`(`agent_openviking.go` 现有 import 已含 bytes/context/encoding-json/fmt/net-http/os/filepath/strings/sync-atomic/time,补 io 即可)。

- [ ] **Step 4: 跑测试确认绿**

Run: `go test ./app/http/services/ -run 'TestOpenVikingPurge' -count=1`
Expected: PASS(8 个用例全绿)

- [ ] **Step 5: handler + 路由( thin 层,TDD 以 service 层为准)**

`app/http/handlers/agent_handler.go` 照 `HandleOpenVikingApply`(`:862`)追加:

```go
// HandleOpenVikingPurge POST 云端记忆全量删除(spec 2026-11-27 §6.3):
// BYO 激活态 400(服务层闸),云端状态码透传,超时本地合成 504。
func (h *AgentHandler) HandleOpenVikingPurge(w http.ResponseWriter, r *http.Request) {
	if h.proxyIfNeeded(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.service.OpenVikingPurge(); err != nil {
		var pe *services.OpenVikingPurgeStatusError
		if errors.As(err, &pe) {
			// 必须用 SendError 直写状态码(common.SendError 带 deprecated 标注,
			// 此处刻意用之:直写 pe.Status 是本契约的全部意义;不想碰 deprecated
			// 可手写 w.WriteHeader(pe.Status)+JSON envelope,等价):common.Error
			// 会把 code 走 ErrorCodeToHTTPStatus 映射(common/errors.go:61),
			// 400/502/504 全落 default→500,破坏 spec §7 透传契约。
			common.SendError(w, pe.Text, pe.Status, nil)
			return
		}
		common.Error(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{"purged": true})
}
```

注意:①`openvikingPurgeError` 要从 services 包导出给 handler 用——实现时命名 **`OpenVikingPurgeStatusError`**(导出),service/测试同步改名;上面 Step 1/3 代码中的 `openvikingPurgeError` 一律换成导出名。②`errors` 是**新增** import(`agent_handler.go` 现有 import 块没有它,计划评审已核实)。

`app/http/routes/routes.go` 照 `:328` 追加:

```go
register("/api/agent/openviking/purge", h.Agent.HandleOpenVikingPurge, http.MethodPost)
```

- [ ] **Step 6: 全量验证**

Run: `go build ./... && go test ./app/http/services/ -run 'TestOpenViking' -count=1`
Expected: build 无错;OpenViking 全族(含既有 ensure 族 + 新 purge 族)全绿

- [ ] **Step 7: 提交(须用户确认)**

```bash
git add app/http/services/agent_openviking.go app/http/services/agent_openviking_purge_test.go app/http/handlers/agent_handler.go app/http/routes/routes.go
git commit -m "新增:OpenViking 云端记忆全量删除 agent 端点(数据与隐私批 §6.3)" -- app/http/services/agent_openviking.go app/http/services/agent_openviking_purge_test.go app/http/handlers/agent_handler.go app/http/routes/routes.go
```

---

### Task 2: 仪表盘 OV 卡删除按钮 + 文案改写实(Vue)

**Files:**
- Modify: `app/website/src/services/agentApi.js:125-132` 后追加
- Modify: `app/website/src/components/settings/agent/OpenVikingSettings.vue`
- Modify: `app/website/src/i18n/zh.js` / `en.js`
- Test: Modify: `app/website/src/components/settings/agent/OpenVikingSettings.test.js`

- [ ] **Step 1: 写失败测试(逻辑层,仓内无 @vue/test-utils)**

在 `OpenVikingSettings.test.js` 追加:

```js
import { openVikingPurgeAvailable } from './OpenVikingSettings.vue';

describe('openVikingPurgeAvailable(spec §6.3 评审修:仅 BYO 激活态隐藏)', () => {
  it('BYO 激活态隐藏', () => {
    expect(openVikingPurgeAvailable('byo')).toBe(false);
  });
  it('关闭态可用(唯一删除通道)', () => {
    expect(openVikingPurgeAvailable('off')).toBe(true);
  });
  it('托管态可用', () => {
    expect(openVikingPurgeAvailable('managed')).toBe(true);
  });
});
```

照 `SettingsPage.vue` 导出 `buildCustomerSavePatch` 的先例,SFC 内加具名导出:

```js
// spec §6.3:仅 BYO 激活态隐藏删除按钮;关闭态/托管态一律可用。
export function openVikingPurgeAvailable(mode) {
  return mode !== 'byo';
}
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd app/website && npx vitest run src/components/settings/agent/OpenVikingSettings.test.js`
Expected: FAIL(openVikingPurgeAvailable 未导出)

- [ ] **Step 3: 实现 agentApi + 按钮 + 文案**

`agentApi.js` 追加:

```js
// OpenViking 云端记忆全量删除(spec 2026-11-27 §6.3):400/502/504 由后端映射。
export async function purgeOpenViking() {
  return request('/api/agent/openviking/purge', { method: 'POST' });
}
```

`OpenVikingSettings.vue` 状态行(`data-testid="openviking-status"` 那行)旁追加按钮组,模板:

```html
<!-- 云端记忆全量删除(spec 2026-11-27 §6.3):BYO 激活态隐藏;两击确认 -->
<div v-if="openVikingPurgeAvailable(form.mode)" class="mt-3" data-testid="openviking-purge">
  <button v-if="!purgeConfirming" type="button" class="text-[11px] font-medium text-slate-500 hover:text-red-600 dark:text-slate-400 dark:hover:text-red-400" data-testid="openviking-purge-start" @click="purgeConfirming = true">
    {{ t('openViking_purge') }}
  </button>
  <template v-else>
    <span class="mr-2 text-[11px] text-slate-500 dark:text-slate-400">{{ t('openViking_purgeHint') }}</span>
    <button type="button" class="mr-2 rounded bg-red-600 px-2 py-1 text-[11px] font-bold text-white hover:bg-red-500" data-testid="openviking-purge-confirm" @click="purge">
      {{ t('openViking_purgeConfirm') }}
    </button>
    <button type="button" class="text-[11px] text-slate-400 hover:text-slate-600" data-testid="openviking-purge-cancel" @click="purgeConfirming = false">
      {{ t('sys_cancel') }}
    </button>
  </template>
</div>
<span v-else class="mt-3 block text-[11px] text-slate-400 dark:text-slate-500" data-testid="openviking-purge-byo-hint">
  {{ t('openViking_purgeByoHint') }}
</span>
```

组件 data/methods 追加(**注意:模板里的 `v-if="openVikingPurgeAvailable(form.mode)"` 无法解析 `<script>` 模块级裸导出**——计划评审已用本仓 `@vue/compiler-sfc` 实证,`bindingMetadata` 只含 data 字段,`_ctx.openVikingPurgeAvailable` 运行时恒 undefined,按钮永不渲染。必须同时挂到 methods 上暴露给模板;逻辑层测试直接 import 导出,两种写法都绿,红绿循环抓不到这个):

```js
data() {
  return {
    // ...既有字段
    purgeConfirming: false,
    purging: false,
    purgeOk: false, // 成功反馈:inline 绿字三秒自清(仓内无全局成功 dialog 组件,不新造)
  };
},
methods: {
  openVikingPurgeAvailable, // 模板可见性:裸 <script> 导出必须经 methods 暴露(见上注)
  async purge() {
    if (this.purging) return;
    this.purging = true;
    try {
      await purgeOpenViking();
      this.purgeConfirming = false;
      this.purgeOk = true;
      setTimeout(() => { this.purgeOk = false; }, 3000);
      await this.loadStatus();
    } catch (e) {
      showErrorDialog(`${this.t('openViking_purgeFailed')}:${(e instanceof Error && e.message) || String(e || '')}`);
    } finally {
      this.purging = false;
    }
  },
}
```

成功反馈渲染:在**整个 v-if/v-else 按钮组对之后**(不得插在 v-if 的 `</div>` 与 v-else 的 span 之间——插中间会破坏 v-else 邻接,SFC 编译直接红)加一行 `<span v-if="purgeOk" class="mt-1 block text-[11px] text-emerald-600" data-testid="openviking-purge-ok">{{ t('openViking_purgeSuccess') }}</span>`(`showErrorDialog` 已有 import;**不要**引入 showSuccessDialog——仓内无此全局组件,计划评审核实)。

i18n(`zh.js`,照 `agent_integrations_*` 口径:全角标点/「」/「你」):

```js
openViking_purge: '删除云端记忆',
openViking_purgeConfirm: '确认删除',
openViking_purgeHint: '将删除该账户在所有设备的云端记忆,不可恢复。',
openViking_purgeSuccess: '云端记忆已删除。',
openViking_purgeFailed: '删除失败',
openViking_purgeByoHint: 'BYO 模式请在你的部署中自行管理数据。',
```

`en.js` 同键英文(语气对齐现有 en 文案)。

`openViking_description` 改写实(spec §6.2),zh:

```js
openViking_description: '默认开启（托管模式）：每轮对话的用户消息与助手回复会在回合成功后自动上传至云端记忆库，跨设备检索共享；记忆提取由模型服务处理。选择「关闭」并保存即停止上传并移除托管条目（云端已存数据保留，可用「删除云端记忆」清除）。',
```

en 同步;数据事实以 spec §6.2 原文为准。

- [ ] **Step 4: 跑测试确认绿**

Run: `cd app/website && npx vitest run src/components/settings/agent/OpenVikingSettings.test.js`
Expected: PASS

- [ ] **Step 5: 提交(须用户确认)**

```bash
git add app/website/src/services/agentApi.js app/website/src/components/settings/agent/OpenVikingSettings.vue app/website/src/components/settings/agent/OpenVikingSettings.test.js app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "新增:OpenViking 卡删除云端记忆按钮与描述写实(数据与隐私批 §6.2/§6.3)" -- app/website/src
```

---

### Task 3: 「数据与隐私」弹层 + 页脚链接激活(Vue)

**Files:**
- Create: `app/website/src/components/settings/agent/DataPrivacyDialog.vue`
- Modify: `app/website/src/components/SettingsPage.vue`(页脚链接 + 挂载)
- Modify: `app/website/src/i18n/zh.js` / `en.js`
- Test: Create: `app/website/src/components/settings/agent/DataPrivacyDialog.test.js`

- [ ] **Step 1: 写失败测试(i18n 键位完备性 + 导出键清单)**

```js
// 数据与隐私弹层 i18n 完备性(spec §6.1):zh/en 同键、政策文本四要点齐全。
import { describe, expect, it } from 'vitest';
import zh from '../../../i18n/zh.js';
import en from '../../../i18n/en.js';

const REQUIRED = [
  'dataPrivacy_title',
  'dataPrivacy_memory_title',
  'dataPrivacy_memory_body',
  'dataPrivacy_devices_title',
  'dataPrivacy_devices_body',
  'dataPrivacy_delete_title',
  'dataPrivacy_delete_body',
  'dataPrivacy_policy_title',
  'dataPrivacy_policy_body',
];

describe('dataPrivacy i18n 完备性', () => {
  it('zh/en 同键非空', () => {
    for (const key of REQUIRED) {
      expect(String(zh[key] || '')).not.toBe('');
      expect(String(en[key] || '')).not.toBe('');
    }
  });
  it('zh 政策文本覆盖四要点(上传时机/第三方模型/关闭保留/删除通道)', () => {
    const body = zh.dataPrivacy_memory_body + zh.dataPrivacy_delete_body + zh.dataPrivacy_policy_body;
    expect(body).toContain('自动上传');
    expect(body).toContain('模型服务');
    expect(body).toContain('不会删除');
    expect(body).toContain('删除云端记忆');
  });
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd app/website && npx vitest run src/components/settings/agent/DataPrivacyDialog.test.js`
Expected: FAIL(键不存在)

- [ ] **Step 3: 实现 i18n + 组件 + 页脚**

i18n zh(正文照 spec §6.5 定稿文本,此处为骨架,实施时逐字落 §6.5):

```js
dataPrivacy_title: '数据与隐私',
dataPrivacy_memory_title: '对话记忆(OpenViking)',
dataPrivacy_memory_body: '默认开启(托管模式):每轮对话的用户消息与助手回复,在回合成功后自动上传至运营方云端记忆库(openviking.aliang.xin),用于跨设备记忆检索;记忆提取处理会经过运营方配置的模型服务(含第三方大模型供应商)。工具调用记录与模型内部推理不在上传范围。关闭本功能即停止上传,已存数据保留至你主动删除(关闭不会删除)。',
dataPrivacy_devices_title: '设备互访 / 文件传输',
dataPrivacy_devices_body: '默认开启,用于你的设备之间互访与传输;数据仅在你自己的设备之间流转,不经第三方。开关位于对应功能设置页。',
dataPrivacy_delete_title: '删除',
dataPrivacy_delete_body: '你可在「设置 → OpenViking 卡 → 删除云端记忆」一键清除全部云端记忆,该操作不可恢复且作用于你账户的所有设备(BYO 模式请在你的部署中自行管理)。',
dataPrivacy_policy_title: '隐私说明(简版)',
dataPrivacy_policy_body: '本平台默认开启以下功能,数据流向如实描述如上;你随时可以在设置中关闭。正式法律文本以官网发布为准。',
```

en 同键同步翻译。

`DataPrivacyDialog.vue`(最小形态:遮罩 + 面板 + 关闭;不引新依赖):

```vue
<template>
  <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" data-testid="data-privacy-dialog" @click.self="$emit('close')">
    <div class="max-h-[80vh] w-full max-w-lg overflow-y-auto rounded-xl border border-slate-200 bg-white p-6 dark:border-slate-800 dark:bg-background-dark">
      <div class="mb-4 flex items-center justify-between">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('dataPrivacy_title') }}</h2>
        <button type="button" class="text-slate-400 hover:text-slate-600" data-testid="data-privacy-close" @click="$emit('close')">✕</button>
      </div>
      <section v-for="sec in sections" :key="sec.titleKey" class="mb-4">
        <h3 class="text-sm font-bold text-slate-800 dark:text-slate-100">{{ t(sec.titleKey) }}</h3>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t(sec.bodyKey) }}</p>
      </section>
      <section class="mt-6 border-t border-slate-100 pt-4 dark:border-slate-800">
        <h3 class="text-sm font-bold text-slate-800 dark:text-slate-100">{{ t('dataPrivacy_policy_title') }}</h3>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('dataPrivacy_policy_body') }}</p>
      </section>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../../i18n';

// 数据与隐私常驻知情弹层(spec 2026-11-27 §6.1):零弹窗触达纪律——只从
// SettingsPage 页脚「隐私」链接进入,绝不主动弹出。
export default {
  name: 'DataPrivacyDialog',
  props: { open: { type: Boolean, default: false } },
  emits: ['close'],
  setup() {
    const { t } = useI18n();
    const sections = [
      { titleKey: 'dataPrivacy_memory_title', bodyKey: 'dataPrivacy_memory_body' },
      { titleKey: 'dataPrivacy_devices_title', bodyKey: 'dataPrivacy_devices_body' },
      { titleKey: 'dataPrivacy_delete_title', bodyKey: 'dataPrivacy_delete_body' },
    ];
    return { t, sections };
  },
};
</script>
```

`SettingsPage.vue` 页脚(`:308`):死链改活 + 挂载:

```html
<a class="hover:text-primary" href="javascript:void(0)" @click.prevent="dataPrivacyOpen = true">{{ t('settings_privacy') }}</a>
```

script 追加 `dataPrivacyOpen: false` data、import + 模板尾部挂 `<DataPrivacyDialog :open="dataPrivacyOpen" @close="dataPrivacyOpen = false" />`(照该文件既有组件 import/挂载范式)。

- [ ] **Step 4: 跑测试确认绿 + 构建验证**

Run: `cd app/website && npx vitest run src/components/settings/agent/DataPrivacyDialog.test.js && npm run build`
Expected: 测试 PASS;vite build 无错

- [ ] **Step 5: 提交(须用户确认)**

```bash
git add app/website/src/components/settings/agent/DataPrivacyDialog.vue app/website/src/components/settings/agent/DataPrivacyDialog.test.js app/website/src/components/SettingsPage.vue app/website/src/i18n/zh.js app/website/src/i18n/en.js
git commit -m "新增:「数据与隐私」常驻知情弹层并激活页脚隐私链接(数据与隐私批 §6.1)" -- app/website/src
```

---

### Task 4: alianggate 收尾验证

- [ ] **Step 1: 全量构建 + 定向回归**

Run: `go build ./... && go test ./app/http/services/ -run 'TestOpenViking' -count=1 && cd app/website && npm run test && npm run build`
Expected: 全绿(vitest 存量基线红则对照主分支基线判断)

- [ ] **Step 2: 对照 spec §10 条款 1/2/3/4 自检**

人工过一遍:页脚可点/弹层三段/描述写实/BYO 隐藏/关态可用。远程联调项(5/6/7)归 Task 7。

---

### Task 5: OV Admin 删除契约侦察(PhoneServer 批前置,集群内实弹)

**背景:** spec §6.4 侦察项——OpenViking server v0.5.x「删除 user 全部记忆」的 Admin API 形态未探明。实施本任务用 aliangboard MCP 工具(`exec_pod` / `list_resources`)在集群内实测,产出锁定调用形态并回填 Task 6。

- [ ] **Step 1: 定位 openviking pod**

Tool: `mcp__aliangboard__list_resources`(namespace 待定——先 `list_resources` 找 openviking pod;admin base=`http://openviking.nursor.svc:1933` 说明 namespace 是 `nursor`)

- [ ] **Step 2: 探删除契约(用一次性测试 user,不碰真实数据)**

在 openviking pod 内(或 PhoneServer pod,持 ROOT key env)依序探测:

```bash
# 1) 建一次性 user
curl -s -X POST -H "Authorization: Bearer $OPENVIKING_ROOT_KEY" -H 'Content-Type: application/json' \
  -d '{"user_id":"purge-recon-probe","role":"user"}' \
  http://openviking.nursor.svc:1933/api/v1/admin/accounts/aliang/users
# 2) 候选 A:DELETE user(级联删数据?)
curl -s -X DELETE -H "Authorization: Bearer $OPENVIKING_ROOT_KEY" \
  http://openviking.nursor.svc:1933/api/v1/admin/accounts/aliang/users/purge-recon-probe
# 3) 候选 B:purge/clear 子端点
curl -s -X POST -H "Authorization: Bearer $OPENVIKING_ROOT_KEY" \
  http://openviking.nursor.svc:1933/api/v1/admin/accounts/aliang/users/purge-recon-probe/purge
# 4) 验证:GET users 列表确认 probe 已消失;再 GET 其资源面确认为空
```

- [ ] **Step 3: 定谳并回填**

三选一记录到本文件(勾选项):
- [x] 候选 A 可用:`DELETE /api/v1/admin/accounts/{account}/users/{ov_user_id}`,成功 status 含 ok,404=已不存在(幂等成功)

**Task 5 侦察定谳(2026-11-27,集群实弹,探针=一次性 purge-recon-probe,已清场):**

- 成功:**HTTP 202**(非 200/204),响应体 `{"status":"ok","result":{"account_id":"aliang","user_id":"...","status":"deleting","task_id":"..."}}` —— **异步任务式删除**,user 即刻从列表消失,后台任务收尾
- **幂等**:对同一 user 重复 DELETE → 202 且返回**同一 task_id**(不报错)
- **404**:对不存在 user → `{"status":"error","error":{"code":"NOT_FOUND","message":"User not found: ..."}}` —— 视为幂等成功(已不存在=删成)
- ROOT key 形态:ov.conf 写 `${OV_ROOT_KEY}` 占位符,真 key 在 **OV pod env `OV_ROOT_KEY`**(48 字节);PhoneServer 部署形态的 env 里没有 OPENVIKING_ROOT_KEY/ADMIN_BASE(纯 dist 镜像无 .env)——**部署验收(Task 7)时必须先核实 PhoneServer 新部署如何拿到这两个 env**,否则 purge 端点 503 openviking_root_key_unconfigured
- 实现映射:`purgeOvUser` 现有形态已兼容(202 ∈ resp.ok → 成功;404 → 幂等成功;其余非 ok → 502)。**wire 测试的成功用例必须 mock 202**(不是 200),404 用例照旧
- [ ] 候选 B/C 可用:`____________`(未采用——A 直接命中)
- [ ] 全部不可用 → Task 6 走 `openviking_purge_unsupported` 502 语义(未触发——A 可用)

**红线:探针只建/删 `purge-recon-probe` 一次性 user,绝不触碰真实用户数据。**

---

### Task 6: PhoneServer purge 端点(TS,TDD,wire 测试)

**Files:**
- Create: `AliangPhoneServer/server/src/modules/openviking/purgeRoutes.ts`
- Modify: `AliangPhoneServer/server/src/modules/routes/auth.ts`(import + `router.post('/api/agent/openviking/purge', handleOpenVikingPurge)`,照 `:189` credential 先例)
- Test: Create: `AliangPhoneServer/server/test/openvikingPurgeRoutes.wire.test.ts`

- [ ] **Step 1: 写失败 wire 测试(mock 链路照 `openvikingCredentialRoute.wire.test.ts`:vi.mock requireAgentIdentity/getDatabase/global fetch)**

前置:到达 `purgeOvUser` 的用例需要 `process.env.OPENVIKING_ROOT_KEY`(否则 `ovRootKey()` 抛 503)——照 credential wire 测试的 mint 路径做法,在 `beforeEach` 设 `process.env.OPENVIKING_ROOT_KEY = 'test-root-key'`、`afterEach` 恢复。

用例清单(全钉 spec §6.4/§7 契约):

```ts
describe('handleOpenVikingPurge', () => {
  it('无存量行(从未铸 key)→ 200 {status:"ok",purged:true},不发起任何 fetch(幂等)', ...);
  it('有行 + DELETE ok → fetch 打 ADMIN_BASE /api/v1/admin/accounts/{account}/users/{ovUserId},Bearer ROOT key', ...);
  it('DELETE 404 → 仍 200 purged:true(幂等:已不存在=删成)', ...);
  it('DELETE 非 ok 非 404 → ApiError 502 openviking_purge_failed', ...);
  it('fetch 抛错(OV 不可达)→ ApiError 502', ...);
  it('缺 device_id → 400 device_id_required;JWT 无效 → 401(照 credential wire 同款薄契约)', ...);
  it('DB 不可用 → 503 database_unavailable', ...);
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `cd ~/MyProgram/AiProgram/vibe_on_phone/AliangPhoneServer && npx vitest run server/test/openvikingPurgeRoutes.wire.test.ts`
Expected: FAIL(模块不存在)

- [ ] **Step 3: 实现 `purgeRoutes.ts`**

骨架(purge 调用形态按 Task 5 定谳回填;侦察全败则 `purgeOvUser` 直接 throw `ApiError(502,'openviking_purge_unsupported')`):

```ts
// OpenViking 云端记忆删除端点(spec 2026-11-27 数据与隐私 §6.4):
// POST /api/agent/openviking/purge。幂等:从未铸 key(无存量行)→ 200
// purged:true;DELETE 404(已不存在)→ 200。ROOT key 只在本服务 env。
import type { Request, Response, NextFunction } from 'express';
import { ApiError } from '../../errors.js';
import { getDatabase } from '../../store.js';
import { logger } from '../../shared/logging.js';
import { requireAgentIdentity, requestDeviceId } from '../agent/routes.js';
import { ovAccount, ovUserSlug } from './credentialRoutes.js';

// 以下三件从 credentialRoutes.ts export(现文件未导出 ovAdminFetch/ovAdminHeaders
// 的实现处补 export 或在本文件复刻私有helper——优先 export 复用,避免双份超时/头契约)。
import { ovAdminFetch, ovRootKey } from './credentialRoutes.js';

// 删除调用(Task 5 侦察定谳;候选 A 形态,DELETE 404 视为幂等成功)。
async function purgeOvUser(account: string, ovUserId: string): Promise<void> {
  const rootKey = ovRootKey();
  const resp = await ovAdminFetch(
    `/api/v1/admin/accounts/${encodeURIComponent(account)}/users/${encodeURIComponent(ovUserId)}`,
    { method: 'DELETE', headers: { Authorization: `Bearer ${rootKey}` } },
  );
  if (resp.status === 404) return; // 幂等:目标已不存在
  if (!resp.ok) {
    logger.warn(`openviking-purge: admin delete failed user=${ovUserId} status=${resp.status}`);
    throw new ApiError(502, 'openviking_purge_failed');
  }
}

export async function handleOpenVikingPurge(req: Request, res: Response, next: NextFunction): Promise<void> {
  try {
    const deviceId = requestDeviceId(req);
    const identity = await requireAgentIdentity(req, deviceId);
    const db = getDatabase();
    if (!db) throw new ApiError(503, 'database_unavailable');
    const existing = await db.getOpenVikingCredential(identity.userId);
    if (!existing) {
      // 从未铸 key:平台云端无该账户任何数据,幂等成功(spec §6.4)。
      res.json({ status: 'ok', purged: true });
      return;
    }
    await purgeOvUser(existing.accountId || ovAccount(), existing.ovUserId);
    res.json({ status: 'ok', purged: true });
  } catch (error) {
    logger.warn(`openviking-purge: request failed err=${String(error)}`);
    next(error);
  }
}
```

配套:`credentialRoutes.ts` 把 `ovAdminFetch`/`ovRootKey`/`ovAccount` 加 `export`(无行为变化);`ovUserSlug` 已导出。

- [ ] **Step 4: 跑测试确认绿**

Run: `npx vitest run server/test/openvikingPurgeRoutes.wire.test.ts server/test/openvikingCredentialRoute.wire.test.ts server/test/openvikingCredentialRoutes.test.ts`
Expected: 全 PASS(存量 credential 测试不回归)

- [ ] **Step 5: 全量回归 + 提交(须用户确认)**

Run: `npm run test:server`(注意 3 个预存 flaky,对照 stash 基线)

```bash
git add server/src/modules/openviking/purgeRoutes.ts server/src/modules/openviking/credentialRoutes.ts server/src/modules/routes/auth.ts server/test/openvikingPurgeRoutes.wire.test.ts
git commit -m "新增:OpenViking 云端记忆全量删除端点(数据与隐私批 §6.4,幂等/ROOT key 服务端持有)"
```

(PhoneServer 仓提交同样先问用户;该仓如有并行会话同样路径限定。)

---

### Task 7: 部署与真机验收链

- [ ] **Step 1: PhoneServer 部署**(purge 端点先行或同批——spec §9 生效链;部署流程照该仓既有 runbook/镜像管线)
- [ ] **Step 2: alianggate agent 构建装机**(本机构建或出包,照既有 pkg 配方;本机 docker 是 aarch64,amd64 基座必须 `--platform linux/amd64`——历史坑)
- [ ] **Step 3: 对照 spec §10 八条逐项实测**

1. 页脚「隐私」可点,弹层三段+政策全文,zh/en 完整
2. OV 卡描述与行为一致
3. BYO 激活态:无按钮;直调 agent purge → 400 不发云端
4. 关闭态:按钮可用
5. managed+已注册:确认流 → 集群内 OV 数据实测清空(`curl users` 列表/资源面)+ 重复 purge 200 + 全新设备 purge 200
6. 云端不可达:dialog 显原因,capture/拨测不受影响
7. 关闭开关:云端数据保留,关态删除成功
8. 单测全绿(两仓)

- [ ] **Step 4: 验收证据落盘** `docs/superpowers/specs/`(照 dogfood evidence 先例)

---

## 明确不做(spec §3 YAGNI)

弹窗/首绑拦截、同意状态落库、手机端说明页、细粒度删除、自动过期、官网政策页(独立小活后置)、正式法律文本(挂 TODOS,商用前律师)。
