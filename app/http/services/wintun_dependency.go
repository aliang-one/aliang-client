package services

const (
	wintunDownloadURL    = "https://www.wintun.net/builds/wintun-0.14.1.zip"
	wintunMirrorURLX86   = "https://nursor-1305838434.cos.ap-chengdu.myqcloud.com/static/wintun/bin/x86/wintun.dll"
	wintunMirrorURLAMD64 = "https://nursor-1305838434.cos.ap-chengdu.myqcloud.com/static/wintun/bin/amd64/wintun.dll"
	wintunMirrorURLARM64 = "https://nursor-1305838434.cos.ap-chengdu.myqcloud.com/static/wintun/bin/arm64/wintun.dll"
)

// WintunDependencyStatus describes the Windows Wintun dependency state that
// the frontend can poll while switching into TUN mode.
type WintunDependencyStatus struct {
	Supported    bool   `json:"supported"`
	Required     bool   `json:"required"`
	Available    bool   `json:"available"`
	Installing   bool   `json:"installing"`
	State        string `json:"state"`
	Progress     int    `json:"progress_percent,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Message      string `json:"message"`
	Error        string `json:"error,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	InstallPath  string `json:"install_path,omitempty"`
	TargetPath   string `json:"target_path,omitempty"`
	DownloadURL  string `json:"download_url,omitempty"`
	LastChecked  int64  `json:"last_checked,omitempty"`
	UpdatedAt    int64  `json:"updated_at,omitempty"`
}

type wintunDependencyController interface {
	Status() WintunDependencyStatus
	Refresh() WintunDependencyStatus
	StartInstall() WintunDependencyStatus
}

var sharedWintunDependencyController wintunDependencyController = newWintunDependencyController()

func getSharedWintunDependencyController() wintunDependencyController {
	return sharedWintunDependencyController
}

func GetWintunDependencyStatus() WintunDependencyStatus {
	return getSharedWintunDependencyController().Status()
}

func StartWintunDependencyInstall() WintunDependencyStatus {
	return getSharedWintunDependencyController().StartInstall()
}

func RefreshWintunDependencyStatus() WintunDependencyStatus {
	return getSharedWintunDependencyController().Refresh()
}

func setSharedWintunDependencyControllerForTest(controller wintunDependencyController) {
	if controller == nil {
		sharedWintunDependencyController = newWintunDependencyController()
		return
	}
	sharedWintunDependencyController = controller
}
