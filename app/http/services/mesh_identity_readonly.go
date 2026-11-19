package services

// mesh 身份只读访问面(组网诊断读面批 Task 6,GET /api/mesh/identity 数据源)。
//
// ⚠️ 为什么不用既有入口(评审实锤):CurrentDeviceID 的文档注释自称「只读
// 查询」,实际链路 currentDeviceID → ensureDeviceIdentityLocked →
// pinPermanentDeviceIDLocked——身份缺失会在读路径上生成并落盘新身份(读
// 端点产写副作用=本批红线,且正是本端点要抓的「文档措辞撒谎」陷阱)。
// 而四个真源在 handlers 包内不可达:agentIdentityPath/loadAgentDeviceIdentity/
// agentDeviceIdentity/s.state.DeviceID 全小写未导出。故在此钉两个导出只读
// 访问器:零副作用读,不做任何生成。

import (
	"encoding/json"
	"os"
	"strings"
)

// LoadAgentIdentityFileReadonly 常驻身份文件只读解析:返回 (文件路径, 文件内
// device_id)。照 loadAgentDeviceIdentity 同款 ReadFile+Unmarshal 零副作用,
// 路径走 agentIdentityPath 同款 IsUserAgentRuntime 分叉(UserStateDir/agent/
// 或 cache.GetCacheSubdir("agent"))。三态:路径解析失败或文件缺失 →
// ("", "")(identity_file_path 本身是诊断信息,缺失=空串,消费方以空判
// 「无常驻身份文件」);JSON 解析失败 → (路径, "");正常 → (路径, device_id,
// TrimSpace 归一——空白值视同空,防比对面伪 mismatch)。
func LoadAgentIdentityFileReadonly() (path, deviceID string) {
	path, err := agentIdentityPath()
	if err != nil {
		return "", ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var id agentDeviceIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		return path, ""
	}
	return path, strings.TrimSpace(id.DeviceID)
}

// StateDeviceIDReadonly 进程内 state.DeviceID 只读访问(s.mu 锁内读,不生成)。
// 与 CurrentDeviceID 的本质分野:本访问器绝不触发 ensureDeviceIdentityLocked
// ——身份缺失返回空串(未注册/boot 未完成是合法态),由消费方(诊断面)
// 直读空值自行判读,而不是在读路径上静默铸新身份。TrimSpace 归一同上。
func (s *AgentService) StateDeviceIDReadonly() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.state.DeviceID)
}
