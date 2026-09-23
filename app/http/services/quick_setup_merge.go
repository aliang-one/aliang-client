// 快速配置的智能合并引擎——JSON 深合并 + codex config.toml 行级拼接。
package services

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// mergeQuickSetupJSONObjects 递归深合并 incoming 到 existing（incoming 的键胜出，
// existing 其余字段全保留）。两参均可为 nil。existing 必须：由调用方保证已成功解析。
// 返回合并后的 map；ok 当前恒为 true，预留错误通道。
func mergeQuickSetupJSONObjects(existing, incoming map[string]interface{}) (map[string]interface{}, bool) {
	if existing == nil {
		existing = map[string]interface{}{}
	}
	mergeQuickSetupJSONInto(existing, incoming)
	return existing, true
}

// mergeQuickSetupJSONInto 将 src 深合并进 dst（src 的键胜出，dst 其余字段全保留）。
// 契约：src 的子 map 以引用共享并入结果；调用方在 src 会被复用的场景下，不得再修改返回 map 的子对象。
func mergeQuickSetupJSONInto(dst, src map[string]interface{}) {
	for key, value := range src {
		srcObj, srcIsObj := value.(map[string]interface{})
		dstObj, dstIsObj := dst[key].(map[string]interface{})
		if srcIsObj && dstIsObj {
			mergeQuickSetupJSONInto(dstObj, srcObj)
			continue
		}
		dst[key] = value
	}
}

// 表头行识别：TOML 表名（裸键或基础字符串键）不得含 `]` 与 `#`，
// 因此 ^\[名字]$（可带行尾空白与注释，含紧贴 ] 的 #）足以区分真表头
// 与数组续行/行内 table 值。注释前不强制空白——`[x]# c` 也是合法 TOML。
var (
	codexTableHeaderRe      = regexp.MustCompile(`^\[([^#\]]+)]\s*(?:#.*)?$`)
	codexArrayTableHeaderRe = regexp.MustCompile(`^\[\[([^#\]]+)]]\s*(?:#.*)?$`)
)

// mergeCodexTOML 把我们管理的顶层键（model / model_provider）与
// [model_providers.<quickSetupCodexProviderID>] 段合并进 existing，
// 其余字节（注释/格式/其他表）原样保留（spec §7）。输出必须能通过 toml.Unmarshal，
// 校验失败返回错误而不产出可能损坏用户配置的结果。
func mergeCodexTOML(existing, model, baseURL string) (string, error) {
	eol := ""
	if strings.Contains(existing, "\r\n") {
		eol = "\r" // CRLF 文件里我们插入的行跟随原行尾风格
	}
	lines := strings.Split(existing, "\n")

	kept := make([]string, 0, len(lines)+8)
	inMultiline := false // """ / ''' 多行字符串状态（奇偶切换启发式）
	inAliang := false    // 正处于我们拥有的段内（整体丢弃待替换）
	currentTable := ""   // 空串 = 顶层区域
	aliangTable := "model_providers." + quickSetupCodexProviderID

	insertIdx := -1      // 首个被移除的顶层键行位置（我们的键插回这里，保持用户版式）
	firstHeaderIdx := -1 // 首个保留的表头行位置（顶层键的兜底插入点）
	aliangIdx := -1      // 已有 aliang 段的表头位置（原位替换）

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)

		if inMultiline {
			// 多行字符串内容逐字节保留：其中的伪表头/伪键一律不动
			kept = append(kept, raw)
			if codexQuoteToggle(line) {
				inMultiline = false
			}
			continue
		}

		if inAliang {
			if name, _, isHdr := codexTableHeader(trimmed); isHdr {
				// 下一个真表头：aliang 段结束，恢复正常处理
				inAliang = false
				currentTable = name
				if firstHeaderIdx < 0 {
					firstHeaderIdx = len(kept)
				}
				kept = append(kept, raw)
				continue
			}
			if codexQuoteToggle(line) {
				// 段内残留多行字符串：按字节保留并进入多行状态，
				// 避免把字符串内容误判为后续表头/顶层键
				inMultiline = true
				kept = append(kept, raw)
			}
			// 其余段内容（键/空行/注释）整体丢弃——我们拥有该段
			continue
		}

		if name, isArray, isHdr := codexTableHeader(trimmed); isHdr {
			currentTable = name
			if firstHeaderIdx < 0 {
				firstHeaderIdx = len(kept)
			}
			if !isArray && name == aliangTable {
				aliangIdx = len(kept)
				inAliang = true
				continue // 表头行由段体替换逻辑重建
			}
			// 表头行不 toggle 多行状态：能走到这里（非多行态）的一定是真表头，
			// 其非注释部分只是 [名字]，不可能开启多行字符串；行尾注释里的
			// 奇数个 """ 是合法 TOML 但不构成定界符，在此 toggle 只会假进入
			// 多行态，静默吞掉后续表头/顶层键的识别。
			kept = append(kept, raw)
			continue
		}

		// 顶层 model / model_provider 行移除，值统一由我们重写；
		// 表内（currentTable != ""）的同名键属于那个表，必须原样保留
		if currentTable == "" && codexTopLevelKey(trimmed) != "" {
			if insertIdx < 0 {
				insertIdx = len(kept)
			}
			if codexQuoteToggle(line) {
				inMultiline = true
			}
			continue
		}

		kept = append(kept, raw)
		if codexQuoteToggle(line) {
			inMultiline = true
		}
	}

	keyLines := []string{
		"model = " + quickSetupTOMLQuote(model) + eol,
		"model_provider = " + quickSetupTOMLQuote(quickSetupCodexProviderID) + eol,
	}
	if insertIdx < 0 {
		insertIdx = firstHeaderIdx
		if insertIdx < 0 {
			insertIdx = 0 // 无表头：插到文件头（仍属顶层区域，语义安全）
		}
	}
	kept = quickSetupInsertLines(kept, insertIdx, keyLines)

	sectionLines := buildCodexAliangSection(baseURL, eol)
	if aliangIdx >= 0 {
		idx := aliangIdx + len(keyLines)
		if idx < len(kept) && isCodexHeaderLine(kept[idx]) {
			sectionLines = append(sectionLines, "") // 与紧随的用户段之间留一个空行
		}
		kept = quickSetupInsertLines(kept, idx, sectionLines)
	} else {
		// 没有旧段：追加到文件尾。段前保证至少一个空行，但不保证恰好一个——
		// 文件无尾换行时恰补一个；以换行结尾时行终止符的影子会多出一个，
		// 尾部已有空行则进一步叠加（多出的空行仅影响版式，不影响 TOML 语义）。
		needBlank := true
		if n := len(kept); n > 0 && strings.TrimSpace(kept[n-1]) == "" {
			needBlank = n < 2 || strings.TrimSpace(kept[n-2]) != ""
		}
		if needBlank {
			kept = append(kept, "")
		}
		kept = append(kept, sectionLines...)
	}

	merged := strings.Join(kept, "\n")
	if !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	var check map[string]interface{}
	if err := toml.Unmarshal([]byte(merged), &check); err != nil {
		return "", fmt.Errorf("合并后的 codex config.toml 不是合法 TOML（共 %d 行）: %w",
			strings.Count(merged, "\n"), err)
	}
	return merged, nil
}

// codexQuoteToggle 判断一行是否切换多行字符串状态：
// 该行多行定界符（双引号或单引号各连三个）出现总次数为奇数即切换（约定的行级启发式，spec §7）。
func codexQuoteToggle(line string) bool {
	return (strings.Count(line, `"""`)+strings.Count(line, `'''`))%2 == 1
}

// codexTableHeader 识别表头行，返回表名与是否为 array-of-tables 表头。
// [[array]] 是表头（结束我们段的替换范围、结束顶层区域），但不触发 aliang 段替换。
func codexTableHeader(trimmed string) (name string, isArray bool, ok bool) {
	if m := codexArrayTableHeaderRe.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1]), true, true
	}
	if m := codexTableHeaderRe.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1]), false, true
	}
	return "", false, false
}

// isCodexHeaderLine 判断保留行（可能带 \r）是否为表头行。
func isCodexHeaderLine(raw string) bool {
	_, _, ok := codexTableHeader(strings.TrimSpace(strings.TrimRight(raw, "\r")))
	return ok
}

// codexTopLevelKey 识别 model / model_provider 键行（含带引号键形式），返回键名或空串。
// 仅靠「键名后跟 =」判定：值与行尾注释不影响识别；modelx=/model.x= 等不是我们的键。
func codexTopLevelKey(trimmed string) string {
	for _, k := range []string{"model", "model_provider", `"model"`, `"model_provider"`} {
		if !strings.HasPrefix(trimmed, k) {
			continue
		}
		rest := strings.TrimLeft(trimmed[len(k):], " \t")
		if strings.HasPrefix(rest, "=") {
			return strings.Trim(k, `"`)
		}
	}
	return ""
}

// buildCodexAliangSection 生成我们拥有的 provider 段（spec §7）。
func buildCodexAliangSection(baseURL, eol string) []string {
	return []string{
		"[model_providers." + quickSetupCodexProviderID + "]" + eol,
		`name = "Aliang Gateway"` + eol,
		"base_url = " + quickSetupTOMLQuote(baseURL) + eol,
		`env_key = "OPENAI_API_KEY"` + eol,
		`wire_api = "responses"` + eol,
	}
}

// quickSetupTOMLQuote 把值编码为 TOML 基本字符串（转义引号/反斜杠/控制字符）。
func quickSetupTOMLQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// quickSetupInsertLines 在 idx 处插入 vals：先扩容再整体右移，规避 append 底层数组别名。
func quickSetupInsertLines(s []string, idx int, vals []string) []string {
	s = append(s, make([]string, len(vals))...)
	copy(s[idx+len(vals):], s[idx:])
	copy(s[idx:], vals)
	return s
}
