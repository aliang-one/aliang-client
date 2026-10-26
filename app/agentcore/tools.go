package agentcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 工具层。
//
// 工具名用 Claude 的规范名（Read / Write / Edit / Bash / Grep / Glob / LS /
// PowerShell），不用 pi 的小写名。这不是风格选择：宿主 claudeToolUseEvents
// （app/http/services/agent_ai.go:6530）和 claudeMessageToolUseFilePaths
// （:6444）按名字分派，只认这几个 —— 用 pi 的名字，手机端的命令卡和文件变更
// 卡会全部哑掉。语义仍然按 pi 来（尤其 Edit 的 exact-oldText），能力对等不变。

// ToolResult 是一次工具执行的结果。
type ToolResult struct {
	// Content 是回填给模型的文本。
	Content string
	// Images 是随结果一起回填的图片块（Read 读图片时用）。
	//
	// 为什么工具结果要能带图：Read 是模型接触文件的唯一通道，只回一句「这是
	// 图片」等于告诉模型「你看不到」。把字节真的内联进 tool_result，模型才能
	// 描述截图、读图表数字。
	//
	// 为空时 tool_result 的 content 仍是纯字符串（形状与之前完全一致，不影响
	// 任何现有工具）；非空时变成「文本块 + 图片块」数组。
	Images []ContentBlock
	// IsError 为 true 时以 is_error 回填，模型会据此自行纠错。
	IsError bool
}

// ContentForModel 返回回填给模型的 tool_result content。
//
// Anthropic 的 tool_result.content 既可以是字符串也可以是内容块数组，这里按
// 有没有图片选形状：无图走字符串（少一层解析，也更贴近真实 claude-code 的
// 请求形状），有图才升级成数组。
func (r ToolResult) ContentForModel() interface{} {
	if len(r.Images) == 0 {
		return r.Content
	}
	blocks := make([]ContentBlock, 0, len(r.Images)+1)
	if strings.TrimSpace(r.Content) != "" {
		blocks = append(blocks, ContentBlock{Type: BlockTypeText, Text: r.Content})
	}
	blocks = append(blocks, r.Images...)
	return blocks
}

// ErrorResult 构造一个失败结果。
func ErrorResult(format string, args ...interface{}) ToolResult {
	return ToolResult{Content: fmt.Sprintf(format, args...), IsError: true}
}

// ToolEnv 是工具执行时的环境。
type ToolEnv struct {
	// ProjectPath 是会话的项目根。所有相对路径以此为基准。
	ProjectPath string
	// ScopeCheck 是宿主注入的只读围栏（goal 模式）。nil 表示不限制。
	// 返回 (false, reason) 时工具不得执行。
	ScopeCheck func(tool string, args map[string]interface{}) (bool, string)
	// ShellPolicy 是宿主注入的 shell 策略。nil 表示禁止一切命令执行。
	//
	// 为什么由宿主注入而不是内核自己实现：命令白名单与「可信 shell 路径」
	// 的判定逻辑已经在 app/http/services/agent_execution_guard.go 里，但本包
	// 禁止 import services。所以把策略做成窄接口，宿主实现、内核调用。
	// nil 时 fail closed —— 没配策略就不给执行，而不是默认放行。
	ShellPolicy ShellPolicy
	// Env 是子进程（Bash/PowerShell）的完整环境。nil = 继承宿主进程环境
	// （旧行为）。宿主通过这里下发「用户登录 shell 快照 + TERM/LANG 兜底」，
	// 否则 GUI/systemd 启动的 agent 只有系统四件套 PATH，node/npm 等用户级
	// 工具全部不可见——aliangcode「没有 nodejs」误报的执行层根因。
	Env []string
}

// ShellPolicy 是宿主注入的 shell 执行策略。
type ShellPolicy interface {
	// Resolve 返回可信的 shell 可执行文件与固定参数。
	// kind 取值 "bash" | "powershell"；该平台不支持时返回 error。
	Resolve(kind string) (path string, baseArgs []string, err error)
	// Allow 判定命令是否在白名单内。返回 (false, reason) 时拒绝执行。
	Allow(command string) (bool, string)
}

// Tool 是一个可被模型调用的工具。
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]interface{}
	Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult
}

// Registry 是工具注册表。零值不可用，用 NewRegistry 构造。
type Registry struct {
	tools map[string]Tool
	order []string
}

// NewRegistry 构造注册表。
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register 注册一个工具。重名会覆盖（后注册者胜），便于测试替换。
func (r *Registry) Register(t Tool) {
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	name := t.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = t
}

// Get 按名字取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

// Defs 返回下发给模型的工具定义列表（顺序稳定）。
func (r *Registry) Defs() []ToolDef {
	if r == nil {
		return nil
	}
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	defs := make([]ToolDef, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		defs = append(defs, ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// DefaultRegistry 返回 pi 对等的 8 个工具。
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&readTool{})
	r.Register(&writeTool{})
	r.Register(&editTool{})
	r.Register(&bashTool{})
	r.Register(&powershellTool{})
	r.Register(&grepTool{})
	r.Register(&globTool{})
	r.Register(&lsTool{})
	return r
}

// ---------------------------------------------------------------------------
// 路径围栏
// ---------------------------------------------------------------------------

// maxToolOutputBytes 是单次工具回填给模型的上限。超出即截断并明确标注——
// 静默截断会让模型以为自己看完了全文。
const maxToolOutputBytes = 64 * 1024

// resolveToolPath 把工具入参里的路径解析成绝对路径，并挡掉逃逸出项目根的情况。
//
// 规则：
//   - 空路径 → 项目根（LS 用）
//   - 相对路径 → 相对项目根
//   - ~ 展开
//   - 解析后（含符号链接）必须仍在项目根内
//
// 项目根为空时（未绑定项目）不做围栏，只做绝对化——由调用方决定是否允许。
func resolveToolPath(env *ToolEnv, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	root := ""
	if env != nil {
		root = strings.TrimSpace(env.ProjectPath)
	}
	if raw == "" {
		if root == "" {
			return "", fmt.Errorf("缺少 path 参数")
		}
		return filepath.Abs(root)
	}

	expanded, err := expandHome(raw)
	if err != nil {
		return "", err
	}

	var abs string
	if filepath.IsAbs(expanded) {
		abs = filepath.Clean(expanded)
	} else {
		if root == "" {
			return "", fmt.Errorf("未绑定项目目录，无法解析相对路径: %s", raw)
		}
		abs = filepath.Clean(filepath.Join(root, expanded))
	}

	if root == "" {
		return abs, nil
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// 用 EvalSymlinks 兜住「软链指到项目外」的逃逸；路径不存在时（新建文件）
	// 退回逐级解析最近的已存在祖先。
	resolvedRoot, err := evalSymlinksBestEffort(rootAbs)
	if err != nil {
		return "", err
	}
	resolvedAbs, err := evalSymlinksBestEffort(abs)
	if err != nil {
		return "", err
	}
	if !pathWithin(resolvedAbs, resolvedRoot) {
		return "", fmt.Errorf("路径越界，拒绝访问项目目录之外的位置: %s", raw)
	}
	return abs, nil
}

// evalSymlinksBestEffort 解析符号链接；目标不存在时向上找到最近的存在祖先，
// 解析它再把剩余段拼回去。这样「新建文件」也能正确判定围栏。
func evalSymlinksBestEffort(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	var missing []string
	cur := filepath.Clean(p)
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(p), nil
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
		resolved, err := filepath.EvalSymlinks(cur)
		if err != nil {
			continue
		}
		return filepath.Join(append([]string{resolved}, missing...)...), nil
	}
}

// pathWithin 判定 child 是否在 root 之内（含 root 本身）。
func pathWithin(child, root string) bool {
	child = filepath.Clean(child)
	root = filepath.Clean(root)
	if child == root {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// expandHome 展开前导 ~。
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法定位用户主目录: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// truncateToolOutput 按字节上限截断工具输出，并在尾部标注被截掉了多少。
func truncateToolOutput(s string) string {
	if len(s) <= maxToolOutputBytes {
		return s
	}
	cut := s[:maxToolOutputBytes]
	// 尽量在行边界切，避免半个 UTF-8 字符。
	if idx := strings.LastIndexByte(cut, '\n'); idx > maxToolOutputBytes/2 {
		cut = cut[:idx]
	} else {
		cut = strings.ToValidUTF8(cut, "")
	}
	return fmt.Sprintf("%s\n\n[输出已截断：原始 %d 字节，仅显示前 %d 字节。请用更精确的 offset/limit 或更窄的匹配缩小范围。]",
		cut, len(s), len(cut))
}

// ---------------------------------------------------------------------------
// schema 辅助
// ---------------------------------------------------------------------------

// objectSchema 构造 input_schema。
func objectSchema(props map[string]interface{}, required ...string) map[string]interface{} {
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// stringProp 构造一个字符串属性。
func stringProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

// intProp 构造一个整数属性。
func intProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

// boolProp 构造一个布尔属性。
func boolProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

// inputString 取字符串入参。
func inputString(input map[string]interface{}, key string) string {
	if input == nil {
		return ""
	}
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

// inputInt 取整数入参。JSON 解出来是 float64，也容忍字符串数字。
func inputInt(input map[string]interface{}, key string) (int, bool) {
	if input == nil {
		return 0, false
	}
	switch v := input[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// inputBool 取布尔入参。
func inputBool(input map[string]interface{}, key string) bool {
	if input == nil {
		return false
	}
	v, _ := input[key].(bool)
	return v
}
