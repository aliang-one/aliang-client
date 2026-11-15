// Package handlers — 状态页「接入客户端」handler：GET /api/status/ingress
// （台账 + 黑名单列表）与 POST /api/status/ingress/kick|block|unblock
// （管理动作：踢出在线连接；指纹/device_id 加入或移出拒绝名单——持久化进
// customer.ssh_ingress，经既有 customer 配置提交通道，保存回调里
// ReconcileSSHIngressFromConfig 因 runIdentity 凭证未变而 no-op，黑名单
// 经活闭包即时生效）。
// 数据源=inbound/ssh 进程级 reconcile 实例的会话台账与全局配置。
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	sshin "aliang.one/nursorgate/inbound/ssh"
	"aliang.one/nursorgate/processor/config"
)

// ingressBlocksView 是 GET 响应里的拒绝名单视图（运行面口径，供前端渲染
// 管理动作的状态——某条在线会话是否已被拉黑）。
type ingressBlocksView struct {
	Fingerprints []string `json:"fingerprints"`
	DeviceIDs    []string `json:"device_ids"`
}

// HandleIngressSessions GET /api/status/ingress：在线接入客户端 + 已关闭
// 环形历史（300 条,新→旧）+ 拒绝名单视图。非 GET 一律 405。
func HandleIngressSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot, ok := sshin.SessionsSnapshotOf()
	if !ok {
		common.Success(w, map[string]interface{}{
			"ok":     false,
			"active": []sshin.Session{},
			"closed": []sshin.Session{},
			// 黑名单视图独立于运行态：入口停着，名单也是配置的一部分，管理
			// 动作（解封）需要对它可见可操作。
			"blocks": ingressBlocks(),
		})
		return
	}
	common.Success(w, map[string]interface{}{
		"ok":     true,
		"active": snapshot.Active,
		"closed": snapshot.Closed,
		"blocks": ingressBlocks(),
	})
}

// ingressBlocks 读全局配置的拒绝名单（空恒回空数组——API 序列化不出现 null）。
func ingressBlocks() ingressBlocksView {
	view := ingressBlocksView{Fingerprints: []string{}, DeviceIDs: []string{}}
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Customer == nil || cfg.Customer.SSHIngress == nil {
		return view
	}
	si := cfg.Customer.SSHIngress
	for _, fp := range si.BlockedFingerprints {
		if line := strings.TrimSpace(fp); line != "" {
			view.Fingerprints = append(view.Fingerprints, line)
		}
	}
	for _, id := range si.BlockedDeviceIDs {
		if line := strings.TrimSpace(id); line != "" {
			view.DeviceIDs = append(view.DeviceIDs, line)
		}
	}
	return view
}

// ingressActionRequest 是管理动作的统一载荷。
type ingressActionRequest struct {
	Fingerprint string `json:"fingerprint"` // 踢出/按指纹拉黑（SHA256:xxx）
	DeviceID    string `json:"device_id"`   // 按 mesh 标签拉黑
}

// HandleIngressKick POST /api/status/ingress/kick：按指纹断开一条在线接入
// 连接（标记 Kicked 落关闭历史）。不触碰黑名单——补拦请再调 block。
func HandleIngressKick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req ingressActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		common.ErrorBadRequest(w, "invalid request body: "+err.Error(), nil)
		return
	}
	fp := strings.TrimSpace(req.Fingerprint)
	if fp == "" {
		common.ErrorBadRequest(w, "fingerprint is required", nil)
		return
	}
	if !sshin.KickSession(fp) {
		common.ErrorNotFound(w, "session not online")
		return
	}
	common.Success(w, map[string]interface{}{"kicked": true, "fingerprint": fp})
}

// HandleIngressBlock POST /api/status/ingress/block：把指纹或 mesh 标签加入
// 拒绝名单（可选踢出——默认同时断开当前在线连接，no_kick=true 只拉黑不断）。
// 持久化走 customer 配置提交通道（patch 深合并），认证期活闭包即时生效。
func HandleIngressBlock(w http.ResponseWriter, r *http.Request) {
	fp, deviceID, noKick, err := decodeIngressBlockAction(r)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	if fp == "" && deviceID == "" {
		common.ErrorBadRequest(w, "fingerprint or device_id is required", nil)
		return
	}
	if err := patchSSHIngressBlocklist(fp, deviceID, true); err != nil {
		if isCustomerConfigValidationErr(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	kicked := false
	if !noKick && fp != "" {
		kicked = sshin.KickSession(fp)
	}
	common.Success(w, map[string]interface{}{
		"blocked":     true,
		"fingerprint": fp,
		"device_id":   deviceID,
		"kicked":      kicked,
	})
}

// HandleIngressUnblock POST /api/status/ingress/unblock：移出拒绝名单。
func HandleIngressUnblock(w http.ResponseWriter, r *http.Request) {
	fp, deviceID, _, err := decodeIngressBlockAction(r)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	if fp == "" && deviceID == "" {
		common.ErrorBadRequest(w, "fingerprint or device_id is required", nil)
		return
	}
	if err := patchSSHIngressBlocklist(fp, deviceID, false); err != nil {
		if isCustomerConfigValidationErr(err) {
			common.ErrorBadRequest(w, err.Error(), nil)
			return
		}
		common.ErrorInternalServer(w, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{
		"blocked":     false,
		"fingerprint": fp,
		"device_id":   deviceID,
	})
}

// decodeIngressBlockAction 解码并规整 block/unblock 载荷。
func decodeIngressBlockAction(r *http.Request) (fingerprint, deviceID string, noKick bool, err error) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
		DeviceID    string `json:"device_id"`
		NoKick      bool   `json:"no_kick"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", "", false, fmt.Errorf("invalid request body: %w", err)
	}
	return strings.TrimSpace(req.Fingerprint), strings.TrimSpace(req.DeviceID), req.NoKick, nil
}

// patchSSHIngressBlocklist 以 customer 配置 patch 通道增删拒绝名单条目。
// add=false 表示移除。构造 patch {ssh_ingress:{blocked_fingerprints:[...新全集]}}
// 经 UpdateCommittedCustomerConfig 的深合并整体覆盖数组（deep-merge 数组是
// 整体替换语义），验证/持久化/内存提交与既有保存通道完全同路。
func patchSSHIngressBlocklist(fp, deviceID string, add bool) error {
	current, _, err := services.NewCustomerConfigService().GetCommittedCustomerConfig()
	if err != nil {
		return fmt.Errorf("load current customer config: %w", err)
	}
	siMap, _ := current["ssh_ingress"].(map[string]interface{})
	if siMap == nil {
		siMap = map[string]interface{}{}
	}
	fps := stringListFromAny(siMap["blocked_fingerprints"])
	ids := stringListFromAny(siMap["blocked_device_ids"])
	if add {
		if fp != "" && !containsString(fps, fp) {
			fps = append(fps, fp)
		}
		if deviceID != "" && !containsString(ids, deviceID) {
			ids = append(ids, deviceID)
		}
	} else {
		fps = removeString(fps, fp)
		ids = removeString(ids, deviceID)
	}
	siMap["blocked_fingerprints"] = fps
	siMap["blocked_device_ids"] = ids
	current["ssh_ingress"] = siMap
	payload, err := json.Marshal(map[string]interface{}{"ssh_ingress": siMap})
	if err != nil {
		return fmt.Errorf("marshal blocklist patch: %w", err)
	}
	if _, err := services.NewCustomerConfigService().UpdateCommittedCustomerConfig(payload); err != nil {
		return err // 保持 *CustomerConfigValidationError 类型——调用方按 400 映射
	}
	return nil
}

// isCustomerConfigValidationErr 报告错误是否为 customer 配置校验拒绝
// （黑名单条目格式不合法等——客户端可修正的输入错误，映射 400 而非 500）。
func isCustomerConfigValidationErr(err error) bool {
	var verr *services.CustomerConfigValidationError
	return errors.As(err, &verr)
}

// stringListFromAny 把 JSON 反序列化出的 any 转字符串列表（缺失/形态不
// 符 → 空）。
func stringListFromAny(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			if line := strings.TrimSpace(s); line != "" {
				out = append(out, line)
			}
		}
	}
	return out
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func removeString(list []string, target string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if target != "" && item == target {
			continue
		}
		out = append(out, item)
	}
	return out
}
