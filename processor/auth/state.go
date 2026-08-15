package user

import "strings"

// GetCurrentUserInfoOrLoad returns the in-memory auth state when available,
// and falls back to persisted auth state only when memory is empty.
func GetCurrentUserInfoOrLoad() *UserInfo {
	current := GetCurrentUserInfo()
	if current != nil {
		return current
	}

	loaded, err := LoadUserInfo()
	if err != nil {
		return nil
	}
	return loaded
}

// GetCurrentUserInfoWithAccessTokenOrLoad returns the best available auth
// state for outbound authorization header injection.
func GetCurrentUserInfoWithAccessTokenOrLoad() *UserInfo {
	current := GetCurrentUserInfo()
	if current != nil && strings.TrimSpace(current.AccessToken) != "" {
		return current
	}

	loaded, err := LoadUserInfo()
	if err != nil {
		return current
	}
	if loaded == nil || strings.TrimSpace(loaded.AccessToken) == "" {
		return current
	}
	return loaded
}
