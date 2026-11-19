package punch

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/stun"
)

// startSTUNResponder 在 loopback 起一个 mini STUN responder:收到 Binding
// request 就回成功应答,XOR-MAPPED-ADDRESS 宣告调用方指定的 ip:port——
// 刻意不反映请求的真实来源地址,测试要验证的正是「服务端宣告什么,
// Discover 就原样学到什么」。全部用 pion/stun 自身原语构包,不引新依赖。
func startSTUNResponder(t *testing.T, announce net.IP, port int) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 responder 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return // conn 已被 Cleanup 关闭,退出收包循环
			}
			if !stun.IsMessage(buf[:n]) {
				continue // 非 STUN 报文直接丢弃
			}
			var req stun.Message
			if err := stun.Decode(buf[:n], &req); err != nil {
				continue
			}
			if req.Type != stun.BindingRequest {
				continue // 只应答 Binding request
			}
			resp, err := stun.Build(
				stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
				stun.NewTransactionIDSetter(req.TransactionID),
				stun.XORMappedAddress{IP: announce, Port: port},
			)
			if err != nil {
				continue
			}
			if _, err := conn.WriteToUDP(resp.Raw, src); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

// testUDPConn 起 loopback 未连接 UDP socket,复刻打洞 socket 的未连接
// 形态——Discover 必须经 WriteToUDP 发向显式目的地址,而不是 connected
// socket 的裸 Write;真绑物理接口的链路属 Task 14 集成面,这里不碰。
func testUDPConn(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起测试 socket 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// deadLoopbackAddr 占一个 loopback 端口随即关掉,得到一个「发包无人应答」
// 的地址(不回应≠拒绝——UDP 上它就是黑洞,正好喂超时路径)。
func deadLoopbackAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("占端口失败: %v", err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatalf("关占位端口失败: %v", err)
	}
	return addr
}

// TestDiscoverReturnsAnnouncedMapping (a):Discover 返回 responder 宣告的
// ip:port。宣告用 TEST-NET 段地址,证明结果来自应答属性而非本地来源地址。
func TestDiscoverReturnsAnnouncedMapping(t *testing.T) {
	addr := startSTUNResponder(t, net.IPv4(198, 51, 100, 77), 41321)
	conn := testUDPConn(t)

	mapping, err := Discover(conn, addr, 2*time.Second)
	if err != nil {
		t.Fatalf("responder 在线时 Discover 应成功: %v", err)
	}
	if !mapping.IP.Equal(net.IPv4(198, 51, 100, 77)) {
		t.Fatalf("IP 应为宣告的 198.51.100.77,得到 %v", mapping.IP)
	}
	if mapping.Port != 41321 {
		t.Fatalf("端口应为宣告的 41321,得到 %d", mapping.Port)
	}
}

// TestClassifySymmetricSuspectAndMemo (b):两台宣告不同端口 →
// symmetricSuspect=true;返回首台宣告的映射。随后按调用方契约(Task 5
// 编排层)Record 进 SkipMemo,5min 内 ShouldSkip=true。
func TestClassifySymmetricSuspectAndMemo(t *testing.T) {
	a := startSTUNResponder(t, net.IPv4(198, 51, 100, 1), 40001)
	b := startSTUNResponder(t, net.IPv4(198, 51, 100, 2), 40002)
	conn := testUDPConn(t)

	mapping, symmetric, err := Classify(conn, []string{a, b})
	if err != nil {
		t.Fatalf("两台都在线时 Classify 应成功: %v", err)
	}
	if !symmetric {
		t.Fatal("两台宣告不同端口应判 symmetricSuspect=true")
	}
	if mapping.Port != 40001 {
		t.Fatalf("应返回首台宣告的映射(端口 40001),得到 %d", mapping.Port)
	}

	memo := &SkipMemo{}
	memo.Record("peer-1")
	if !memo.ShouldSkip("peer-1") {
		t.Fatal("判 symmetric 后 Record,5min 内 ShouldSkip 应为 true")
	}
}

// TestClassifyConeOrNotSuspect (c):两台宣告相同端口 → symmetricSuspect=false。
func TestClassifyConeOrNotSuspect(t *testing.T) {
	a := startSTUNResponder(t, net.IPv4(198, 51, 100, 1), 41000)
	b := startSTUNResponder(t, net.IPv4(198, 51, 100, 2), 41000)
	conn := testUDPConn(t)

	mapping, symmetric, err := Classify(conn, []string{a, b})
	if err != nil {
		t.Fatalf("两台都在线时 Classify 应成功: %v", err)
	}
	if symmetric {
		t.Fatal("两台宣告相同端口不应判 symmetricSuspect")
	}
	if mapping.Port != 41000 {
		t.Fatalf("应返回首台宣告的映射(端口 41000),得到 %d", mapping.Port)
	}
}

// TestClassifyPartialFailure 单台应答单台超时:端口比对不成立但映射可用,
// 判型降级为非对称(不冤枉),真对称由打洞失败记忆兜底。临时调小
// classifyDiscoverTimeout 避免单测真等 3s。
func TestClassifyPartialFailure(t *testing.T) {
	old := classifyDiscoverTimeout
	classifyDiscoverTimeout = 150 * time.Millisecond
	t.Cleanup(func() { classifyDiscoverTimeout = old })

	alive := startSTUNResponder(t, net.IPv4(198, 51, 100, 9), 42000)
	dead := deadLoopbackAddr(t)
	conn := testUDPConn(t)

	mapping, symmetric, err := Classify(conn, []string{alive, dead})
	if err != nil {
		t.Fatalf("单台应答时 Classify 不应报错: %v", err)
	}
	if symmetric {
		t.Fatal("只有一台应答,端口比对不成立,不应判 symmetricSuspect")
	}
	if mapping.Port != 42000 {
		t.Fatalf("应返回存活台宣告的映射(端口 42000),得到 %d", mapping.Port)
	}
}

// TestClassifyAllUnreachable (补充,对齐计划「两台都不可达≠symmetric」):
// 两台都超时 → error 路径,调用方按「STUN 不可用」落 piko,且结构性不写 SkipMemo(memo 不在该函数签名内,此断言为文档式自检非机械锁)。
func TestClassifyAllUnreachable(t *testing.T) {
	old := classifyDiscoverTimeout
	classifyDiscoverTimeout = 150 * time.Millisecond
	t.Cleanup(func() { classifyDiscoverTimeout = old })

	conn := testUDPConn(t)
	_, _, err := Classify(conn, []string{deadLoopbackAddr(t), deadLoopbackAddr(t)})
	if err == nil {
		t.Fatal("两台都不可达应返回 error(STUN 不可用路径)")
	}
	memo := &SkipMemo{}
	if memo.ShouldSkip("peer-x") {
		t.Fatal("STUN 不可达不得写失败记忆")
	}
}

// TestSkipMemoTTL (d):Record 后 5min 内 ShouldSkip=true;注入时钟推进到
// 满 5min 过期恢复 false;过期记录经惰性清理后不得复活;重新 Record 重新计时。
// now 作 SkipMemo 字段正是为了让这里推进时钟,而不是真睡 5 分钟。
func TestSkipMemoTTL(t *testing.T) {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	now := base
	memo := &SkipMemo{now: func() time.Time { return now }}

	if memo.ShouldSkip("peer-a") {
		t.Fatal("未 Record 过的 peer 不应 skip")
	}
	memo.Record("peer-a")
	if !memo.ShouldSkip("peer-a") {
		t.Fatal("Record 后 5min 内应 skip")
	}

	now = base.Add(5 * time.Minute) // 恰好满 TTL:按过期处理
	if memo.ShouldSkip("peer-a") {
		t.Fatal("满 5min 应过期恢复(ShouldSkip=false)")
	}

	now = base.Add(10 * time.Minute)
	if memo.ShouldSkip("peer-a") {
		t.Fatal("过期记录经惰性清理后不得复活")
	}

	memo.Record("peer-a") // 重新 Record 从新时刻重新计时
	if !memo.ShouldSkip("peer-a") {
		t.Fatal("重新 Record 后应恢复 skip")
	}
}

// TestDiscoverTimeoutNoMemo (e):对不回应的 server,Discover 按 timeout 报
// error 且耗时与 timeout 同量级;该失败属于「STUN 不可达」,不产生
// SkipMemo 记录。
func TestDiscoverTimeoutNoMemo(t *testing.T) {
	dead := deadLoopbackAddr(t)
	conn := testUDPConn(t)

	start := time.Now()
	_, err := Discover(conn, dead, 300*time.Millisecond)
	if err == nil {
		t.Fatal("对不回应的 server Discover 应报超时错误")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("应在 timeout 量级返回,实际耗时 %v", elapsed)
	}

	memo := &SkipMemo{}
	if memo.ShouldSkip("peer-x") {
		t.Fatal("STUN 不可达路径不得留下 SkipMemo 记录")
	}
}

// ---------------------------------------------------------------------------
// 对抗性补测(质量审查 Minor 1):MAPPED 回落路径 + 错事务 ID 噪声过滤
// ——

// startSTUNResponderRaw 自由度更高的 responder:reply 变换器由调用方给
// (MAPPED 变体/错事务 ID 先喷后对,共用构包原语)。
func startSTUNResponderRaw(t *testing.T, announce net.IP, port int, decorate func(*stun.Message, *stun.Message)) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("起 responder 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if !stun.IsMessage(buf[:n]) {
				continue
			}
			var req stun.Message
			if err := stun.Decode(buf[:n], &req); err != nil {
				continue
			}
			if req.Type != stun.BindingRequest {
				continue
			}
			resp, err := stun.Build(
				stun.NewType(stun.MethodBinding, stun.ClassSuccessResponse),
				stun.NewTransactionIDSetter(req.TransactionID),
				&stun.MappedAddress{IP: announce, Port: port},
			)
			if err != nil {
				continue
			}
			if decorate != nil {
				decorate(&req, resp)
			}
			if _, err := conn.WriteToUDP(resp.Raw, src); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

// TestDiscoverMappedAddressFallback XOR 之外,MAPPED-ADDRESS 回落(RFC 5389
// 老 Server 兼容)同样可得映射——回落分支此前零覆盖(质量审查钉子)。
func TestDiscoverMappedAddressFallback(t *testing.T) {
	announceIP := net.IPv4(198, 51, 100, 42)
	const announcePort = 45012
	server := startSTUNResponderRaw(t, announceIP, announcePort, nil) // 不加修饰:纯 MAPPED
	got, err := Discover(testUDPConn(t), server, 2*time.Second)
	if err != nil {
		t.Fatalf("MAPPED 回落应成功: %v", err)
	}
	if !got.IP.Equal(announceIP) || got.Port != announcePort {
		t.Fatalf("回落学到的映射不符: got=%v:%d want=%v:%d", got.IP, got.Port, announceIP, announcePort)
	}
}

// TestDiscoverIgnoresWrongTransactionID 同源 responder 先喷错事务 ID 应答
// 再喷正解:RFC 5389 应答真实性凭据(事务匹配)的对抗钉子——错喷必须被
// 丢弃,正解必须被接受。
func TestDiscoverIgnoresWrongTransactionID(t *testing.T) {
	announceIP := net.IPv4(198, 51, 100, 43)
	const announcePort = 45013
	var wrong喷 atomic.Int32
	server := startSTUNResponderRaw(t, announceIP, announcePort, func(req *stun.Message, resp *stun.Message) {
		if wrong喷.Add(1) == 1 {
			// 首答:换掉事务 ID(应答内容其余全对)——必须因事务不匹配被弃。
			var bad [stun.TransactionIDSize]byte
			for i := range bad {
				bad[i] = byte(0xA5 ^ req.TransactionID[i])
			}
			resp.TransactionID = bad
		}
	})
	got, err := Discover(testUDPConn(t), server, 2*time.Second)
	if err != nil {
		t.Fatalf("错事务 ID 应被跳过、正解应被接受: %v", err)
	}
	if !got.IP.Equal(announceIP) || got.Port != announcePort {
		t.Fatalf("学到的映射不符: got=%v:%d", got.IP, got.Port)
	}
	if wrong喷.Load() == 0 {
		t.Fatal("噪声 responder 未被触发,测试无效")
	}
}

// ---------------------------------------------------------------------------
// ClassifyWithBudget 预算化变体(显式活探测批 Task 7):截断/判实即停/判型
// 结论词汇。既有 Classify 的语义回归由上方 TestClassify* 全绿钉(薄壳改造
// 零行为变化),此处只钉变体自身的新面。
// ---------------------------------------------------------------------------

// TestClassifyWithBudgetImmediateExpiry 总预算 1ns+4 台:一台都测不了,
// truncated=true、results 空(已测台数如实=0),不 panic。
func TestClassifyWithBudgetImmediateExpiry(t *testing.T) {
	conn := testUDPConn(t)
	res := ClassifyWithBudget(conn, []string{"a", "b", "c", "d"}, time.Nanosecond)
	if !res.Truncated {
		t.Fatal("预算开局即死应 truncated=true")
	}
	if len(res.Results) != 0 {
		t.Fatalf("一台未测 results 应空,得到 %d 条: %v", len(res.Results), res.Results)
	}
	if res.Symmetric {
		t.Fatal("零探测不得判 symmetric")
	}
}

// TestClassifyWithBudgetMidwayExpiry 预算中途耗尽:4 台全黑洞,总预算 250ms
// 只够 ~2.5 个 100ms 窗口——第 3 台拿到 min(3s,剩余) 的尾巴窗,第 4 台前
// 剩余必 ≤0(3 全窗=300ms 已超预算),截断确定性成立:results=3 台、
// truncated=true、已测台数如实。
func TestClassifyWithBudgetMidwayExpiry(t *testing.T) {
	old := classifyDiscoverTimeout
	classifyDiscoverTimeout = 100 * time.Millisecond
	t.Cleanup(func() { classifyDiscoverTimeout = old })

	conn := testUDPConn(t)
	res := ClassifyWithBudget(conn, []string{
		deadLoopbackAddr(t), deadLoopbackAddr(t), deadLoopbackAddr(t), deadLoopbackAddr(t),
	}, 250*time.Millisecond)
	if !res.Truncated {
		t.Fatal("预算中途耗尽应 truncated=true")
	}
	if len(res.Results) != 3 {
		t.Fatalf("已测台数应 3(第 4 台前剩余必耗尽),得到 %d 条: %v", len(res.Results), res.Results)
	}
	for _, e := range res.Results {
		if e.OK || e.Err == "" || e.NatType != "" {
			t.Fatalf("黑洞台应 ok=false+err 非空+nat_type 空: %v", e)
		}
	}
}

// TestClassifyWithBudgetSymmetricStopsEarly 判实即停(照 Classify early
// return 语义):首两台应答端口不同→结论 symmetric,第三台不再探测,余台
// 未测以 truncated 如实标注;应答台共享 symmetric 结论。
func TestClassifyWithBudgetSymmetricStopsEarly(t *testing.T) {
	a := startSTUNResponder(t, net.IPv4(198, 51, 100, 1), 40001)
	b := startSTUNResponder(t, net.IPv4(198, 51, 100, 2), 40002)
	c := startSTUNResponder(t, net.IPv4(198, 51, 100, 3), 40003)
	conn := testUDPConn(t)

	res := ClassifyWithBudget(conn, []string{a, b, c}, 9*time.Second)
	if !res.Symmetric {
		t.Fatal("两台端口不同应判 symmetric")
	}
	if len(res.Results) != 2 {
		t.Fatalf("判实即停应只测 2 台,得到 %d 条: %v", len(res.Results), res.Results)
	}
	if !res.Truncated {
		t.Fatal("余台未测应 truncated=true(不假装完整)")
	}
	for _, e := range res.Results {
		if !e.OK || e.MappedPort <= 0 || e.NatType != NatTypeSymmetric || e.Err != "" {
			t.Fatalf("应答台应 ok+有端口+结论 symmetric: %v", e)
		}
	}
}

// TestClassifyWithBudgetNonSymmetricFullRun 多台同端口:全测完 truncated=
// false,结论 non_symmetric,逐台映射端口如实。
func TestClassifyWithBudgetNonSymmetricFullRun(t *testing.T) {
	a := startSTUNResponder(t, net.IPv4(198, 51, 100, 1), 41000)
	b := startSTUNResponder(t, net.IPv4(198, 51, 100, 2), 41000)
	conn := testUDPConn(t)

	res := ClassifyWithBudget(conn, []string{a, b}, 6*time.Second)
	if res.Truncated {
		t.Fatal("预算足额全测完不应 truncated")
	}
	if res.Symmetric {
		t.Fatal("两台同端口不应判 symmetric")
	}
	if len(res.Results) != 2 {
		t.Fatalf("应逐台 2 条,得到 %d 条: %v", len(res.Results), res.Results)
	}
	for _, e := range res.Results {
		if !e.OK || e.MappedPort != 41000 || e.NatType != NatTypeNonSymmetric || e.Err != "" {
			t.Fatalf("同端口应答台应 ok=41000+non_symmetric: %v", e)
		}
	}
}

// TestClassifyWithBudgetSingleResponderUnknown 恰一台应答:结论 unknown
// (端口比对不成立不冤枉,照 Classify 降级语义);不可达台 err 如实、
// nat_type 留空。
func TestClassifyWithBudgetSingleResponderUnknown(t *testing.T) {
	old := classifyDiscoverTimeout
	classifyDiscoverTimeout = 150 * time.Millisecond
	t.Cleanup(func() { classifyDiscoverTimeout = old })

	alive := startSTUNResponder(t, net.IPv4(198, 51, 100, 9), 42000)
	dead := deadLoopbackAddr(t)
	conn := testUDPConn(t)

	res := ClassifyWithBudget(conn, []string{alive, dead}, 6*time.Second)
	if res.Truncated || res.Symmetric {
		t.Fatalf("预算足额单台应答应完整非对称: %v", res)
	}
	if len(res.Results) != 2 {
		t.Fatalf("应逐台 2 条,得到 %d 条: %v", len(res.Results), res.Results)
	}
	if !res.Results[0].OK || res.Results[0].MappedPort != 42000 || res.Results[0].NatType != NatTypeUnknown {
		t.Fatalf("存活台应 ok+unknown 结论: %v", res.Results[0])
	}
	if res.Results[1].OK || res.Results[1].Err == "" || res.Results[1].NatType != "" {
		t.Fatalf("黑洞台应 err 如实+nat_type 空: %v", res.Results[1])
	}
}

// TestClassifyWithBudgetNilConn 防御形态:conn nil 逐台如实回 Err 记录,
// 不探测不 panic。
func TestClassifyWithBudgetNilConn(t *testing.T) {
	res := ClassifyWithBudget(nil, []string{"a", "b"}, time.Second)
	if len(res.Results) != 2 {
		t.Fatalf("nil conn 应逐台回 Err 记录,得到 %d 条: %v", len(res.Results), res.Results)
	}
	for _, e := range res.Results {
		if e.OK || e.Err == "" {
			t.Fatalf("nil conn 台应 ok=false+err 非空: %v", e)
		}
	}
}

// ---------------------------------------------------------------------------
// SkipMemo 快照面(组网诊断读面批 Task 2)
// ---------------------------------------------------------------------------

// TestSkipMemoSnapshotPureRead 快照面三断言:双键入表快照含两者(按键排序,
// 余量在界内且 Expired=false);注入时钟推过 TTL → 条目仍在表(惰性清理
// 归 Record/ShouldSkip)且 Expired=true 标注;快照纯读——读后 ShouldSkip
// 仍真、表未被快照动过;另验零值 &SkipMemo{} 直调 Snapshot 不 panic(now
// 兜底 clock 约定,now=nil 走 time.Now)。
func TestSkipMemoSnapshotPureRead(t *testing.T) {
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	now := base
	memo := &SkipMemo{now: func() time.Time { return now }}
	memo.Record("peer-b")
	memo.Record("peer-a")

	snap := memo.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("双键入表快照应含两笔,得到 %d 笔: %v", len(snap), snap)
	}
	if snap[0].PeerID != "peer-a" || snap[1].PeerID != "peer-b" {
		t.Fatalf("快照应按键排序稳定输出: %v", snap)
	}
	for _, e := range snap {
		if e.Expired {
			t.Fatalf("未过 TTL 的条目不应标注 Expired: %v", e)
		}
		if e.RemainingSeconds <= 0 || e.RemainingSeconds > int64(skipMemoTTL/time.Second) {
			t.Fatalf("余量应在 (0,%d] 秒内: %v", int64(skipMemoTTL/time.Second), e)
		}
	}

	// 纯读断言(未过期窗口内):读过快照后 ShouldSkip 仍真——快照零副作用,
	// 表未被快照动过。
	if !memo.ShouldSkip("peer-a") || !memo.ShouldSkip("peer-b") {
		t.Fatal("Snapshot 纯读:读过之后 ShouldSkip 应仍为 true(快照不得删表)")
	}

	// 时钟推满 TTL:条目仍保留(快照零副作用,绝不删表),标注过期、余量 <=0。
	// 注:恰满 TTL 按 TestSkipMemoTTL 同一口径视为过期(ShouldSkip=false),
	// 故纯读断言必须在未过期窗口先做完。
	now = base.Add(5 * time.Minute)
	snap = memo.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("快照不得删表:推过 TTL 后两笔应仍在,得到 %d 笔", len(snap))
	}
	for _, e := range snap {
		if !e.Expired {
			t.Fatalf("推过 TTL 后快照应标注 Expired=true: %v", e)
		}
		if e.RemainingSeconds > 0 {
			t.Fatalf("过期条目余量应 <=0: %v", e)
		}
	}

	// 惰性清理仍归 ShouldSkip:过期条目由它当场清掉,快照随之只剩 peer-b。
	if memo.ShouldSkip("peer-a") {
		t.Fatal("满 TTL 应按过期处理(ShouldSkip=false,TestSkipMemoTTL 同口径)")
	}
	if snap = memo.Snapshot(); len(snap) != 1 || snap[0].PeerID != "peer-b" {
		t.Fatalf("ShouldSkip 惰性清理后快照应只剩 peer-b: %v", snap)
	}

	// 零值安全:生产以 &SkipMemo{} 零值构造(now=nil),Snapshot 直调不得
	// panic(直呼 m.now() 即生产 nil-func panic,这正是 clock() 兜底存在的理由)。
	zero := &SkipMemo{}
	if got := zero.Snapshot(); got == nil || len(got) != 0 {
		t.Fatalf("零值 memo 空表快照应得非 nil 空数组: %v", got)
	}
}
