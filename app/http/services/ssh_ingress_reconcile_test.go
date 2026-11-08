package services

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/storage"
	sshin "aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"

	gossh "golang.org/x/crypto/ssh"
)

// newSSHIngressTestKey 生成测试密钥对:authorized_keys 行(进提交载荷)
// 与客户端签名器(验证准入)。
func newSSHIngressTestKey(t *testing.T) (authorizedLine string, signer gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 generate: %v", err)
	}
	signer, err = gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return string(gossh.MarshalAuthorizedKey(signer.PublicKey())), signer
}

// reserveIngressPort 预约一个空闲回环端口(取口即关;TOCTOU 窗口极小)。
func reserveIngressPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// commitIngressConfig 走提交协调路径落一段 customer 配置(失败即 Fatal——
// 本文件断言的是运行面,提交本身必须先成功)。
func commitIngressConfig(t *testing.T, payload string) {
	t.Helper()
	service := NewCustomerConfigService()
	if _, err := service.UpdateCommittedCustomerConfig([]byte(payload)); err != nil {
		t.Fatalf("UpdateCommittedCustomerConfig(%s): %v", payload, err)
	}
}

// assertIngressPortListening 断言端口在监听且是 SSH 服务(横幅首字节)。
func assertIngressPortListening(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s must succeed: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read SSH banner from %s: %v", addr, err)
	}
	if buf[0] != 'S' {
		t.Fatalf("unexpected banner start %q from %s, want 'S' (SSH-...)", buf[0], addr)
	}
}

// assertIngressPortRefused 断言端口连接层拒绝(未监听)。
func assertIngressPortRefused(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("dial 127.0.0.1:%d must be refused, got connection", port)
	}
}

// setupIngressReconcileTest 是三个用例共用的隔离底座:全局配置/提交协调器/
// 配置库三重置+temp HOME+temp cache(入口 host key 落盘隔离)+提交后兜底
// 期望停(进程级 reconcile 单例不泄漏到包内其他用例)。
func setupIngressReconcileTest(t *testing.T) {
	t.Helper()
	config.ResetGlobalConfigForTest()
	config.ResetEffectiveConfigCommitCoordinatorForTest()
	storage.ResetSoftwareConfigDBForTest()
	t.Cleanup(func() {
		sshin.Reconcile(nil, "", nil)
		config.ResetGlobalConfigForTest()
		config.ResetEffectiveConfigCommitCoordinatorForTest()
		storage.ResetSoftwareConfigDBForTest()
	})

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("ALIANG_CACHE_DIR", filepath.Join(tempHome, ".aliang-cache"))

	config.SetGlobalConfig(&config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	})
}

// TestSSHIngressReconcileStartsAndStops 走 customer_config_service 提交协调
// 路径验证接线(Task 7):enable→IsRunning+端口监听+载荷公钥可认证;
// 端口变更→重启(旧口失效/新口监听);disable→停。凭证变更重启的监听器
// 级断言(Port=0 换随机口)在 inbound/ssh 的 reconcile_test.go 单测覆盖。
func TestSSHIngressReconcileStartsAndStops(t *testing.T) {
	setupIngressReconcileTest(t)

	keyLine, keySigner := newSSHIngressTestKey(t)
	port1 := reserveIngressPort(t)
	port2 := reserveIngressPort(t)

	// enable:合法公钥+显式端口 → 提交协调路径 reconcile 起服务。
	commitIngressConfig(t, fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"port":%d,"authorized_keys":[%q]}}}`, port1, keyLine))
	if !sshin.IsRunning() {
		t.Fatal("ssh ingress must be running after enabling commit")
	}
	assertIngressPortListening(t, port1)

	// 认证贯通:提交载荷里的公钥即准入凭证——接线翻译路径
	// (EffectiveSSHIngress→RunConfig 解析→Server)端到端可用。
	conn, err := gossh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port1), &gossh.ClientConfig{
		User:            "ingress",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(keySigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("authenticated dial against committed ingress: %v", err)
	}
	_ = conn.Close()

	// 端口变更:重启——旧口失效、新口监听。
	commitIngressConfig(t, fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"port":%d,"authorized_keys":[%q]}}}`, port2, keyLine))
	if !sshin.IsRunning() {
		t.Fatal("ssh ingress must stay running after port change")
	}
	assertIngressPortRefused(t, port1)
	assertIngressPortListening(t, port2)

	// disable(deep merge 只动 enable):期望停。
	commitIngressConfig(t, `{"customer":{"ssh_ingress":{"enable":false}}}`)
	if sshin.IsRunning() {
		t.Fatal("ssh ingress must stop after disabling commit")
	}
	assertIngressPortRefused(t, port2)
}

// TestSSHIngressReconcileStartFailureDoesNotBlockCommit 钉住「失败仅日志不
// 阻断配置提交」:端口被外部占住 → reconcile 起服失败停留止态,但提交
// 本身必须成功;冲突消失后再次提交(协调器恒回调,不做内容去重)重试起服。
func TestSSHIngressReconcileStartFailureDoesNotBlockCommit(t *testing.T) {
	setupIngressReconcileTest(t)

	keyLine, _ := newSSHIngressTestKey(t)
	occupied := reserveIngressPort(t)

	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", occupied))
	if err != nil {
		t.Fatalf("occupy port %d: %v", occupied, err)
	}

	payload := fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"port":%d,"authorized_keys":[%q]}}}`, occupied, keyLine)
	service := NewCustomerConfigService()
	if _, err := service.UpdateCommittedCustomerConfig([]byte(payload)); err != nil {
		t.Fatalf("commit must not be blocked by ssh ingress reconcile failure: %v", err)
	}
	if sshin.IsRunning() {
		t.Fatal("ingress with unbindable port must stay stopped")
	}

	// 释放端口后重复提交:reconcile 重试起服(失败不粘死到下次进程重启)。
	_ = l.Close()
	commitIngressConfig(t, payload)
	if !sshin.IsRunning() {
		t.Fatal("ingress must start on retry once the port conflict is gone")
	}
	assertIngressPortListening(t, occupied)
}

// TestBuildSSHIngressRunConfigUnparseableKeyDefense 钉住翻译层的坏值防御:
// 保存通道 Validate 已拦合法提交,手改文件绕过校验的坏行在此兜底——
// 混合列表按好部分继续跑;全量解析失败返回 nil(调用方按期望停处理)。
func TestBuildSSHIngressRunConfigUnparseableKeyDefense(t *testing.T) {
	if rc := buildSSHIngressRunConfig(&config.SSHIngressConfig{
		Port:               config.SSHIngressDefaultPort,
		AuthorizedKeys:     []string{"not-a-key", "also not a key"},
		MeshAuthorizedKeys: []string{"still not a key"},
	}); rc != nil {
		t.Fatalf("all-unparseable keys must yield nil run config, got %+v", rc)
	}

	keyLine, _ := newSSHIngressTestKey(t)
	rc := buildSSHIngressRunConfig(&config.SSHIngressConfig{
		Port:               config.SSHIngressDefaultPort,
		AuthorizedKeys:     []string{"not-a-key", keyLine},
		MeshAuthorizedKeys: []string{keyLine},
	})
	if rc == nil {
		t.Fatal("partially parseable config must yield a run config")
	}
	if len(rc.AuthorizedKeys) != 1 || len(rc.MeshKeys) != 1 {
		t.Fatalf("unparseable lines must be skipped: authorized=%d mesh=%d, want 1/1", len(rc.AuthorizedKeys), len(rc.MeshKeys))
	}
	if rc.Port != config.SSHIngressDefaultPort {
		t.Fatalf("port = %d, want %d", rc.Port, config.SSHIngressDefaultPort)
	}
	if rc.ForwardGate != nil {
		t.Fatal("ForwardGate must stay nil (merge integration seam, see RunConfig comment)")
	}
	if rc.LocalNodeID != nil {
		t.Fatal("LocalNodeID must be left for Reconcile injection, not set at translation")
	}
}

// TestReconcileSSHIngressFromConfigBootRestore 钉住启动期恢复入口(与提交
// 协调路径共用的 ReconcileSSHIngressFromConfig):加载了 enable 配置的状态
// 下钩子执行后入口在跑且端口监听;禁用/nil 配置不启且停掉在跑实例。
// core 启动序列(daemon initializeCoreSubsystems 与 interactive runStart)
// 各以一行调用接线——本用例即该一行的可测单元。
func TestReconcileSSHIngressFromConfigBootRestore(t *testing.T) {
	setupIngressReconcileTest(t)

	keyLine, _ := newSSHIngressTestKey(t)
	port := reserveIngressPort(t)
	enable := true
	enabledCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{SSHIngress: &config.SSHIngressConfig{
			Enable:         &enable,
			Port:           uint16(port),
			AuthorizedKeys: []string{keyLine},
		}},
	}

	// 启动钩子语义:enable 配置已加载 → 自动恢复入口。
	ReconcileSSHIngressFromConfig(enabledCfg)
	if !sshin.IsRunning() {
		t.Fatal("boot reconcile must restore an enabled ssh ingress")
	}
	assertIngressPortListening(t, port)

	// 禁用配置 → 不启并停掉在跑实例(Reconcile 期望态语义)。
	ReconcileSSHIngressFromConfig(&config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	})
	if sshin.IsRunning() {
		t.Fatal("boot reconcile with disabled config must stop the ingress")
	}
	assertIngressPortRefused(t, port)

	// nil 配置(启动极早期全局配置尚未就绪的防御)→ 期望停,不 panic。
	ReconcileSSHIngressFromConfig(nil)
	if sshin.IsRunning() {
		t.Fatal("boot reconcile with nil config must keep the ingress stopped")
	}

	// 再次 enable:恢复(启动钩子重入路径,幂等)。
	ReconcileSSHIngressFromConfig(enabledCfg)
	if !sshin.IsRunning() {
		t.Fatal("boot reconcile must restore the ingress again after re-enable")
	}
	assertIngressPortListening(t, port)
}
