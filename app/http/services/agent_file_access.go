package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/tccguard"
)

// 文件访问授权（一次性预先授权）。
//
// 为什么需要：TCC 授权是「第一次访问才弹窗」的。手机驱动的 run 经常发生在没人
// 守在 Mac 前的时候——弹窗无人应答，请求进程被 tcd 同步阻塞，工具调用直接卡死
// （2026-11-01 实测一条命令卡了 1h58m）。这个服务让用户在**自己就在电脑前**时，
// 主动把几类目录各弹一次窗、一次性拿全授权，之后远程调用就不会再卡在这里。
//
// 三条设计约束：
//
//  1. 必须跑在 **user-agent 进程**：tccguard 的 grant 以 executableStamp
//     (os.Executable+mtime+size) 为键，且 stamp 不匹配会整份丢弃；core 是 root
//     LaunchDaemon，在那儿探测写下的 grant 与真实执行工具的进程对不上，用户会
//     看到「已授权」但下次照样卡死。所以 handler 首行必须 proxyIfNeeded。
//  2. **绝不预写 grant**（见 tccguard/request.go 文件头）。
//  3. 探测是**同步阻塞**的（要等人点系统弹窗），因此绝不能在 HTTP handler 里
//     同步跑完 8 类：POST 只启动后台 sweep 并立即返回快照，前端轮询 GET。

// AgentFileAccessCategory 是单个类别的状态。
type AgentFileAccessCategory struct {
	Key    string `json:"key"`
	Root   string `json:"root"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// AgentFileAccessSnapshot 是「文件访问授权」的完整状态快照。
type AgentFileAccessSnapshot struct {
	Supported bool `json:"supported"`
	Attended  bool `json:"attended"`
	// State: idle | running | done
	State string `json:"state"`
	// StartedAt/FinishedAt 是 RFC3339；零值省略。
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	// ProbeTimeoutSec 是每类的等待上限，供前端文案使用。
	ProbeTimeoutSec int `json:"probeTimeoutSec"`
	// LastErrorCode: "" | unsupported | unattended
	LastErrorCode string                    `json:"lastErrorCode,omitempty"`
	Categories    []AgentFileAccessCategory `json:"categories"`
}

// agentFileAccessService 是进程内单例状态机。
//
// 不做持久化：探测结果本身已经持久化在 tccguard 的 grant 文件里，进程重启后
// 重新 GET 一次就能从 grant 缓存正确回显「已授权」。
type agentFileAccessService struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	lastErr  string
	cats     []AgentFileAccessCategory
	// keys 是本次 sweep 的类别顺序快照。sweep 迭代它而不是重新调
	// tccguard.Categories()：后者每次返回新切片，若中途长度/顺序变了，
	// 用索引去写 s.cats 就会错位甚至越界。
	keys []string
	// gen 是 sweep 世代号。收尾只允许「自己那一代」复位 running：否则旧 sweep
	// 的 defer 会在新 sweep 刚启动时把状态清回 done（用户看到「已完成」但屏幕
	// 上还挂着待点的系统弹窗）。
	gen uint64
}

var (
	fileAccessOnce sync.Once
	fileAccessSvc  *agentFileAccessService
)

// GetSharedAgentFileAccessService 返回进程内单例。
func GetSharedAgentFileAccessService() *agentFileAccessService {
	fileAccessOnce.Do(func() { fileAccessSvc = &agentFileAccessService{} })
	return fileAccessSvc
}

// Snapshot 返回当前状态快照（无副作用，可高频轮询）。
func (s *agentFileAccessService) Snapshot() AgentFileAccessSnapshot {
	// Supported()/Attended() 必须在**锁外**求值：Attended 冷缓存时会去问
	// ioreg/pmset（各 3s 超时），持锁调用会把 sweep 的逐类进度更新卡住 6 秒。
	supported, attended := tccguard.Supported(), tccguard.Attended()
	granted := map[string]bool{}
	for _, key := range tccguard.GrantedCategories() {
		granted[key] = true
	}
	cats := tccguard.Categories()

	s.mu.Lock()
	defer s.mu.Unlock()

	snap := s.snapshotLocked(supported, attended)

	// 非 macOS 没有这些隐私目录，列出来只会是一排「未知」，误导用户。
	if !supported {
		return snap
	}

	if len(snap.Categories) > 0 {
		// 用**实时** grant 覆盖陈旧状态：用户可能刚在系统设置里手动打开了
		// 某个目录（被拒过的目录只能这样补救），此时「刷新状态」必须能看到。
		// 此前这里直接返回 s.cats 的快照，导致刷新按钮是死按钮。
		for i := range snap.Categories {
			if granted[snap.Categories[i].Key] && snap.Categories[i].State != string(tccguard.StateGranted) {
				snap.Categories[i].State = string(tccguard.StateGranted)
				snap.Categories[i].Detail = ""
			}
		}
		return snap
	}

	// 还没有跑过：用「静态类别清单 + 已缓存的 grant」拼一个初始视图。
	// 注意这里只读 grant 缓存，绝不 stat 受保护目录（stat 本身就会弹窗）。
	for _, cat := range cats {
		state := "unknown"
		if granted[cat.Key] {
			state = string(tccguard.StateGranted)
		}
		snap.Categories = append(snap.Categories, AgentFileAccessCategory{
			Key:   cat.Key,
			Root:  cat.Root,
			State: state,
		})
	}
	return snap
}

// Start 启动一次授权 sweep。已在跑时幂等返回当前快照（不重入）。
//
// 返回 false 表示没有启动（已在跑 / 不支持 / 无人值守），调用方据此决定文案。
func (s *agentFileAccessService) Start() (AgentFileAccessSnapshot, bool) {
	if !tccguard.Supported() {
		s.mu.Lock()
		s.lastErr = "unsupported"
		s.mu.Unlock()
		return s.Snapshot(), false
	}
	// 无人值守时绝不启动：那只会制造没人能点的系统弹窗。
	if !tccguard.Attended() {
		s.mu.Lock()
		s.lastErr = "unattended"
		s.mu.Unlock()
		return s.Snapshot(), false
	}

	s.mu.Lock()
	if s.running {
		snap := s.snapshotLocked(true, true)
		s.mu.Unlock()
		return snap, false
	}
	cats := tccguard.Categories()
	s.cats = make([]AgentFileAccessCategory, 0, len(cats))
	s.keys = make([]string, 0, len(cats))
	for _, cat := range cats {
		s.cats = append(s.cats, AgentFileAccessCategory{
			Key:   cat.Key,
			Root:  cat.Root,
			State: string(tccguard.StatePending),
		})
		s.keys = append(s.keys, cat.Key)
	}
	s.running = true
	s.started = time.Now()
	s.finished = time.Time{}
	s.lastErr = ""
	s.gen++
	gen := s.gen
	snap := s.snapshotLocked(true, true)
	s.mu.Unlock()

	go s.sweep(gen)
	return snap, true
}

// snapshotLocked 要求已持锁；supported/attended 由调用方在锁外求值后传入
// （见 Snapshot 注释：持锁做 I/O 会卡住 sweep）。
func (s *agentFileAccessService) snapshotLocked(supported, attended bool) AgentFileAccessSnapshot {
	snap := AgentFileAccessSnapshot{
		Supported:       supported,
		Attended:        attended,
		State:           "idle",
		ProbeTimeoutSec: int(agentFileAccessProbeWait / time.Second),
		LastErrorCode:   s.lastErr,
	}
	if s.running {
		snap.State = "running"
	} else if !s.finished.IsZero() {
		snap.State = "done"
	}
	if !s.started.IsZero() {
		snap.StartedAt = s.started.Format(time.RFC3339)
	}
	if !s.finished.IsZero() {
		snap.FinishedAt = s.finished.Format(time.RFC3339)
	}
	snap.Categories = append([]AgentFileAccessCategory(nil), s.cats...)
	return snap
}

// agentFileAccessProbeWait 是单类等待上限，与 tccguard 的 probeWait 对齐。
const agentFileAccessProbeWait = 25 * time.Second

// sweep 顺序推进各分类。
//
// 刻意**串行**：macOS 的授权弹窗是模态的，并发扇出会一次堆出 8 个弹窗，用户
// 根本点不过来。串行 + 逐类回写，用户点完一个再来下一个。
//
// 一旦某类卡在 pending（没人点），立刻把剩余类别标记为 skipped 并收工：继续
// 往下弹只会再制造 7 个无人应答的弹窗，那正是要避免的事。
func (s *agentFileAccessService) sweep(gen uint64) {
	// 整体兜底：正常 8 类 × 用户点按，几十秒内完成；卡住时靠单类超时收束。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 唯一的收尾点（含 panic 兜底）：必须复位，否则按钮会永久禁用、要重启
	// agent 才能恢复。用世代号守卫：只有「仍是当前这一代」才复位——否则旧
	// sweep 的收尾会把刚启动的新 sweep 状态清成 done。
	defer func() {
		if rec := recover(); rec != nil {
			logger.Warn(fmt.Sprintf("[file-access] sweep panic: %v", rec))
		}
		s.mu.Lock()
		if s.gen == gen {
			s.running = false
			s.finished = time.Now()
		}
		s.mu.Unlock()
	}()

	s.mu.Lock()
	keys := append([]string(nil), s.keys...)
	s.mu.Unlock()

	for i := range keys {
		key := keys[i]

		res := tccguard.ProbeCategory(ctx, key)

		s.mu.Lock()
		stale := s.gen != gen // 已被新一代 sweep 顶替：不要再写它的状态
		if !stale && i < len(s.cats) {
			s.cats[i].State = string(res.State)
			s.cats[i].Detail = res.Detail
		}
		stuck := res.State == tccguard.StatePending || res.State == tccguard.StateUnattended
		if stuck && !stale {
			// 剩余类别不再探测：没人应答时继续弹窗毫无意义。
			for j := i + 1; j < len(s.cats); j++ {
				s.cats[j].State = string(tccguard.StateSkipped)
			}
			if res.State == tccguard.StateUnattended {
				s.lastErr = "unattended"
			}
		}
		s.mu.Unlock()

		if stale {
			return // 收尾交给 defer，且它不会碰新一代的状态
		}
		if stuck {
			return
		}
	}
}
