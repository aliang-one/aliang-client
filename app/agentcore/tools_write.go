package agentcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// 写工具：Write / Edit。
//
// Edit 的语义严格照抄 pi：给一段 oldText，必须在原文里**唯一**出现，然后原地
// 替换成 newText。不是 diff、不是行号替换、不是正则。这套语义看着朴素，但它是
// 小模型能稳定用对的少数几种编辑协议之一 —— 行号会因前面的编辑而漂移，diff
// 需要模型算上下文，正则则几乎必错。

// utf8BOM 是 UTF-8 字节序标记。用转义写而不是字面量：源码里藏一个不可见
// 字符，改起来没人看得见。
const utf8BOM = "\uFEFF"

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

type writeTool struct{}

func (t *writeTool) Name() string { return "Write" }

func (t *writeTool) Description() string {
	return "把内容整体写入文件（覆盖）。父目录不存在会自动创建。" +
		"修改已有文件请优先用 Edit，避免整体覆盖丢失未预期的改动。"
}

func (t *writeTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"file_path": stringProp("文件的绝对路径，或相对项目根的路径"),
		"content":   stringProp("要写入的完整内容"),
	}, "file_path", "content")
}

func (t *writeTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	raw := inputString(input, "file_path")
	if strings.TrimSpace(raw) == "" {
		return ErrorResult("缺少 file_path 参数")
	}
	content, ok := input["content"].(string)
	if !ok {
		return ErrorResult("缺少 content 参数（必须是字符串）")
	}
	if err := ctx.Err(); err != nil {
		return ErrorResult("写入被取消: %v", err)
	}

	path, err := resolveToolPath(env, raw)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return ErrorResult("%s 是目录，无法写入", raw)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ErrorResult("无法创建父目录: %v", err)
	}
	if err := atomicWriteFile(path, []byte(content)); err != nil {
		return ErrorResult("写入 %s 失败: %v", raw, err)
	}

	lines := strings.Count(content, "\n")
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		lines++
	}
	return ToolResult{Content: fmt.Sprintf("已写入 %s（%d 行，%d 字节）。", raw, lines, len(content))}
}

// atomicWriteFile 先写临时文件再 rename，避免写一半断电/崩溃留下截断的文件。
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".agentcore-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// CreateTemp 建的是 0600，改回常规文件权限，否则编辑器/构建工具可能读不了。
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

type editTool struct{}

func (t *editTool) Name() string { return "Edit" }

func (t *editTool) Description() string {
	return "在文件里把 old_string 精确替换成 new_string。old_string 必须在文件中唯一出现" +
		"（除非 replace_all=true），且要包含足够上下文以便定位。这是修改文件的默认方式。"
}

func (t *editTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"file_path":   stringProp("文件的绝对路径，或相对项目根的路径"),
		"old_string":  stringProp("要被替换的原文片段。必须在文件中唯一出现"),
		"new_string":  stringProp("替换后的内容。留空即删除该片段"),
		"replace_all": boolProp("替换所有出现（默认 false，此时要求唯一）"),
	}, "file_path", "old_string", "new_string")
}

func (t *editTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	raw := inputString(input, "file_path")
	if strings.TrimSpace(raw) == "" {
		return ErrorResult("缺少 file_path 参数")
	}
	oldText, ok := input["old_string"].(string)
	if !ok || oldText == "" {
		return ErrorResult("缺少 old_string 参数（不能为空）")
	}
	newText, _ := input["new_string"].(string)
	if oldText == newText {
		return ErrorResult("old_string 与 new_string 相同，无需修改")
	}
	replaceAll := inputBool(input, "replace_all")
	if err := ctx.Err(); err != nil {
		return ErrorResult("编辑被取消: %v", err)
	}

	path, err := resolveToolPath(env, raw)
	if err != nil {
		return ErrorResult("%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult("文件不存在: %s", raw)
		}
		return ErrorResult("无法访问 %s: %v", raw, err)
	}
	if info.IsDir() {
		return ErrorResult("%s 是目录，无法编辑", raw)
	}

	rawBytes, err := os.ReadFile(path)
	if err != nil {
		return ErrorResult("读取 %s 失败: %v", raw, err)
	}
	original := string(rawBytes)

	updated, usedFuzzy, err := applyEdit(original, oldText, newText, replaceAll)
	if err != nil {
		return ErrorResult("%v", err)
	}
	if err := atomicWriteFile(path, []byte(updated)); err != nil {
		return ErrorResult("写入 %s 失败: %v", raw, err)
	}

	note := ""
	if usedFuzzy {
		// 明确告知模型发生了模糊匹配：它给的 old_string 与文件实际内容有
		// 细微差异（全角/半角、兼容字符），下次应当照抄文件原文。
		note = "（注意：old_string 未精确命中，已按 NFKC 归一化后匹配。请核对结果。）"
	}
	return ToolResult{Content: fmt.Sprintf("已更新 %s。%s", raw, note)}
}

// applyEdit 是 pi 的编辑算法本体。
//
// 顺序：
//  1. 去 BOM，记下来最后补回
//  2. CRLF 归一成 LF（匹配时统一，回写时还原）
//  3. 在**原文**里数 oldText 出现次数 —— 不是逐次替换后的缓冲区
//  4. 唯一性检查：非 replaceAll 时出现次数必须恰好 1
//  5. 未命中时退到 NFKC 模糊匹配（全角/半角、兼容字符差异）
//  6. 从左到右非重叠替换
//  7. 还原行尾、补回 BOM
//
// 第 3 步是关键：在逐步修改的缓冲区里数出现次数，会让「替换文本本身包含
// oldText」的情况无限自我匹配。pi 的做法是一次性在原文里定位全部位置。
func applyEdit(original, oldText, newText string, replaceAll bool) (result string, usedFuzzy bool, err error) {
	body := original
	bom := ""
	if strings.HasPrefix(body, utf8BOM) {
		bom = utf8BOM
		body = strings.TrimPrefix(body, bom)
	}

	// 记录原文用的是 CRLF 还是 LF。混合行尾时按「含 CRLF 即视为 CRLF」处理，
	// 与 pi 一致：真实项目里几乎不会出现有意的混合行尾。
	crlf := strings.Contains(body, "\r\n")
	lfBody := normalizeToLF(body)
	lfOld := normalizeToLF(oldText)
	lfNew := normalizeToLF(newText)

	positions := findAll(lfBody, lfOld)
	if len(positions) == 0 {
		// 精确匹配失败 → 归一化后重试。这一步救的是「模型把全角括号写成半角」
		// 「中文标点写成英文标点」这类高频偏差。
		//
		// 关键点：归一化会改变字节长度（全角 Ａ 是 3 字节，归一成 A 只剩 1
		// 字节），所以命中位置**不能**直接拿去索引原文。用 normWithOffsets
		// 建一张「归一化字节偏移 → 原文字节偏移」的表，命中后映射回原文区间。
		fuzzyBody, offsets := normWithOffsets(lfBody)
		fuzzyOld := normalizeForFuzzyMatch(lfOld)
		fuzzyPositions := findAll(fuzzyBody, fuzzyOld)
		if len(fuzzyPositions) == 0 {
			return "", false, fmt.Errorf(
				"在文件中找不到 old_string。请先用 Read 查看当前内容，再原样复制要替换的片段（包含缩进）")
		}
		if len(fuzzyPositions) > 1 {
			// 模糊匹配下批量替换风险太高（无法确认每处语义一致），只做唯一命中。
			return "", false, fmt.Errorf(
				"old_string 有 %d 处模糊匹配，不唯一。请补充上下文让它唯一，或改用精确文本", len(fuzzyPositions))
		}

		start := offsets[fuzzyPositions[0]]
		end := offsets[fuzzyPositions[0]+len(fuzzyOld)]
		if start < 0 || end < 0 || end < start || end > len(lfBody) {
			return "", false, fmt.Errorf("old_string 与文件内容的差异超出模糊匹配能力，请用 Read 核对原文后重试")
		}
		replaced := lfBody[:start] + lfNew + lfBody[end:]
		return finishEdit(replaced, bom, crlf), true, nil
	}

	if !replaceAll && len(positions) > 1 {
		return "", false, fmt.Errorf(
			"old_string 在文件中出现 %d 次，不唯一。请补充上下文让它唯一，或设置 replace_all=true", len(positions))
	}

	var sb strings.Builder
	sb.Grow(len(lfBody) + len(lfNew)*len(positions))
	last := 0
	for _, pos := range positions {
		if pos < last {
			continue // 非重叠：跳过与上一次替换重叠的位置
		}
		sb.WriteString(lfBody[last:pos])
		sb.WriteString(lfNew)
		last = pos + len(lfOld)
		if !replaceAll {
			break
		}
	}
	sb.WriteString(lfBody[last:])
	return finishEdit(sb.String(), bom, crlf), false, nil
}

// finishEdit 还原行尾与 BOM。
func finishEdit(body, bom string, crlf bool) string {
	if crlf {
		body = restoreLineEndings(body)
	}
	return bom + body
}

// findAll 返回 needle 在 haystack 中所有（可重叠的）起始位置。
//
// 用可重叠扫描而不是 strings.Index 循环：后者对 "aaa" 找 "aa" 只会得到 [0]，
// 而唯一性检查必须知道真实出现次数。
func findAll(haystack, needle string) []int {
	if needle == "" {
		return nil
	}
	var out []int
	for i := 0; i+len(needle) <= len(haystack); {
		idx := strings.Index(haystack[i:], needle)
		if idx < 0 {
			break
		}
		out = append(out, i+idx)
		i += idx + 1
	}
	return out
}

// normalizeToLF 把 CRLF 归一成 LF。
func normalizeToLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// restoreLineEndings 把 LF 还原成 CRLF。
func restoreLineEndings(s string) string {
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// normalizeForFuzzyMatch 做 NFKC 归一化，用于模糊匹配回退。
//
// 覆盖的典型偏差：全角字母数字（ＡＢＣ → ABC）、全角标点（，→ ,）、
// 兼容汉字、不间断空格等。
func normalizeForFuzzyMatch(s string) string {
	return norm.NFKC.String(s)
}

// normWithOffsets 逐字符做 NFKC 归一化，并返回一张偏移映射表。
//
// offsets[i] = 归一化文本第 i 个字节位置对应的**原文**字节位置；长度是
// len(normalized)+1，末尾一项等于 len(original)。这样命中一段归一化文本后，
// 两端各查一次就能还原出原文区间。
//
// 逐字符归一化而非整串：整串 norm.NFKC.String 无法给出偏移对应关系。
// 代价是个别跨字符的组合序列（极少见于代码文件）不会被归一化——可以接受，
// 因为模糊匹配本来就是尽力而为。
func normWithOffsets(s string) (string, []int) {
	var sb strings.Builder
	sb.Grow(len(s))
	offsets := make([]int, 0, len(s)+1)

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// 非法 UTF-8 字节：原样保留，偏移一一对应。
			sb.WriteByte(s[i])
			offsets = append(offsets, i)
			i++
			continue
		}
		normed := norm.NFKC.String(s[i : i+size])
		// 归一化后的每个字节都映射回这个字符在原文中的起点。
		for j := 0; j < len(normed); j++ {
			offsets = append(offsets, i)
		}
		sb.WriteString(normed)
		i += size
	}
	offsets = append(offsets, len(s))
	return sb.String(), offsets
}

// 编译期确认工具实现了接口。
var (
	_ Tool = (*readTool)(nil)
	_ Tool = (*writeTool)(nil)
	_ Tool = (*editTool)(nil)
	_ Tool = (*bashTool)(nil)
	_ Tool = (*powershellTool)(nil)
	_ Tool = (*grepTool)(nil)
	_ Tool = (*globTool)(nil)
	_ Tool = (*lsTool)(nil)
)

// 确保 utf8 被引用（保留给将来的编码校验）。
var _ = utf8.RuneLen
