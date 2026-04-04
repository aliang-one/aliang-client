package services

import (
	"encoding/json"
	"fmt"
	"strings"

	auth "aliang.one/nursorgate/processor/auth"
)

type ConfigCenterService struct{}

var (
	getConfigByTagFn  = auth.GetConfigByTag
	getGlobalVarsFn   = auth.GetGlobalVars
	parseGlobalVarsFn = auth.ParseGlobalVars
	renderTemplateFn  = auth.RenderTemplate
	getAccessTokenFn  = auth.GetConfigAccessToken
)

func NewConfigCenterService() *ConfigCenterService {
	return &ConfigCenterService{}
}

// GetRenderedConfig fetches a config template by tag, collects global variables
// and the user's apikey, renders the template, and returns the result.
func (s *ConfigCenterService) GetRenderedConfig(tag string) map[string]interface{} {
	if strings.TrimSpace(tag) == "" {
		return map[string]interface{}{
			"status": "failed",
			"error": "tag_required",
			"msg":   "Tag parameter is required",
		}
	}

	// 1. Fetch config by tag from backend
	configRaw, err := getConfigByTagFn(tag)
	if err != nil {
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":   "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "config_fetch_failed",
			"msg":   fmt.Sprintf("Failed to fetch config: %v", err),
		}
	}

	// 2. Parse the config data to extract template content
	var configData map[string]json.RawMessage
	if err := json.Unmarshal(configRaw, &configData); err != nil {
		return map[string]interface{}{
			"status": "failed",
			"error":  "config_parse_failed",
			"msg":   fmt.Sprintf("Failed to parse config data: %v", err),
		}
	}

	// Extract template field - could be nested under different paths
	templateRaw := extractTemplate(configData)
	if templateRaw == nil {
		// No template field — return raw config data as-is
		return map[string]interface{}{
			"status": "success",
			"data":   configData,
		}
	}

	templateContent := string(templateRaw)

	if templateContent == "" {
		return map[string]interface{}{
			"status": "success",
			"data":   configData,
		}
	}

	// Strip surrounding quotes if present
	templateContent = strings.Trim(templateContent, `"`)

	// 3. Build variable map: global vars + apikey
	vars := make(map[string]string)

	// Fetch global variables (best-effort, don't fail if unavailable)
	globalVarsRaw, gErr := getGlobalVarsFn()
	if gErr == nil && len(globalVarsRaw) > 0 {
		globalVars := parseGlobalVarsFn(globalVarsRaw)
		for k, v := range globalVars {
			vars[k] = v
		}
	}
	// Fetch user's access token as apikey
	accessToken, aErr := getAccessTokenFn()
	if aErr == nil && accessToken != "" {
		vars["apikey"] = accessToken
	}

	// 4. Render template
	rendered := renderTemplateFn(templateContent, vars)

	// 5. Build response with rendered template replacing original
	result := make(map[string]interface{})
	for k, v := range configData {
		if k == "template" {
			result["template"] = rendered
		} else {
			result[k] = v
		}
	}
	return map[string]interface{}{
		"status": "success",
		"data":   result,
	}
}

// extractTemplate recursively searches for a "template" field in the config data map.
func extractTemplate(data map[string]json.RawMessage) json.RawMessage {
	// Direct "template" key
	if t, exists := data["template"]; exists {
		return t
	}
	// Look inside a nested "data" object
	if d, exists := data["data"]; exists {
		var inner map[string]json.RawMessage
		if json.Unmarshal(d, &inner) == nil {
			if t, exists := inner["template"]; exists {
				return t
			}
		}
	}
	return nil
}
