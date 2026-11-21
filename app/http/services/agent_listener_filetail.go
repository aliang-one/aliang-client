package services

// agent_listener_filetail.go file_tail 事件源 watcher(specs/005-event-listener
// §3/§2/FR-5,plan Task 9):监听本机文件追加,按行匹配命中即经远端 WS 上行
// listener.event(§1.5 契约原样,无信封包装)。
//
// 结构三层:
//   - 管理器(listenerWatchers 注册表 + Start/Stop + Task 8 钩子接线):
//     镜像行 → watcher 生命周期;参数(path/match)或 status 变更 → 重启/停止。
//   - 单 watcher goroutine:fsnotify 监听目标文件所在目录(create/write 事件)
//     + 2s 轮询兜底(fsnotify 在部分平台丢事件,轮询是正确性网);断点续读
//     offset 落本地状态文件(原子写,1s 去抖);截断/轮转重置 offset;文件
//     消失 → 本地置 error(source_gone)停 watcher(不上行)。
//   - 上行/暂存:listener.event 帧经 listenerEventUplink 缝发出;失败入
//     本地 outbox(listener-outbox.jsonl,上限 50,溢出丢最旧+记摘要行),
//     重连注册成功后按序重放(M2-B)。
//
// home 缝:复用 listenerMirrorHome(默认 agentHome,测试 swap 同一注入点即可
// 同时搬走镜像/tails/outbox 三处落盘)。
//
// 接线选择:钩子与上行缝都在 init() 赋值而非声明处初始化——Go 初始化依赖
// 分析会穿过包级 var 声明处的函数字面量,一旦默认实现引用链上的任何包级
// func var 回指本文件即成 initialization cycle(agentChannelAlertWriter 实测
// 前例,agent_channel_health.go:171 一带)。当前链路未必已构成环,init()
// 赋值是按该前例做的防御性定型(依赖分析不穿 init 体),后继往这些缝上加
// 引用无须再逐条验环。

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// 缝与可调参数(测试 swap/shrink;测试串行跑,禁 t.Parallel)
// ---------------------------------------------------------------------------

// listenerEventUplink 是 listener.event 帧的实际发送通道;声明为 var 便于测试
// 替换(agentChannelAlertWriter 同款纪律)。默认实现(经 init 接线,防
// initialization cycle)取共享 AgentService 的远端 writer;无连接/发送失败
// 由调用方(watcher emit 路径)落 outbox,缝本身只报错。
var listenerEventUplink func(frame map[string]interface{}) error

// listenerOutboxReplayHook 重连补发钩子:agent.registered 到达(注册成功,
// 门已开)时由 agent_remote_ws.go 调用,默认 no-op,本文件 init() 接线成
// 按序重放 outbox(safeGo 离线化,不堵读循环)。
var listenerOutboxReplayHook = func() {}

// listenerLocalDeviceID 本机 deviceID 取值缝(spawn 门用)。init() 接线取
// 共享 AgentService 的 CurrentDeviceID(mesh 装配同款只读访问器);测试 swap
// 注入固定 id。返回空(服务未起/身份未铸)时 spawn 门恒关——fail-closed:
// 宁可不起 watcher,不可把别设备的行当本机行 watch(跨设备误归因)。
var listenerLocalDeviceID func() string

var (
	// listenerTailPollInterval 轮询兜底周期。fsnotify 在部分平台(尤其网络
	// 文件系统/编辑器原子重写)会丢事件,轮询是正确性网而非优化;测试 shrink
	// 加速收敛(atomicDuration:goroutine 在跑时改值须无竞态)。
	listenerTailPollInterval = newDurationAtom(2 * time.Second)
	// listenerTailOffsetFlushInterval offset 落盘去抖窗口:每个 watcher 每秒
	// 至多写一次状态文件(优雅停止时兜底强刷一次)。
	listenerTailOffsetFlushInterval = newDurationAtom(1 * time.Second)
	// listenerTailStopGrace 停止等待上限:run 循环卡在慢读时 stop 不得无限等。
	listenerTailStopGrace = newDurationAtom(5 * time.Second)
	// listenerTailMissThreshold 文件消失判定阈值:连续 N 次探测不存在才算
	// source_gone——轮转(rename→create)窗口内的瞬时缺文件不得误判。
	listenerTailMissThreshold = 2
)

func init() {
	listenerEventUplink = func(frame map[string]interface{}) error {
		svc := GetSharedAgentService()
		if svc == nil {
			return fmt.Errorf("no shared agent service")
		}
		writer := svc.currentRemoteWriter()
		if writer == nil {
			return fmt.Errorf("agent is not connected")
		}
		return writer(frame)
	}
	// Task 8 生命周期钩子接线(选择 init() 显式接线而非声明处初始化:自动生效、
	// 无 boot 依赖,Task 10 只需调 StartEventListenerWatchers;测试 swap 后还原
	// 即回到本接线)。语义见 syncListenerWatcherForMirror / stopListenerWatcher。
	listenerUpsertHook = syncListenerWatcherForMirror
	listenerRemoveHook = func(id string) { stopListenerWatcher(id) }
	listenerLocalDeviceID = func() string {
		svc := GetSharedAgentService()
		if svc == nil {
			return ""
		}
		return svc.CurrentDeviceID()
	}
	listenerOutboxReplayHook = func() {
		safeGo("listener_outbox_replay", drainListenerOutbox)
	}
}

// ---------------------------------------------------------------------------
// 管理器:注册表 + 生命周期
// ---------------------------------------------------------------------------

var (
	// listenerWatcherMu 守护 listenerWatchers 注册表(增删查全走它)。
	listenerWatcherMu sync.Mutex
	// listenerWatchers listener_id → 在跑 watcher(本机唯一属主:同 id 至多一个)。
	listenerWatchers = map[string]*fileTailWatcher{}
)

// StartEventListenerWatchers 遍历本地镜像,为每个有效且 active 的
// file_tail 行启动 watcher(幂等:已在跑的 id 跳过;非 active/无效行跳过,
// 在跑的停止)。boot(Task 10)与测试的统一入口。
func StartEventListenerWatchers() {
	for _, m := range listListenerMirrors() {
		syncListenerWatcherForMirror(m)
	}
}

// StopEventListenerWatchers 停止全部在跑 watcher 并清空注册表(幂等)。
// 测试助手 + Task 10 boot 复用(全量拉取前清场)。
func StopEventListenerWatchers() {
	listenerWatcherMu.Lock()
	ids := make([]string, 0, len(listenerWatchers))
	for id := range listenerWatchers {
		ids = append(ids, id)
	}
	listenerWatcherMu.Unlock()
	for _, id := range ids {
		stopListenerWatcher(id)
	}
}

// syncListenerWatcherForMirror 单行镜像 → watcher 生命周期(Task 8
// listenerUpsertHook 的真身)。规则:
//   - 非 file_tail 行(source_type 变更)→ 停在跑 watcher(幂等);
//   - 无效行(空 id/updated_at/path/match)→ Warn 跳过;在跑旧 watcher 一并停
//     (镜像已换新参数,旧 watcher 依据已失效);
//   - status 非 active(paused/fired/expired/error)→ 不起;在跑则停
//     (server 侧状态迁移经 upsert 到达);
//   - active:未在跑 → 启;在跑且 path/match 与在册启动参数一致 → no-op(幂等,
//     抵御平局重放的多余重启);不一致(参数变更)→ 停 + 按新参数重启。
//
// 「变更」判定对 watcher 管理器自己在册的启动参数做 diff(Task 8 评审注记①:
// 钩子只收新镜像,不反推旧镜像)。
func syncListenerWatcherForMirror(m ListenerMirror) {
	if m.SourceType != "file_tail" {
		stopListenerWatcher(m.ID)
		return
	}
	// 跨设备行不入本地 watch;镜像仍保留(供本机 UI 展示)。file_tail 的
	// 监听目标在本机文件系统,watch 别设备注册的行 = 把本机文件内容按
	// 他人 listener_id 上行(路径恰好存在时即跨设备误归因)。boot 全量拉取
	// 是账号级的(他机行是常态),故 Debug 不 Warn;在跑 watcher 一并停
	// (设备重指派/本机身份变化的收敛)。本机身份不可得时空 id 恒不等 =
	// fail-closed(见 listenerLocalDeviceID 缝注)。
	if localID := listenerLocalDeviceID(); m.SourceDeviceID != localID {
		logger.Debug(fmt.Sprintf("[AGENT-LISTENER] file_tail 行属设备 %q(本机 %q),不入本地 watch id=%s",
			m.SourceDeviceID, localID, m.ID))
		stopListenerWatcher(m.ID)
		return
	}
	path, match, maxBytes, ok := parseFileTailParams(m)
	if !ok {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail 行无效,跳过 watcher(id=%s updated_at=%q 需非空 id/updated_at/path/match)", m.ID, m.UpdatedAt))
		stopListenerWatcher(m.ID)
		return
	}
	if m.Status != "active" {
		stopListenerWatcher(m.ID)
		return
	}
	listenerWatcherMu.Lock()
	cur, running := listenerWatchers[m.ID]
	// 参数变更判定含 readCap(max_bytes 归一值):行限变化也必须重启 watcher
	// (plan 的「参数变更即重启」覆盖全部启动参数,不只 path/match)。
	sameParams := running && cur.path == path && cur.match == match &&
		cur.readCap == clampListenerTailMaxBytes(maxBytes)
	listenerWatcherMu.Unlock()
	if sameParams {
		return
	}
	if running {
		stopListenerWatcher(m.ID)
	}
	startListenerWatcher(m.ID, path, match, maxBytes)
}

// parseFileTailParams 校验并抽取 file_tail 行的启动参数。有效 = id/updated_at
// 非空 + source_params 内 path/match 非空(其余键容忍,前向兼容)。max_bytes
// 可缺省(0 = 默认行限,见 clampListenerTailMaxBytes)。
func parseFileTailParams(m ListenerMirror) (path, match string, maxBytes int64, ok bool) {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.UpdatedAt) == "" {
		return "", "", 0, false
	}
	var p struct {
		Path     string `json:"path"`
		Match    string `json:"match"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if len(m.SourceParams) == 0 || json.Unmarshal(m.SourceParams, &p) != nil {
		return "", "", 0, false
	}
	p.Path = strings.TrimSpace(p.Path)
	p.Match = strings.TrimSpace(p.Match)
	if p.Path == "" || p.Match == "" {
		return "", "", 0, false
	}
	return p.Path, p.Match, p.MaxBytes, true
}

// 行限(read-span cap)策略:单行长度上限 = max_bytes(缺省 1MiB),下限 1KiB
// (防退化配置把所有行判超限)、上限 8MiB(防配置本身撑爆读缓冲——与
// agentRemoteMaxFrameBytes 同量级)。超限行整段丢弃(跳到下一换行或 EOF,
// offset 越过),不发事件——无换行巨文件读侧内存有界、watcher 不被卡死;
// 其后正常追加行照常匹配。
const (
	listenerTailDefaultMaxBytes = 1 << 20
	listenerTailMaxBytesFloor   = 1 << 10
	listenerTailMaxBytesCeil    = 8 << 20
)

// clampListenerTailMaxBytes 行限归一(见上方策略注)。
func clampListenerTailMaxBytes(v int64) int {
	if v <= 0 {
		return listenerTailDefaultMaxBytes
	}
	if v < listenerTailMaxBytesFloor {
		return listenerTailMaxBytesFloor
	}
	if v > listenerTailMaxBytesCeil {
		return listenerTailMaxBytesCeil
	}
	return int(v)
}

// startListenerWatcher 启动单 watcher(已在跑同 id → no-op,幂等入口)。
func startListenerWatcher(id, path, match string, maxBytes int64) {
	listenerWatcherMu.Lock()
	if _, exists := listenerWatchers[id]; exists {
		listenerWatcherMu.Unlock()
		return
	}
	w := newFileTailWatcher(id, path, match, maxBytes)
	listenerWatchers[id] = w
	listenerWatcherMu.Unlock()
	// safeGo 自带 recover 护栏(仓规):watcher panic 只记日志,不杀进程。
	safeGo("listener_file_tail_"+id, w.run)
	logger.Info(fmt.Sprintf("[AGENT-LISTENER] file_tail watcher started id=%s path=%s", id, path))
}

// stopListenerWatcher 停止并摘除 id 对应 watcher(幂等:不在册 no-op)。
// 停止 = 摘注册表 + 关 stopCh + 有界等待 run 退出(测试与断言依赖 goroutine
// 真正退出,而非仅标记)。
// grace 超时的残余风险:run 卡死时孤儿 goroutine 仍活着,同 id 新 watcher
// 可先入册——短暂双读者最多产生重复帧,server 侧 dedupe/cooldown 吸收。
func stopListenerWatcher(id string) {
	listenerWatcherMu.Lock()
	w, ok := listenerWatchers[id]
	if ok {
		delete(listenerWatchers, id)
	}
	listenerWatcherMu.Unlock()
	if !ok {
		return
	}
	w.requestStop()
	select {
	case <-w.doneCh:
	case <-time.After(listenerTailStopGrace.Load()):
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail watcher stop_grace_expired id=%s", id))
	}
	logger.Info(fmt.Sprintf("[AGENT-LISTENER] file_tail watcher stopped id=%s", id))
}

// ---------------------------------------------------------------------------
// 单 watcher
// ---------------------------------------------------------------------------

// fileTailWatcher 一个 file_tail 监听行对应一个实例。单 goroutine 属主:除
// 注册表交互(requestStop/doneCh)外的字段(offset/lastFp/sawFile 等)只被
// run goroutine 读写,无须加锁。
type fileTailWatcher struct {
	id         string
	path       string // 监听目标文件(绝对或相对,按镜像原样)
	match      string // 原始 match 参数(注册表 diff 用)
	re         *regexp.Regexp
	offsetPath string // offset 状态文件路径
	readCap    int    // 单行长度上限(归一后,见 clampListenerTailMaxBytes)

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	// run goroutine 私有:
	offset          int64
	sawFile         bool
	missCount       int
	lastFp          string // 上次发出的 fingerprint(洪峰合并:相同即跳过)
	lastOffsetWrite time.Time
	selfStopped     bool
}

func newFileTailWatcher(id, path, match string, maxBytes int64) *fileTailWatcher {
	offsetPath := listenerTailOffsetPath(path)
	w := &fileTailWatcher{
		id:         id,
		path:       path,
		match:      match,
		offsetPath: offsetPath,
		readCap:    clampListenerTailMaxBytes(maxBytes),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
		offset:     readListenerTailOffset(offsetPath),
	}
	// offset 状态文件存在 = 上个生命周期见过该文件。据此初始化 sawFile:
	// 文件在 agent 停机窗口内被删时,重启后的首轮 stat 即开始缺失计数
	// (source_gone),而不是误入「等创建」模式。
	if _, err := os.Stat(offsetPath); err == nil {
		w.sawFile = true
	}
	// match 语义:能编译即正则,编译失败 Warn 后按关键字包含(仓内定案)。
	if re, err := regexp.Compile(match); err == nil {
		w.re = re
	} else {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail match 编译失败按关键字处理 id=%s match=%q err=%v", id, match, err))
	}
	return w
}

// requestStop 幂等关停(close-once):外部 stop 与 watcher 自停共用。
func (w *fileTailWatcher) requestStop() { w.stopOnce.Do(func() { close(w.stopCh) }) }

// run watcher 主循环:fsnotify 目录监听 + 轮询兜底双通道驱动 pollOnce。
// 退出路径:stopCh(外部停/自停)/ fsnotify watcher 关闭(退化为纯轮询)。
func (w *fileTailWatcher) run() {
	defer close(w.doneCh)
	// 停止兜底强刷一次 offset(去抖窗口内的变更不丢进度)。
	defer w.flushOffsetNow()

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail fsnotify 不可用(%v),退化为纯轮询 id=%s", err, w.id))
	} else {
		defer fw.Close()
		// 监听目录而非文件:文件尚不存在(等创建)与轮转(rename+create)都
		// 只有目录监听能看见。目录缺失(父目录还没建)不致命——轮询兜底。
		if err := fw.Add(filepath.Dir(w.path)); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail 目录监听失败(%v),轮询兜底 id=%s dir=%s", err, w.id, filepath.Dir(w.path)))
		}
	}

	poll := time.NewTicker(listenerTailPollInterval.Load())
	defer poll.Stop()

	// 起跑即追读一次(文件存在则从 offset 续读;不存在则等创建)。
	w.pollOnce()
	for {
		if w.selfStopped {
			return
		}
		if fw == nil {
			select {
			case <-w.stopCh:
				return
			case <-poll.C:
				w.pollOnce()
			}
			continue
		}
		select {
		case <-w.stopCh:
			return
		case ev, ok := <-fw.Events:
			if !ok {
				// Events 通道关闭(fsnotify 内部错误后 Close):继续纯轮询。
				fw = nil
				continue
			}
			if filepath.Base(ev.Name) != filepath.Base(w.path) {
				continue
			}
			w.pollOnce()
		case err, ok := <-fw.Errors:
			if !ok {
				fw = nil
				continue
			}
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail fsnotify error id=%s err=%v", w.id, err))
		case <-poll.C:
			w.pollOnce()
		}
	}
}

// pollOnce 单次对账:stat → 缺失计数(source_gone)/截断重置 → 从 offset
// 追读到 EOF(只消费完整行;残尾留在盘上等下轮)。单 goroutine 调用。
func (w *fileTailWatcher) pollOnce() {
	info, err := os.Stat(w.path)
	if err != nil {
		if !os.IsNotExist(err) {
			return // 瞬时 stat 错误(权限等):下轮再看,不判死
		}
		if w.sawFile {
			w.missCount++
			if w.missCount >= listenerTailMissThreshold {
				w.markSourceGone()
			}
		}
		return // 从未见过文件:等 create,不判死
	}
	w.sawFile = true
	w.missCount = 0
	if info.Size() < w.offset {
		// 截断/轮转:offset 越界 → 归零重读,不丢轮转后首条匹配(spec FR)。
		// 等长改写(size==offset)天然不可见——offset-tail 的固有限界
		// (agentcore journal 游标同款),内容级去重交给 server fingerprint。
		w.offset = 0
	}
	if info.Size() == w.offset {
		return
	}
	f, err := os.Open(w.path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
		return
	}
	// 缓冲=行限:ReadSlice 超限行报 ErrBufferFull,读侧内存恒 ≤ 行限。
	r := bufio.NewReaderSize(f, w.readCap)
	oversizedSkipped := int64(0)
	for {
		slice, rerr := r.ReadSlice('\n')
		if rerr == bufio.ErrBufferFull {
			// 行长超限:丢弃残行至下一换行(或 EOF),offset 越过整段,
			// 不发事件(策略见 clampListenerTailMaxBytes 上方注)。
			oversizedSkipped += w.skipOversizedLine(r, int64(len(slice)))
			continue
		}
		// 只消费完整行(以 \n 结尾);残尾不推进 offset——磁盘即缓冲。
		if len(slice) > 0 && slice[len(slice)-1] == '\n' {
			w.handleLine(string(slice))
			w.offset += int64(len(slice))
		}
		if rerr != nil {
			break // io.EOF(正常)或读错误(下轮从 offset 重试)
		}
	}
	if oversizedSkipped > 0 {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail 超限行丢弃(整段跳过不发事件) id=%s bytes=%d cap=%d offset=%d",
			w.id, oversizedSkipped, w.readCap, w.offset))
	}
	w.flushOffsetMaybe()
}

// skipOversizedLine 丢弃超限行残段:吞到下一换行(含)或 EOF,返回吞掉的字节
// 数并推进 offset。EOF 仍无换行时 offset 落在 EOF——该无界残行被整体放弃,
// 后续追加照常从新位置续读(换行到达时其前内容已弃,最多留一个空行工件)。
// 调用方须在 run goroutine 内(pollOnce 专用)。
func (w *fileTailWatcher) skipOversizedLine(r *bufio.Reader, already int64) int64 {
	consumed := already
	for {
		s, err := r.ReadSlice('\n')
		consumed += int64(len(s))
		if err == bufio.ErrBufferFull {
			continue
		}
		break // 找到换行(err==nil)或 EOF:整段跳过完结
	}
	w.offset += consumed
	return consumed
}

// handleLine 单完整行匹配与上行。行内容先剥尾部换行(\n 与 \r\n)。
func (w *fileTailWatcher) handleLine(rawLine string) {
	content := strings.TrimSuffix(rawLine, "\n")
	content = strings.TrimSuffix(content, "\r")
	if w.re != nil {
		if !w.re.MatchString(content) {
			return
		}
	} else if !strings.Contains(content, w.match) {
		return
	}
	// 洪峰合并(本机防洪,非冷却——冷却是 server 侧职责):与上次发出的
	// fingerprint 相同即跳过。同一行内容重复刷屏只上行一次;不同内容照发,
	// 由 server 侧 status/cooldown/dedupe 裁决(spec §2)。
	sum := sha256.Sum256([]byte(content))
	fp := hex.EncodeToString(sum[:]) // hex 小写,契约即小写
	if fp == w.lastFp {
		return
	}
	w.lastFp = fp
	w.emitEvent(content, fp)
}

// emitEvent 构造 ListenerEvent 帧(spec §1.5)并发送:上行失败 → 落 outbox。
func (w *fileTailWatcher) emitEvent(content, fp string) {
	clean := stripListenerControlChars(content)
	frame := map[string]interface{}{
		"type":        models.AgentEventListenerEvent,
		"listener_id": w.id,
		"fingerprint": fp,
		"fired_at":    time.Now().UTC().Format(time.RFC3339),
		"summary":     truncateListenerRunes(clean, 200),
		"source_type": "file_tail",
		"detail": map[string]interface{}{
			"path":            w.path,
			"matched_excerpt": truncateListenerRunes(clean, 512),
		},
	}
	if err := listenerEventUplink(frame); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail 上行失败入暂存 id=%s err=%v", w.id, err))
		enqueueListenerOutbox(frame)
		return
	}
	// 上行成功且此前有暂存积压 → 顺手补发(通道已证明可用)。
	if listenerOutboxHasFrames() {
		safeGo("listener_outbox_drain_after_send", drainListenerOutbox)
	}
}

// markSourceGone 监听文件消失:摘注册表 + 停 watcher + 本地镜像置
// error(source_gone),不上行(spec §1「文件被删置 error,AI/用户可复活」)。
// UpdatedAt 平局技巧:保持原值 upsert,LWW 平局新胜让本地注记落地;若 server
// 更新帧已先至(更大 updated_at),本次注记被拒收——server 权威优先,良性。
func (w *fileTailWatcher) markSourceGone() {
	w.selfStopped = true
	listenerWatcherMu.Lock()
	if cur, ok := listenerWatchers[w.id]; ok && cur == w {
		delete(listenerWatchers, w.id)
	}
	listenerWatcherMu.Unlock()
	w.requestStop()
	logger.Warn(fmt.Sprintf("[AGENT-LISTENER] file_tail 源文件消失置 error id=%s path=%s", w.id, w.path))
	mutateListenerMirrorLocal(w.id, func(m *ListenerMirror) {
		m.Status = "error"
		m.ErrorNote = "source_gone"
	})
}

// ---------------------------------------------------------------------------
// offset 状态文件
// ---------------------------------------------------------------------------

// listenerTailOffsetPath offset 状态文件路径:<home>/.aliang/listener-tails/
// <safeName(path)>.offset。home 缝复用 listenerMirrorHome(见文件头注)。
func listenerTailOffsetPath(path string) string {
	home := listenerMirrorHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aliang", "listener-tails", listenerTailSafeName(path)+".offset")
}

// listenerTailSafeName 把文件路径变成安全文件名:只保留 [A-Za-z0-9._-],其余
// 转 `_`,超长截断 120(照 agentCoreJournalFileName 白名单先例,
// agent_ai_agentcore_journal.go:152)。碰撞取舍:不同路径(或同路径两行)
// 归一后可能共享同一 offset 状态文件——两 watcher 各自推进 offset 造成跳读/
// 重读,重复帧由 server dedupe 吸收,不损正确性(agentcore 同款已知取舍)。
func listenerTailSafeName(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// readListenerTailOffset 读上次进度;缺文件/坏内容一律 0(从头读,安全向)。
func readListenerTailOffset(path string) int64 {
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var off int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &off); err != nil || off < 0 {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] offset 状态坏内容归零 path=%s", path))
		return 0
	}
	return off
}

// flushOffsetMaybe 去抖落盘:距上次写不足窗口则跳过(下轮补)。
func (w *fileTailWatcher) flushOffsetMaybe() {
	now := time.Now()
	if now.Sub(w.lastOffsetWrite) < listenerTailOffsetFlushInterval.Load() {
		return
	}
	w.lastOffsetWrite = now
	w.flushOffsetNow()
}

// flushOffsetNow 强制落盘(原子写;失败仅 Warn——offset 丢失只导致重读重复,
// fingerprint/server dedupe 兜底,不损正确性)。
func (w *fileTailWatcher) flushOffsetNow() {
	if w.offsetPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.offsetPath), 0o755); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] offset mkdir 失败 path=%s err=%v", w.offsetPath, err))
		return
	}
	if err := writePeerMCPFileAtomic(w.offsetPath, []byte(fmt.Sprintf("%d\n", w.offset))); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] offset 落盘失败 path=%s err=%v", w.offsetPath, err))
	}
}

// ---------------------------------------------------------------------------
// 文本工具
// ---------------------------------------------------------------------------

// stripListenerControlChars 剥离控制字符([\x00-\x1f\x7f])——外部日志内容
// 入帧前先剥离(spec §1.5 边界情况:入模板前完成剥离/截断)。
func stripListenerControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// truncateListenerRunes 按 rune 截断(避免截断 UTF-8 序列)。
func truncateListenerRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// mutateListenerMirrorLocal 本地注记镜像行(status/error_note/reject_count 等)
// 而不伪造 updated_at:取现值原样保留 UpdatedAt 回写 upsert,LWW 平局
// (相等)新胜 → 注记落地。若 server 更新帧已先至,本次旧 updated_at 被拒收
// ——server 是权威,良性丢注记。行不存在(已删)→ no-op 返回 false。
func mutateListenerMirrorLocal(id string, mutate func(*ListenerMirror)) bool {
	cur, ok := getListenerMirror(id)
	if !ok {
		return false
	}
	m := cur
	mutate(&m)
	if err := upsertListenerMirror(m); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] 本地注记镜像失败 id=%s err=%v", id, err))
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// 断联暂存 outbox(M2-B)
// ---------------------------------------------------------------------------

// outbox 文件格式(exact contract,勿改口径):
//   - 路径:<home>/.aliang/listener-outbox.jsonl,一行一个 JSON 对象;
//   - 帧行:listener.event 帧对象原样(含 "type":"listener.event"),行序即
//     重放序;
//   - 摘要行:恰一行 {"dropped":N},恒为尾部——溢出丢最旧时把 N 累加成
//     运行总量(多次溢出合并,不逐次追加行)。摘要行不参与重放、不计入
//     50 上限(上限只数帧行);重写时附在帧行之后。
//   - 投递语义 at-least-once:上行成功与文件重写之间崩溃会让已发帧留在
//     队列,下次重放重发一次——server 侧 status/cooldown/dedupe 吸收
//     (spec FR-5 幂等口径),不追求 exactly-once。
//
// 容错:坏行(非法 JSON)在读取时丢弃并 Warn——它永远无法重放,留着会
// 永久堵住其后帧的重放(队列按序推进,首败即停)。丢帧本身已由摘要行口径
// 覆盖。
const listenerOutboxCap = 50

var (
	// listenerOutboxMu 串行化 outbox 全部读写路径(入队/重放/探空)。
	listenerOutboxMu sync.Mutex
	// listenerOutboxPending 探空标志:有积压时 emit 成功后才值得起补发,
	// 避免每条命中都空转一个 goroutine。
	listenerOutboxPending bool
)

// listenerOutboxPath 暂存文件路径;home 缝复用 listenerMirrorHome(文件头注)。
func listenerOutboxPath() (string, error) {
	home := listenerMirrorHome()
	if home == "" {
		return "", fmt.Errorf("agent home unavailable")
	}
	return filepath.Join(home, ".aliang", "listener-outbox.jsonl"), nil
}

// listenerOutboxHasFrames 是否有暂存积压(emit 补发触发用,轻量)。
func listenerOutboxHasFrames() bool {
	listenerOutboxMu.Lock()
	defer listenerOutboxMu.Unlock()
	return listenerOutboxPending
}

// enqueueListenerOutbox 上行失败入队:读全量 → 追帧 → 溢出丢最旧 + 摘要行 →
// 原子重写。入队只在断联期发生且队列 ≤50 行,整文件重写代价可忽略(免追写
// 与重放改写两套路径的形态漂移)。
func enqueueListenerOutbox(frame map[string]interface{}) {
	listenerOutboxMu.Lock()
	defer listenerOutboxMu.Unlock()
	frames, markers := readListenerOutboxLocked()
	frames = append(frames, frame)
	dropped := 0
	for len(frames) > listenerOutboxCap {
		frames = frames[1:]
		dropped++
	}
	if dropped > 0 {
		// 摘要行合并为恰一行累计总量(契约注):既有标记(含旧格式多行)
		// 一并归并,文件恒为「帧…+ 一行 {"dropped":Σ}」。
		total := dropped
		for _, mk := range markers {
			if d, ok := mk["dropped"].(float64); ok {
				total += int(d)
			}
		}
		markers = []map[string]interface{}{{"dropped": total}}
	}
	if err := writeListenerOutboxLocked(frames, markers); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] outbox 落盘失败(帧丢失): %v", err))
		return
	}
	listenerOutboxPending = true
}

// drainListenerOutbox 按序重放:逐帧 re-uplink,成功即移除;首败即停(余量
// 原样保留等下次)。空队列 no-op。由重连注册成功钩子与上行成功后的补发
// 两条路径触发,并发由 listenerOutboxMu 串行化(safeGo 派发,不堵读循环)。
func drainListenerOutbox() {
	listenerOutboxMu.Lock()
	defer listenerOutboxMu.Unlock()
	frames, markers := readListenerOutboxLocked()
	if len(frames) == 0 {
		listenerOutboxPending = false
		return
	}
	kept := make([]map[string]interface{}, 0, len(frames))
	for i, frame := range frames {
		if err := listenerEventUplink(frame); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] outbox 重放首败即停(第 %d/%d 帧): %v", i+1, len(frames), err))
			kept = append(kept, frames[i:]...)
			break
		}
	}
	if err := writeListenerOutboxLocked(kept, markers); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] outbox 重放后落盘失败: %v", err))
		return
	}
	listenerOutboxPending = len(kept) > 0
	if drained := len(frames) - len(kept); drained > 0 {
		logger.Info(fmt.Sprintf("[AGENT-LISTENER] outbox 重放完成 drained=%d remaining=%d", drained, len(kept)))
	}
}

// readListenerOutboxLocked 读全量并按行分型(帧/摘要);缺文件返回空。
// 调用方须已持 listenerOutboxMu。
func readListenerOutboxLocked() (frames []map[string]interface{}, markers []map[string]interface{}) {
	path, err := listenerOutboxPath()
	if err != nil {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // 缺文件=空队列
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] outbox 坏行丢弃(不堵重放): %q", line))
			continue
		}
		if _, isFrame := obj["type"]; isFrame {
			frames = append(frames, obj)
		} else {
			markers = append(markers, obj)
		}
	}
	return frames, markers
}

// writeListenerOutboxLocked 全量重写(帧行在前、摘要行附尾;原子写)。
// 调用方须已持 listenerOutboxMu。
func writeListenerOutboxLocked(frames, markers []map[string]interface{}) error {
	path, err := listenerOutboxPath()
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, frame := range frames {
		line, err := json.Marshal(frame)
		if err != nil {
			return fmt.Errorf("encode frame: %w", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	for _, marker := range markers {
		line, err := json.Marshal(marker)
		if err != nil {
			continue // 摘要行编码失败只丢墓碑,不阻断帧写入
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove empty outbox %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return writePeerMCPFileAtomic(path, []byte(b.String()))
}

// ---------------------------------------------------------------------------
// listener.event.reject 消费(specs/005 §2/FR-6)
// ---------------------------------------------------------------------------

// handleListenerEventRejectFrame 消费下行 listener.event.reject
// {listener_id, permanent, note, counted?}(note 词汇表 {deleted, expired,
// paused, fired, device_unregistered} 为冻结契约,勘误 2026-11-21)。分档:
//   - fired/expired:watcher 终态(server 判定即已推 upsert,此处乱序/漏推
//     兜底)→ 停 watcher + 本地镜像置同值,不计数;
//   - deleted/paused:停 watcher,不动镜像不计数(remove/paused-upsert 帧自会
//     收敛镜像;deleted 时行多半已删,无行可注记);
//   - device_unregistered 且 counted=true:本地镜像 RejectCount +1(server
//     已计数的对齐同步),累计 ≥5 → 本地置 error(rejected_5x)+ 停 watcher;
//     counted≠true 一律不计(FR-6:WS 断联/发送失败/暂存中的事件不计数——
//     那些根本到不了 agent,此帧只可能是 server 真拒绝)。
//
// 帧处理内联于读循环(与 Task 8 两帧同款),自带 recover 护栏:畸形帧只记
// 日志,绝不撕断读循环。
func handleListenerEventRejectFrame(msg map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] reject frame panic recovered: %v", r))
		}
	}()
	id := strings.TrimSpace(remoteString(msg, "listener_id"))
	if id == "" {
		logger.Warn("[AGENT-LISTENER] reject 帧缺 listener_id,丢弃")
		return
	}
	note := strings.TrimSpace(remoteString(msg, "note"))
	permanent := remoteBool(msg, "permanent", false)
	switch note {
	case "fired", "expired":
		stopListenerWatcher(id)
		mutateListenerMirrorLocal(id, func(m *ListenerMirror) {
			m.Status = note
		})
		logger.Info(fmt.Sprintf("[AGENT-LISTENER] reject(note=%s) watcher 终态停止 id=%s", note, id))
	case "deleted", "paused":
		stopListenerWatcher(id)
		logger.Info(fmt.Sprintf("[AGENT-LISTENER] reject(note=%s) watcher 停止(不计数) id=%s", note, id))
	case "device_unregistered":
		if !remoteBool(msg, "counted", false) {
			logger.Info(fmt.Sprintf("[AGENT-LISTENER] reject(note=device_unregistered counted=false) 不计数 id=%s", id))
			return
		}
		reachedLimit := false
		mutateListenerMirrorLocal(id, func(m *ListenerMirror) {
			m.RejectCount++
			reachedLimit = m.RejectCount >= listenerRejectLimit
			if reachedLimit {
				m.Status = "error"
				m.ErrorNote = "rejected_5x"
			}
		})
		if reachedLimit {
			stopListenerWatcher(id)
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] reject 计数达 %d 上限,本地置 error 停 watcher id=%s", listenerRejectLimit, id))
			return
		}
		logger.Info(fmt.Sprintf("[AGENT-LISTENER] reject 计数 +1 id=%s", id))
	default:
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] reject 未知 note=%q(契约冻结词汇表外),忽略 id=%s permanent=%v", note, id, permanent))
	}
}

// listenerRejectLimit 永久拒绝本地置 error 的累计上限(spec FR-6:累计 5 次)。
const listenerRejectLimit = 5
