// 快速配置的智能合并引擎——JSON 深合并；后续任务将追加 TOML 拼接器。
package services

// mergeQuickSetupJSONObjects 递归深合并 incoming 到 existing（incoming 的键胜出，
// existing 其余字段全保留）。两参均可为 nil。existing 必须：由调用方保证已成功解析。
// 返回合并后的 map；ok=false 仅在 Marshal 失败等内部错误时出现。
func mergeQuickSetupJSONObjects(existing, incoming map[string]interface{}) (map[string]interface{}, bool) {
	if existing == nil {
		existing = map[string]interface{}{}
	}
	mergeQuickSetupJSONInto(existing, incoming)
	return existing, true
}

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
