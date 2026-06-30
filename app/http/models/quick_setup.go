package models

type QuickSetupSoftwareFile struct {
	Code        string `json:"code"`
	Label       string `json:"label"`
	FileName    string `json:"file_name"`
	DefaultPath string `json:"default_path"`
	Format      string `json:"format"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

type QuickSetupSoftware struct {
	Code               string                   `json:"code"`
	Name               string                   `json:"name"`
	Description        string                   `json:"description"`
	SupportedProviders []string                 `json:"supported_providers"`
	Files              []QuickSetupSoftwareFile `json:"files"`
}

type QuickSetupAPIKey struct {
	ID              int64                `json:"id"`
	Key             string               `json:"key"`
	Name            string               `json:"name"`
	Provider        string               `json:"provider"`
	BaseURL         string               `json:"base_url,omitempty"`
	Status          string               `json:"status"`
	Masked          bool                 `json:"masked"`
	SecretAvailable bool                 `json:"secret_available"`
	Group           *APIKeyGroupResponse `json:"group,omitempty"`
}

type QuickSetupCatalogResponse struct {
	Softwares []QuickSetupSoftware `json:"softwares"`
	APIKeys   []QuickSetupAPIKey   `json:"api_keys"`
}

type QuickSetupModelsRequest struct {
	KeyID int64 `json:"key_id"`
}

type QuickSetupModel struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	Created int64  `json:"created,omitempty"`
}

type QuickSetupModelsResponse struct {
	KeyID    int64             `json:"key_id"`
	Provider string            `json:"provider"`
	BaseURL  string            `json:"base_url"`
	Models   []QuickSetupModel `json:"models"`
}

type QuickSetupRenderRequest struct {
	Software string              `json:"software"`
	KeyIDs   []int64             `json:"key_ids,omitempty"`
	OpenCode *OpenCodeRenderSpec `json:"opencode,omitempty"`
}

type OpenCodeRenderSpec struct {
	ModelKeyID    int64                  `json:"model_key_id,omitempty"`
	ModelProvider string                 `json:"model_provider,omitempty"`
	Model         string                 `json:"model,omitempty"`
	SmallModel    string                 `json:"small_model,omitempty"`
	BaseURL       string                 `json:"base_url,omitempty"`
	ProviderIDs   []int64                `json:"provider_ids,omitempty"`
	Providers     []OpenCodeProviderSpec `json:"providers,omitempty"`
}

type OpenCodeProviderSpec struct {
	KeyID   int64  `json:"key_id,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

type QuickSetupPreviewFile struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Format  string `json:"format"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

type QuickSetupVariant struct {
	Software string                  `json:"software"`
	Label    string                  `json:"label"`
	Provider string                  `json:"provider"`
	APIKey   QuickSetupAPIKey        `json:"api_key"`
	APIKeys  []QuickSetupAPIKey      `json:"api_keys,omitempty"`
	Files    []QuickSetupPreviewFile `json:"files"`
	Notes    []string                `json:"notes,omitempty"`
}

type QuickSetupRenderResponse struct {
	Software string              `json:"software"`
	Variants []QuickSetupVariant `json:"variants"`
}

type QuickSetupApplyFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Format  string `json:"format,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

type QuickSetupApplyRequest struct {
	Software string                `json:"software"`
	Files    []QuickSetupApplyFile `json:"files"`
}

type QuickSetupApplyResponse struct {
	Software string   `json:"software"`
	Written  []string `json:"written"`
}
