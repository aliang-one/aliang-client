package tccguard

import (
	"context"
	"errors"
	"path/filepath"
)

// 立即授权（proactive authorization）。
//
// 背景：TCC 授权是「第一次访问时才弹窗」的。手机驱动的 run 常常发生在没人守在
// Mac 前的时候，此时弹窗无人应答、请求进程被 tcd 同步阻塞，工具调用直接卡死
// （2026-11-01 实测卡了 1h58m）。本文件提供一组导出 API，让界面可以在**用户
// 就在电脑前**的时候，主动把这几类目录各弹一次窗、把授权一次性拿全。
//
// 两条不可违反的约束：
//
//  1. **绝不预写 grant。** recordGrant 只在一次真实探测成功后调用。手工写
//     tcc-grants.json 会让 GuardPath 返回 nil，但 macOS 的 TCC 库里根本没有
//     授权 —— 卡死会从「守卫能发现并拒绝」退化成「静默阻塞在工具调用里」，
//     比现在更糟。
//  2. **只在有人值守时探测。** 无人值守时直接返回 unattended，绝不制造一个
//     没人能点的系统弹窗（那正是本功能要消灭的场景）。

// Category 是一类受 TCC 保护的目录。
//
// Label 刻意不在这里返回：中文文案属于界面层，由前端用 key 做 i18n。
type Category struct {
	// Key 是稳定标识：desktop/documents/downloads/trash/music/pictures/
	// icloud/cloudstorage。
	Key string
	// Root 是这一类的绝对根路径（探测就是读这个目录本身）。
	Root string
}

// CategoryState 是单个类别的授权状态。
type CategoryState string

const (
	// StateGranted 已授权（缓存里有 grant，或本次探测刚成功）。
	StateGranted CategoryState = "granted"
	// StateDenied 探测被拒（EPERM/EACCES），通常是用户点了「不允许」。
	StateDenied CategoryState = "denied"
	// StateAbsent 目录不存在，无需授权。
	StateAbsent CategoryState = "absent"
	// StatePending 等待超时：弹窗可能还挂着没人点。
	StatePending CategoryState = "pending"
	// StateFailed 其他错误。
	StateFailed CategoryState = "failed"
	// StateUnattended 无人值守，未探测。
	StateUnattended CategoryState = "unattended"
	// StateUnsupported 非 macOS 或守卫未启用。
	StateUnsupported CategoryState = "unsupported"
	// StateSkipped 前面已有类别卡住，本类未探测（避免连环弹窗）。
	StateSkipped CategoryState = "skipped"
)

// CategoryResult 是单个类别的授权结果。
type CategoryResult struct {
	Key    string        `json:"key"`
	Root   string        `json:"root"`
	State  CategoryState `json:"state"`
	Detail string        `json:"detail,omitempty"`
}

// Supported 报告当前平台是否支持 TCC 守卫（非 darwin 为 false）。
//
// 刻意用 supportedFn 而不是 guardEnabled：后者在测试装了 SetTestHooks 时恒为
// true（那是为了强制放行 GuardPath），拿来回答「这个平台支不支持」会答错。
func Supported() bool { return supportedFn() }

// Attended 报告此刻是否有人在这台 Mac 前（解锁、控制台用户匹配、HID 空闲
// < 5min）。结果带 5 秒缓存，可放心轮询。
func Attended() bool {
	if !guardEnabled() {
		return false
	}
	return attendedFn()
}

// Categories 返回受保护类别清单（静态元数据，**不触碰磁盘**）。
//
// 刻意不在这里 stat 判断存在性：stat 一个受保护目录本身就会弹 TCC 框，那是
// 探测阶段才该做的事。
func Categories() []Category {
	home, err := homeFn()
	if err != nil || home == "" {
		return nil
	}
	out := make([]Category, 0, len(homeProtectedSubdirs))
	for _, sub := range homeProtectedSubdirs {
		out = append(out, Category{Key: sub.key, Root: filepath.Join(home, sub.rel)})
	}
	return out
}

// GrantedCategories 返回已有 grant 的类别 key（只读缓存，零磁盘探测）。
func GrantedCategories() []string {
	if !guardEnabled() {
		return nil
	}
	home, err := homeFn()
	if err != nil || home == "" {
		return nil
	}
	out := make([]string, 0, len(homeProtectedSubdirs))
	for _, sub := range homeProtectedSubdirs {
		root := filepath.Join(home, sub.rel)
		cat, outcome := classify(root, home)
		if outcome != classProtected {
			continue
		}
		if hasGrant(cat.key) {
			out = append(out, sub.key)
		}
	}
	return out
}

// ProbeCategory 主动为某一类申请授权：有人值守时读一次该类根目录（这会触发
// 系统弹窗），成功后记录 grant；无人值守直接返回 unattended，不弹窗。
//
// ctx 取消时返回 pending —— 底层探测仍会在后台跑完（probeWait 上限），迟到的
// 「允许」依然会被记录，这与 probeCategory 的既有设计一致。
func ProbeCategory(ctx context.Context, key string) CategoryResult {
	if !guardEnabled() {
		return CategoryResult{Key: key, State: StateUnsupported}
	}
	home, err := homeFn()
	if err != nil || home == "" {
		return CategoryResult{Key: key, State: StateFailed, Detail: "无法定位家目录"}
	}
	var rel string
	for _, sub := range homeProtectedSubdirs {
		if sub.key == key {
			rel = sub.rel
			break
		}
	}
	if rel == "" {
		return CategoryResult{Key: key, State: StateFailed, Detail: "未知的类别"}
	}
	root := filepath.Join(home, rel)

	cat, outcome := classify(root, home)
	if outcome != classProtected {
		return CategoryResult{Key: key, Root: root, State: StateFailed, Detail: "路径未被判定为受保护类别"}
	}
	if hasGrant(cat.key) {
		return CategoryResult{Key: key, Root: root, State: StateGranted}
	}
	if !attendedFn() {
		return CategoryResult{Key: key, Root: root, State: StateUnattended}
	}

	done := make(chan probeResult, 1)
	go func() { done <- probeCategoryResult(cat.key, cat.root) }()

	select {
	case res := <-done:
		switch {
		case res.absent:
			return CategoryResult{Key: key, Root: root, State: StateAbsent}
		case res.err == nil:
			return CategoryResult{Key: key, Root: root, State: StateGranted}
		case errors.Is(res.err, errProbeTimeout):
			return CategoryResult{Key: key, Root: root, State: StatePending}
		default:
			return CategoryResult{Key: key, Root: root, State: StateDenied, Detail: res.err.Error()}
		}
	case <-ctx.Done():
		return CategoryResult{Key: key, Root: root, State: StatePending, Detail: "等待用户确认超时"}
	}
}
