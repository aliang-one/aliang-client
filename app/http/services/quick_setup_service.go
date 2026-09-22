package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
)

type QuickSetupService struct{}

type quickSetupPreparedFile struct {
	code    string
	path    string
	content string
}

type quickSetupFileBackup struct {
	existed bool
	content []byte
}

type quickSetupTargetUser struct {
	homeDir     string
	uid         int
	gid         int
	adjustOwner bool
}

type quickSetupOpenCodeConfig struct {
	Providers  map[string]json.RawMessage `json:"provider"`
	Model      string                     `json:"model"`
	SmallModel string                     `json:"small_model,omitempty"`
}

type quickSetupOpenCodeProvider struct {
	NPM     string                            `json:"npm"`
	Options quickSetupOpenCodeProviderOptions `json:"options"`
	Models  map[string]json.RawMessage        `json:"models"`
}

type quickSetupOpenCodeProviderOptions struct {
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseURL"`
}

const (
	quickSetupMaxApplyFiles     = 16
	quickSetupMaxApplyFileBytes = 1 << 20
	quickSetupControlPlaneHost  = "backend.aliang.one"
	quickSetupInferenceHost     = "api.aliang.one"
)

var ErrQuickSetupUnauthenticated = errors.New("authenticated session is required")

var quickSetupGetAPIKeysFn = auth.GetUserAPIKeys
var quickSetupAuthorizationHeaderFn = auth.GetCurrentAuthorizationHeader
var quickSetupModelsHTTPClient = &http.Client{Timeout: 12 * time.Second}
var quickSetupWriteConfigFileFn = writeConfigFile
var quickSetupTargetUserFn = resolveQuickSetupTargetUser
var quickSetupAdjustOwnershipFn = adjustQuickSetupOwnership
var quickSetupApplyMu sync.Mutex

func NewQuickSetupService() *QuickSetupService {
	return &QuickSetupService{}
}

func (s *QuickSetupService) Catalog() map[string]interface{} {
	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		if isSessionMissingError(err) {
			return map[string]interface{}{
				"status": "unauthenticated",
				"error":  "session_missing",
				"msg":    "No authenticated session found",
			}
		}
		return map[string]interface{}{
			"status": "failed",
			"error":  "quick_setup_catalog_failed",
			"msg":    fmt.Sprintf("Failed to load quick setup catalog: %v", err),
		}
	}

	baseRoot, err := quickSetupBaseURL()
	if err != nil {
		return map[string]interface{}{
			"status": "failed",
			"error":  "quick_setup_catalog_failed",
			"msg":    fmt.Sprintf("Failed to load quick setup catalog: %v", err),
		}
	}
	softwares := quickSetupSoftwares()
	homeDir := quickSetupDetectionHomeFn()
	for i := range softwares {
		softwares[i].Installed = detectQuickSetupInstalled(softwares[i].Code, homeDir)
	}
	return map[string]interface{}{
		"status": "success",
		"data": models.QuickSetupCatalogResponse{
			Softwares: softwares,
			APIKeys:   toQuickSetupAPIKeys(apiKeys, baseRoot),
		},
	}
}

func (s *QuickSetupService) Models(req models.QuickSetupModelsRequest) (*models.QuickSetupModelsResponse, error) {
	if req.KeyID == 0 {
		return nil, errors.New("key_id is required")
	}

	baseRoot, err := quickSetupBaseURL()
	if err != nil {
		return nil, err
	}

	apiKeys, err := quickSetupGetAPIKeysFn()
	if err != nil {
		if isSessionMissingError(err) {
			return nil, ErrQuickSetupUnauthenticated
		}
		return nil, err
	}

	keys := toQuickSetupAPIKeys(apiKeys, baseRoot)
	var selected *models.QuickSetupAPIKey
	for i := range keys {
		if keys[i].ID == req.KeyID {
			selected = &keys[i]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("selected API key id is not valid: %d", req.KeyID)
	}
	if !quickSetupAPIKeyHasPlainSecret(*selected) {
		return nil, errors.New("plaintext secret is required for selected API key")
	}

	modelListBaseURL := quickSetupModelListBaseURL(baseRoot)
	modelsList, err := fetchQuickSetupModels(modelListBaseURL, selected.Key)
	if err != nil {
		return nil, err
	}

	return &models.QuickSetupModelsResponse{
		KeyID:    selected.ID,
		Provider: selected.Provider,
		BaseURL:  modelListBaseURL,
		Models:   modelsList,
	}, nil
}
