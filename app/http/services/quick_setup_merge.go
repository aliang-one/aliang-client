// 快速配置的智能合并引擎——JSON 深合并；后续任务将追加 TOML 拼接器。
package services

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
