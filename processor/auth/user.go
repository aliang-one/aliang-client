package user

import (
	"strings"

	"aliang.one/nursorgate/common/logger"
)

func GetCurrentAuthorizationHeader() string {
	current := resolveUserInfoForAuthorizationHeader()
	if current == nil {
		return ""
	}

	accessToken := strings.TrimSpace(current.AccessToken)
	if accessToken == "" {
		return ""
	}

	tokenType := strings.TrimSpace(current.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return tokenType + " " + accessToken
}

func resolveUserInfoForAuthorizationHeader() *UserInfo {
	current := GetCurrentUserInfoWithAccessTokenOrLoad()
	if current != nil && strings.TrimSpace(current.AccessToken) != "" {
		if inMemory := GetCurrentUserInfo(); inMemory == nil {
			logger.Debug("Authorization header resolved from persisted user info")
		}
	}
	return current
}
