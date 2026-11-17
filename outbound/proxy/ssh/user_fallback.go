// user_fallback.go — 私钥登录的登录用户名兜底:配置留空时统一用「本机名(合规化)」。
//
// 背景:SSH 协议任何认证方式都会带一个登录用户名,但产品侧私钥面已收走用户名输入
// (提交契约把 username 显式置空),拨号身份统一为本机名——与 mesh 互连载体的固定
// SSHUser "mesh" 同族(处理器侧先例)。密码登录不兜底:那是登远端真实账号,空用户名
// 密码认证必然失败,由 SSHProxyConfig.Validate 拦截,这里原样透传。
package ssh

import (
	"os"
	"strings"
)

// fallbackUsernameDefault 是本机名缺失或合规化后为空时的固定兜底名。
const fallbackUsernameDefault = "aliang-node"

// usernameMaxLength 是兜底用户名的保守长度上限(传统 POSIX 登录名 32 字符),
// 超长截断——真 FQDN 主机名不做登录名用。
const usernameMaxLength = 32

// hostnameForFallback 是 os.Hostname 的注入缝,测试可替换。
var hostnameForFallback = os.Hostname

// sanitizeUsername 把任意字符串规整成 SSH 登录名安全形态:仅保留字母/数字/
// '.'/'_'/'-',其余字符统一替换为 '-';修剪首尾 '-';按 rune 截断到上限;
// 结果为空回落固定兜底名。
func sanitizeUsername(raw string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(raw))
	// 首尾的 '-'/'.' 都是无效登录名形态(内部 '.' 合法,如 FQDN),修剪掉
	name = strings.Trim(name, "-.")
	if runes := []rune(name); len(runes) > usernameMaxLength {
		// 截断边界同样可能落在 '-'/'.' 上,与截断前同一套修剪口径
		name = strings.Trim(string(runes[:usernameMaxLength]), "-.")
	}
	if name == "" {
		return fallbackUsernameDefault
	}
	return name
}

// FallbackUsername 返回私钥登录空用户名时实际使用的兜底登录名(本机名合规化,
// 不可用回落固定名)。供测试与诊断展示对齐;生产拨号经 resolveUsername 走同一
// 份结果。
func FallbackUsername() string {
	host, err := hostnameForFallback()
	if err != nil || strings.TrimSpace(host) == "" {
		return fallbackUsernameDefault
	}
	return sanitizeUsername(host)
}

// resolveUsername 决定一次 SSH 拨号的登录用户名:显式配置优先;私钥登录且未填
// → 本机名兜底。密码路径(无私钥)不兜底,原样返回(空值由 Validate 拒)。
func resolveUsername(user, privateKey string) string {
	if user != "" || privateKey == "" {
		return user
	}
	return FallbackUsername()
}
