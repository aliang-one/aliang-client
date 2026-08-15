package runner

import (
	"os"
	"strings"
	"sync"
	"time"
)

var TunSignal = make(chan os.Signal, 1)
var RunStatusChan = make(chan map[string]string, 1)

type StartupProgress struct {
	Active             bool     `json:"active"`
	Status             string   `json:"status"`
	Phase              string   `json:"phase"`
	Progress           int      `json:"progress_percent"`
	Message            string   `json:"message"`
	Error              string   `json:"error,omitempty"`
	Errors             []string `json:"errors,omitempty"`
	RetryCount         int      `json:"retry_count,omitempty"`
	MaxRetries         int      `json:"max_retries,omitempty"`
	PermissionRequired bool     `json:"permission_required"`
	UpdatedAt          int64    `json:"updated_at"`
}

var (
	startupProgressMu sync.RWMutex
	startupProgress   = StartupProgress{
		Active:    false,
		Status:    "idle",
		Phase:     "idle",
		Progress:  0,
		Message:   "TUN startup has not started.",
		Errors:    nil,
		UpdatedAt: time.Now().Unix(),
	}
)

func ResetStartupProgress() {
	setStartupProgress(StartupProgress{
		Active:    false,
		Status:    "idle",
		Phase:     "idle",
		Progress:  0,
		Message:   "TUN startup has not started.",
		Errors:    nil,
		UpdatedAt: time.Now().Unix(),
	})
}

func UpdateStartupProgress(status string, phase string, progress int, message string, errMsg string, permissionRequired bool) {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	startupProgressMu.Lock()
	defer startupProgressMu.Unlock()
	startupProgress.Active = status == "starting"
	startupProgress.Status = status
	startupProgress.Phase = phase
	startupProgress.Progress = progress
	startupProgress.Message = message
	startupProgress.Error = errMsg
	startupProgress.PermissionRequired = permissionRequired
	startupProgress.UpdatedAt = time.Now().Unix()
}

func FailStartupProgress(phase string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
		AppendStartupError(message)
	}
	UpdateStartupProgress("failed", phase, 100, "TUN startup failed.", message, isPermissionLikeError(message))
}

func CompleteStartupProgress(message string) {
	UpdateStartupProgress("success", "running", 100, message, "", false)
}

func GetStartupProgress() StartupProgress {
	startupProgressMu.RLock()
	defer startupProgressMu.RUnlock()
	return startupProgress
}

func setStartupProgress(progress StartupProgress) {
	startupProgressMu.Lock()
	defer startupProgressMu.Unlock()
	startupProgress = progress
}

func AppendStartupError(message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	startupProgressMu.Lock()
	defer startupProgressMu.Unlock()
	if len(startupProgress.Errors) > 0 && startupProgress.Errors[len(startupProgress.Errors)-1] == message {
		return
	}
	startupProgress.Errors = append(startupProgress.Errors, message)
}

func SetStartupRetryInfo(current int, max int) {
	startupProgressMu.Lock()
	defer startupProgressMu.Unlock()
	startupProgress.RetryCount = current
	startupProgress.MaxRetries = max
	startupProgress.UpdatedAt = time.Now().Unix()
}

func isPermissionLikeError(message string) bool {
	lowered := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lowered, "access is denied") ||
		strings.Contains(lowered, "operation not permitted") ||
		strings.Contains(lowered, "administrator") ||
		strings.Contains(lowered, "elevation") ||
		strings.Contains(lowered, "runas")
}
