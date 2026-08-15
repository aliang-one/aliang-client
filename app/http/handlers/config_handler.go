package handlers

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/routing"
)

// ConfigHandler provides a local (non-Nacos) routing-config compatibility API.
type ConfigHandler struct {
	customerConfigService *services.CustomerConfigService
	coreConfigService     *services.CoreConfigService
}

func NewConfigHandler() *ConfigHandler {
	return &ConfigHandler{
		customerConfigService: services.NewCustomerConfigService(),
		coreConfigService:     services.NewCoreConfigService(),
	}
}

func (h *ConfigHandler) HandleRoutingConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetRoutingConfig(w)
	case http.MethodPost:
		h.handleUpdateRoutingConfig(w, r)
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}

func (h *ConfigHandler) HandleToggleRuleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}

	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/config/routing/rules/"), "/")
	if len(pathParts) < 2 {
		common.ErrorBadRequest(w, "Invalid URL format", nil)
		return
	}
	ruleID := pathParts[0]
	common.ErrorBadRequest(w, "Legacy mutable rule toggle write path is removed; update canonical routing config via /api/config/routing", map[string]interface{}{"rule_id": ruleID})
}

func (h *ConfigHandler) HandleAutoUpdateStatus(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		common.Success(w, map[string]interface{}{
			"auto_update": false,
			"source":      "canonical",
			"updated_at":  "",
		})
	case http.MethodPut:
		// Compatibility endpoint: auto-update is no longer supported in simplified mode.
		common.Success(w, map[string]interface{}{
			"message":     "Auto-update is disabled in simplified mode",
			"auto_update": false,
		})
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}

func (h *ConfigHandler) HandleCustomerConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetCustomerConfig(w)
	case http.MethodPost, http.MethodPut:
		h.handleUpdateCustomerConfig(w, r)
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}

func (h *ConfigHandler) HandlePresetAIRuleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	common.Success(w, map[string]interface{}{
		"providers": h.customerConfigService.GetPresetAIRuleProviders(),
	})
}

func (h *ConfigHandler) HandleCoreConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGetCoreConfig(w)
	case http.MethodPost, http.MethodPut:
		h.handleUpdateCoreConfig(w, r)
	default:
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
	}
}

func (h *ConfigHandler) handleGetRoutingConfig(w http.ResponseWriter) {
	common.Success(w, activeCanonicalRoutingConfig())
}

func (h *ConfigHandler) handleUpdateRoutingConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := ioReadAll(r)
	if err != nil {
		common.ErrorBadRequest(w, "Invalid JSON format", map[string]interface{}{"error": err.Error()})
		return
	}

	applyResult, err := config.GetRoutingApplyStore().Apply(raw, func(canonical *config.CanonicalRoutingSchema) (any, error) {
		return routing.CompileRuntimeSnapshot(canonical)
	})
	if err != nil {
		common.ErrorBadRequest(w, "Configuration validation failed", map[string]interface{}{"error": err.Error()})
		return
	}

	logger.Debug("Routing configuration updated (canonical apply store)")
	common.Success(w, map[string]interface{}{
		"message": "Configuration updated successfully",
		"source":  "canonical",
		"version": applyResult.Version,
		"hash":    applyResult.Hash,
	})
}

func (h *ConfigHandler) handleGetCustomerConfig(w http.ResponseWriter) {
	if h.customerConfigService == nil {
		common.ErrorInternalServer(w, "Customer config service is not initialized", nil)
		return
	}

	customer, version, err := h.customerConfigService.GetCommittedCustomerConfig()
	if err != nil {
		common.ErrorInternalServer(w, "Failed to get customer config", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{
		"customer": customer,
		"version":  version,
		"source":   "committed",
	})
}

func (h *ConfigHandler) handleUpdateCustomerConfig(w http.ResponseWriter, r *http.Request) {
	if h.customerConfigService == nil {
		common.ErrorInternalServer(w, "Customer config service is not initialized", nil)
		return
	}

	raw, err := ioReadAll(r)
	if err != nil {
		common.ErrorBadRequest(w, "Invalid JSON format", map[string]interface{}{"error": err.Error()})
		return
	}

	result, err := h.customerConfigService.UpdateCommittedCustomerConfig(raw)
	if err != nil {
		if services.IsCustomerConfigValidationError(err) {
			common.ErrorBadRequest(w, "Configuration validation failed", map[string]interface{}{"error": err.Error()})
			return
		}
		common.ErrorInternalServer(w, "Failed to update customer config", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{
		"message":  "Customer config updated successfully",
		"customer": result.Customer,
		"version":  result.Version,
		"source":   "committed",
	})
}

func (h *ConfigHandler) handleGetCoreConfig(w http.ResponseWriter) {
	if h.coreConfigService == nil {
		common.ErrorInternalServer(w, "Core config service is not initialized", nil)
		return
	}

	core, version, err := h.coreConfigService.GetCommittedCoreConfig()
	if err != nil {
		common.ErrorInternalServer(w, "Failed to get core config", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{
		"core":    core,
		"version": version,
		"source":  "committed",
	})
}

func (h *ConfigHandler) handleUpdateCoreConfig(w http.ResponseWriter, r *http.Request) {
	if h.coreConfigService == nil {
		common.ErrorInternalServer(w, "Core config service is not initialized", nil)
		return
	}

	raw, err := ioReadAll(r)
	if err != nil {
		common.ErrorBadRequest(w, "Invalid JSON format", map[string]interface{}{"error": err.Error()})
		return
	}

	result, err := h.coreConfigService.UpdateCommittedCoreConfig(raw)
	if err != nil {
		if services.IsCoreConfigValidationError(err) {
			common.ErrorBadRequest(w, "Configuration validation failed", map[string]interface{}{"error": err.Error()})
			return
		}
		common.ErrorInternalServer(w, "Failed to update core config", map[string]interface{}{"error": err.Error()})
		return
	}

	common.Success(w, map[string]interface{}{
		"message": "Core config updated successfully",
		"core":    result.Core,
		"version": result.Version,
		"source":  "committed",
	})
}

func ioReadAll(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, fmt.Errorf("request body is empty")
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func activeCanonicalRoutingConfig() *config.CanonicalRoutingSchema {
	canonical := config.GetRoutingApplyStore().ActiveCanonicalSchema()
	if canonical == nil {
		return &config.CanonicalRoutingSchema{
			Version: config.CanonicalRoutingSchemaVersion,
			Ingress: config.CanonicalIngressConfig{Mode: "tun"},
			Egress: config.CanonicalEgressConfig{
				Direct:   config.CanonicalEgressBranch{Enabled: true},
				ToAliang: config.CanonicalEgressBranch{Enabled: false},
				ToSocks:  config.CanonicalSocksEgressBranch{Enabled: false, Upstream: config.CanonicalSocksUpstream{Type: "socks"}},
			},
			Routing: config.CanonicalRoutingConfig{Rules: []config.CanonicalRoutingRule{}},
		}
	}
	return canonical
}
