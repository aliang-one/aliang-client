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

// QuickSetupSoftwarePresets 是按 agent 下发的 base_url 预设：local 指向本机
// HTTP 代理监听地址，public 指向推理网关公网入口。/v1 属变量值口径——
// codex/opencode 的预设带 /v1，claude-code/pi 不带（客户端自行追加）。
type QuickSetupSoftwarePresets struct {
	BaseURLLocal  string `json:"base_url_local"`
	BaseURLPublic string `json:"base_url_public"`
}

type QuickSetupSoftware struct {
	Code               string                     `json:"code"`
	Name               string                     `json:"name"`
	Description        string                     `json:"description"`
	SupportedProviders []string                   `json:"supported_providers"`
	Files              []QuickSetupSoftwareFile   `json:"files"`
	Installed          bool                       `json:"installed"`
	Presets            *QuickSetupSoftwarePresets `json:"presets,omitempty"`
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
	Softwares []QuickSetupSoftware  `json:"softwares"`
	APIKeys   []QuickSetupAPIKey    `json:"api_keys"`
	Combos    []QuickSetupComboView `json:"combos"`
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
	Software string                 `json:"software"`
	Written  []string               `json:"written"`
	Backups  []QuickSetupBackupInfo `json:"backups,omitempty"`
}

type QuickSetupBackupInfo struct {
	OriginalPath  string `json:"original_path"`
	BackupPath    string `json:"backup_path,omitempty"`
	ExistedBefore bool   `json:"existed_before"`
}

type QuickSetupRestoreRequest struct {
	Software string `json:"software"`
}

// QuickSetupComboCreateRequest 是组合创建（三入口）请求体：
// source ∈ blank/copy/disk；copy 入口消费 copy_from_id；
// variables 仅 blank 入口消费（逐键覆盖预填值）。
type QuickSetupComboCreateRequest struct {
	Software   string                `json:"software"`
	Name       string                `json:"name"`
	Source     string                `json:"source"`
	CopyFromID int64                 `json:"copy_from_id,omitempty"`
	Variables  map[string]string     `json:"variables,omitempty"`
	Files      []QuickSetupComboFile `json:"files,omitempty"`
}

// QuickSetupComboUpdateRequest 是组合保存（部分更新）请求体：
// name/variables/files 均可选，nil/缺省表示不动该字段。
type QuickSetupComboUpdateRequest struct {
	Name      *string               `json:"name"`
	Variables map[string]string     `json:"variables"`
	Files     []QuickSetupComboFile `json:"files"`
}

type QuickSetupRestoreFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type QuickSetupRestoreResponse struct {
	Restored []string                   `json:"restored"`
	Deleted  []string                   `json:"deleted"`
	Failed   []QuickSetupRestoreFailure `json:"failed"`
}

type QuickSetupConfigStateFile struct {
	Code            string `json:"code"`
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	Size            int64  `json:"size,omitempty"`
	ModifiedAt      string `json:"modified_at,omitempty"`
	Format          string `json:"format"`
	Content         string `json:"content,omitempty"`
	ManagedByAliang bool   `json:"managed_by_aliang"`
}

type QuickSetupConfigStateBackup struct {
	OriginalPath string `json:"original_path"`
	BackupPath   string `json:"backup_path,omitempty"`
	BackedUpAt   string `json:"backed_up_at"`
	SHA256       string `json:"sha256,omitempty"`
	Kind         string `json:"kind"`
}

type QuickSetupConfigStateResponse struct {
	Software string                        `json:"software"`
	Files    []QuickSetupConfigStateFile   `json:"files"`
	Backups  []QuickSetupConfigStateBackup `json:"backups"`
}
