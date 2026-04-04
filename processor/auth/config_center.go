package user

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// GetConfigByTag fetches the default config from the backend service filtered by tag.
func GetConfigByTag(tag string) (json.RawMessage, error) {
	query := url.Values{}
	if tag != "" {
		query.Set("tag", tag)
	}
	return fetchDashboardData("/api/v1/configs/default", query)
}

// GetGlobalVars fetches all global variables from the backend service.
func GetGlobalVars() (json.RawMessage, error) {
	return fetchDashboardData("/api/v1/admin/config-center/global-vars", nil)
}

// GlobalVar represents a single global variable key-value pair from the backend.
type GlobalVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ParseGlobalVars parses the raw JSON response from the global-vars endpoint into a map.
func ParseGlobalVars(raw json.RawMessage) map[string]string {
	result := make(map[string]string)
	if len(raw) == 0 {
		return result
	}

	// Try array format: [{"key":"k","value":"v"}, ...]
	var arr []GlobalVar
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, v := range arr {
			result[v.Key] = v.Value
		}
		return result
	}

	// Try object format: {"k":"v", ...}
	var obj map[string]string
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}

	// Try object format with interface values: {"k":"v", ...}
	var objIface map[string]any
	if err := json.Unmarshal(raw, &objIface); err == nil {
		for k, v := range objIface {
			if s, ok := v.(string); ok {
				result[k] = s
			} else {
				result[k] = fmt.Sprintf("%v", v)
			}
		}
		return result
	}

	return result
}

// RenderTemplate replaces {{key}} placeholders in the template string with values from vars.
func RenderTemplate(tmpl string, vars map[string]string) string {
	if tmpl == "" || len(vars) == 0 {
		return tmpl
	}

	replacements := make([]string, 0, len(vars)*2)
	for key, value := range vars {
		replacements = append(replacements, "{{"+key+"}}", value)
	}
	replacer := strings.NewReplacer(replacements...)
	return replacer.Replace(tmpl)
}

// GetConfigAccessToken returns the current user's access token for use as apikey.
func GetConfigAccessToken() (string, error) {
	return resolveAccessToken()
}
