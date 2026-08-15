package repositories

import (
	"aliang.one/nursorgate/app/http/services"
)

// RunRepositoryImpl provides access to run mode functionality
type RunRepositoryImpl struct {
	runService *services.RunService
}

// NewRunRepository creates a new run repository instance
func NewRunRepository() *RunRepositoryImpl {
	return &RunRepositoryImpl{
		runService: services.GetSharedRunService(),
	}
}

// GetCurrentMode gets the current operating mode
func (rr *RunRepositoryImpl) GetCurrentMode() string {
	return rr.runService.GetCurrentMode()
}

// SetCurrentMode sets the operating mode
func (rr *RunRepositoryImpl) SetCurrentMode(mode string) {
	rr.runService.SetCurrentMode(mode)
}

// IsRunning checks if service is running
func (rr *RunRepositoryImpl) IsRunning() bool {
	return rr.runService.IsRunning()
}

// SetRunning sets the running state
func (rr *RunRepositoryImpl) SetRunning(running bool) {
	rr.runService.SetRunning(running)
}

// StartService starts the service for the current mode
func (rr *RunRepositoryImpl) StartService() map[string]interface{} {
	return rr.runService.StartService()
}

// StopService stops the current running service
func (rr *RunRepositoryImpl) StopService() map[string]interface{} {
	return rr.runService.StopService()
}

// GetStatus returns the current service status
func (rr *RunRepositoryImpl) GetStatus() map[string]interface{} {
	return rr.runService.GetStatus()
}

// SwitchMode switches the operating mode
func (rr *RunRepositoryImpl) SwitchMode(targetMode string) map[string]interface{} {
	return rr.runService.SwitchMode(targetMode)
}
