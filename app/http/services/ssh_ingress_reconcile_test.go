package services

// SSH 入口 reconcile 接线测试(统一口收编后,spec §6.4):入口不再自持
// 监听,「起/停」的观测面从端口横幅换成 IsRunning + 注入缝上的真实握手;
// port 相关断言删除(customer.ssh_ingress.port 运行面忽略)。

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

// bridgedIngressPair 构造写解耦的双工对(裸 net.Pipe 双向同步会与 gossh
// 「两端都先写版本」的握手互等死锁;泵桥接后与真实 TCP 缓冲语义对齐)。
// 与 inbound/ssh 桥接 helper 同构,跨包不可导出故就地复制。
func bridgedIngressPair() (client, server net.Conn) {
	c1, s1 := net.Pipe()
	c2, s2 := net.Pipe()
	go func() { _, _ = io.Copy(s2, s1); _ = s2.Close() }()
	go func() { _, _ = io.Copy(s1, s2); _ = s1.Close() }()
	return c1, c2
}

// ingressHandshakeOK 经 HandleIngressConn 注入桥接连接跑完整 gossh 握手:
// true=提交载荷里的公钥确实被运行面接纳(翻译→解析→认证回调端到端贯通)。
func ingressHandshakeOK(t *testing.T, signer gossh.Signer) bool {
	t.Helper()
	client, server := bridgedIngressPair()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go sshin.HandleIngressConn(server)
	cfg := &gossh.ClientConfig{
		User:            "ingress",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, _, _, err := gossh.NewClientConn(client, "127.0.0.1:0", cfg)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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

// setupIngressReconcileTest 是几个用例共用的隔离底座:全局配置/提交协调器/
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
// 路径验证接线:enable(无端口)→统一口承载(专用口 Addr 空)+载荷公钥可认证;
// 显式端口→另开常驻专用口(Addr 暴露);56442 哨兵→专用口收回(端到端归零);
// disable→停+注入缝拒收。凭证变更重启的连接级断言在 inbound/ssh 的
// reconcile_test.go 单测覆盖。
func TestSSHIngressReconcileStartsAndStops(t *testing.T) {
	setupIngressReconcileTest(t)

	keyLine, keySigner := newSSHIngressTestKey(t)

	// enable:合法公钥、不带端口 → 统一口承载,专用口 Addr 空,认证贯通。
	commitIngressConfig(t, fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"authorized_keys":[%q]}}}`, keyLine))
	if !sshin.IsRunning() {
		t.Fatal("ssh ingress must be running after enabling commit")
	}
	if got := sshin.Addr(); got != "" {
		t.Fatalf("unified mode must not open dedicated listener, Addr = %q", got)
	}
	if !ingressHandshakeOK(t, keySigner) {
		t.Fatal("committed authorized key must authenticate via the injection seam")
	}

	// 显式端口:另开常驻专用口(Addr 暴露该口)。
	fixed := reserveIngressPort(t)
	commitIngressConfig(t, fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"port":%d,"authorized_keys":[%q]}}}`, fixed, keyLine))
	if !sshin.IsRunning() {
		t.Fatal("ssh ingress must stay running after explicit port commit")
	}
	if _, port, err := net.SplitHostPort(sshin.Addr()); err != nil || port != strconv.Itoa(fixed) {
		t.Fatalf("Addr = %q, want dedicated port %d", sshin.Addr(), fixed)
	}

	// 56442 哨兵(旧默认):端到端归零 → 专用口收回,回归统一口承载。
	commitIngressConfig(t, fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"port":56442,"authorized_keys":[%q]}}}`, keyLine))
	if !sshin.IsRunning() {
		t.Fatal("sentinel port commit must keep ingress running")
	}
	if got := sshin.Addr(); got != "" {
		t.Fatalf("56442 sentinel must withdraw dedicated listener, Addr = %q", got)
	}

	// disable(deep merge 只动 enable):期望停,注入缝拒收。
	commitIngressConfig(t, `{"customer":{"ssh_ingress":{"enable":false}}}`)
	if sshin.IsRunning() {
		t.Fatal("ssh ingress must stop after disabling commit")
	}
	client, server := net.Pipe()
	defer client.Close()
	if sshin.HandleIngressConn(server) {
		t.Fatal("HandleIngressConn must report false after disabling commit")
	}
}

// TestSSHIngressReconcileStartFailureDoesNotBlockCommit 钉住「失败仅日志不
// 阻断配置提交」:host key 目录不可创建(cache/ssh 子目录被普通文件冒名)
// → reconcile 起服失败停留止态,但提交本身必须成功;障碍消失后再次提交
// (协调器恒回调,不做内容去重)重试起服。注入面刻意收窄到 ssh 子目录——
// 整个缓存根坏掉会把提交自身的 db 阶段一起打断,测不到目标语义。
func TestSSHIngressReconcileStartFailureDoesNotBlockCommit(t *testing.T) {
	setupIngressReconcileTest(t)

	// cache 根可用,但 cache/ssh 是普通文件:GetCacheSubdir("ssh") 失败 →
	// host key 路径解析为空 → 按期望停处理。
	cacheRoot := os.Getenv("ALIANG_CACHE_DIR")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatalf("create cache root: %v", err)
	}
	sshSubdir := filepath.Join(cacheRoot, "ssh")
	if err := os.WriteFile(sshSubdir, []byte("x"), 0o600); err != nil {
		t.Fatalf("forge cache/ssh as plain file: %v", err)
	}

	keyLine, keySigner := newSSHIngressTestKey(t)
	payload := fmt.Sprintf(`{"customer":{"ssh_ingress":{"enable":true,"authorized_keys":[%q]}}}`, keyLine)
	service := NewCustomerConfigService()
	if _, err := service.UpdateCommittedCustomerConfig([]byte(payload)); err != nil {
		t.Fatalf("commit must not be blocked by ssh ingress reconcile failure: %v", err)
	}
	if sshin.IsRunning() {
		t.Fatal("ingress with unusable host key dir must stay stopped")
	}

	// 障碍消除(移除冒名文件,子目录可正常创建)后重复提交:reconcile 重试起服。
	if err := os.Remove(sshSubdir); err != nil {
		t.Fatalf("remove obstacle: %v", err)
	}
	commitIngressConfig(t, payload)
	if !sshin.IsRunning() {
		t.Fatal("ingress must start on retry once the host key dir is usable")
	}
	if !ingressHandshakeOK(t, keySigner) {
		t.Fatal("handshake through retried ingress")
	}
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
	// 旧默认 56442 是哨兵:翻译层归零=跟随统一口(spec §6.4 修订)。
	if rc.Port != 0 {
		t.Fatalf("port = %d, want 0 (56442 sentinel maps to unified ingress)", rc.Port)
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
// 下钩子执行后入口在跑;禁用/nil 配置不启且停掉在跑实例。core 启动序列
// (daemon initializeCoreSubsystems 与 interactive runStart)各以一行调用
// 接线——本用例即该一行的可测单元。
func TestReconcileSSHIngressFromConfigBootRestore(t *testing.T) {
	setupIngressReconcileTest(t)

	keyLine, _ := newSSHIngressTestKey(t)
	enable := true
	enabledCfg := &config.Config{
		Core: &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{SSHIngress: &config.SSHIngressConfig{
			Enable:         &enable,
			Port:           config.SSHIngressDefaultPort,
			AuthorizedKeys: []string{keyLine},
		}},
	}

	// 启动钩子语义:enable 配置已加载 → 自动恢复入口。
	ReconcileSSHIngressFromConfig(enabledCfg)
	if !sshin.IsRunning() {
		t.Fatal("boot reconcile must restore an enabled ssh ingress")
	}

	// 禁用配置 → 不启并停掉在跑实例(Reconcile 期望态语义)。
	ReconcileSSHIngressFromConfig(&config.Config{
		Core:     &config.CoreConfig{APIServer: "https://api.aliang.one"},
		Customer: &config.CustomerConfig{},
	})
	if sshin.IsRunning() {
		t.Fatal("boot reconcile with disabled config must stop the ingress")
	}

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
}
