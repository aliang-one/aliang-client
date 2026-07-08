package handlers

import (
	"fmt"
	"net/http"
	"path/filepath"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
	client_cert "aliang.one/nursorgate/processor/cert/client"
	"aliang.one/nursorgate/processor/cert/generator"
)

// CertHandler handles certificate management endpoints
type CertHandler struct {
	certService *services.CertService
}

// NewCertHandler creates a new certificate handler
func NewCertHandler(certService *services.CertService) *CertHandler {
	return &CertHandler{
		certService: certService,
	}
}

// HandleGetStatus returns the status of a certificate
func (ch *CertHandler) HandleGetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	// Get certificate type from query parameter
	certType := r.URL.Query().Get("cert_type")
	if certType == "" {
		// Try to parse from POST body
		var req struct {
			CertType string `json:"cert_type"`
		}
		if err := common.DecodeRequest(r, &req); err == nil {
			certType = req.CertType
		}
	}

	if certType == "" {
		certType = cert_config.CertTypeMitmCA
	}

	// Get certificate status
	status, err := ch.certService.GetCertStatus(certType)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to get certificate status: %v", err))
		common.Error(w, common.CodeInternalServer, "Failed to get certificate status", nil)
		return
	}

	common.Success(w, status)
}

// HandleExport exports a certificate to ~/.aliang/
func (ch *CertHandler) HandleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	var req struct {
		CertType string `json:"cert_type"`
	}

	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.CertType == "" {
		common.ErrorBadRequest(w, "Missing cert_type", nil)
		return
	}

	// Export certificate
	exportPath, err := ch.certService.ExportCert(req.CertType)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to export certificate: %v", err))
		common.Error(w, common.CodeInternalServer, fmt.Sprintf("Failed to export certificate: %s", err.Error()), nil)
		return
	}

	common.Success(w, map[string]interface{}{
		"cert_type":   req.CertType,
		"export_path": exportPath,
	})
}

// HandleDownload downloads a certificate file
func (ch *CertHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	certType := r.URL.Query().Get("cert_type")
	if certType == "" {
		common.ErrorBadRequest(w, "Missing cert_type parameter", nil)
		return
	}

	// Get certificate bytes
	certBytes, err := ch.certService.DownloadCert(certType)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to download certificate: %v", err))
		common.Error(w, common.CodeInternalServer, "Failed to download certificate", nil)
		return
	}

	// Determine filename based on certificate type
	filename := ""
	config := cert_config.GetCertConfig(certType)
	if config != nil {
		filename = config.FileName + ".pem"
	} else {
		filename = certType + ".pem"
	}

	// Set response headers for file download
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(certBytes)))

	// Write certificate bytes
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(certBytes)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to write certificate to response: %v", err))
	}
}

// HandleInstall installs a certificate to the system trust store
func (ch *CertHandler) HandleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	var req struct {
		CertType string `json:"cert_type"`
	}

	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.CertType == "" {
		common.ErrorBadRequest(w, "Missing cert_type", nil)
		return
	}

	// Install certificate
	if err := ch.certService.InstallCert(req.CertType); err != nil {
		logger.Error(fmt.Sprintf("Failed to install certificate: %v", err))
		common.Error(w, common.CodeInternalServer, fmt.Sprintf("Failed to install certificate: %s", err.Error()), nil)
		return
	}

	common.Success(w, map[string]interface{}{
		"cert_type": req.CertType,
		"message":   "Certificate trust flow opened successfully",
	})
}

// HandleRemove removes a certificate from the system trust store
func (ch *CertHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	var req struct {
		CertType string `json:"cert_type"`
	}

	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.CertType == "" {
		common.ErrorBadRequest(w, "Missing cert_type", nil)
		return
	}

	// Remove certificate
	if err := ch.certService.RemoveCert(req.CertType); err != nil {
		logger.Error(fmt.Sprintf("Failed to remove certificate: %v", err))
		common.Error(w, common.CodeInternalServer, fmt.Sprintf("Failed to remove certificate: %s", err.Error()), nil)
		return
	}

	common.Success(w, map[string]interface{}{
		"cert_type": req.CertType,
		"message":   "Certificate removed successfully",
	})
}

// HandleGetInfo returns certificate information
func (ch *CertHandler) HandleGetInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	// Get system info
	sysInfo, err := ch.certService.GetSystemInfo()
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to get system info: %v", err))
		common.Error(w, common.CodeInternalServer, "Failed to get system info", nil)
		return
	}

	// Get status for all certificate types
	certTypes := cert_config.AllCertTypes()
	statuses := make(map[string]interface{})

	for _, certType := range certTypes {
		status, _ := ch.certService.GetCertStatus(certType)
		statuses[certType] = status
	}

	common.Success(w, map[string]interface{}{
		"system_info":  sysInfo,
		"certificates": statuses,
	})
}

// HandleGenerateCert generates a new certificate
func (ch *CertHandler) HandleGenerateCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}

	var req struct {
		CertType string `json:"cert_type"`
	}

	if err := common.DecodeRequest(r, &req); err != nil {
		common.ErrorBadRequest(w, "Invalid request body", nil)
		return
	}

	if req.CertType == "" {
		common.ErrorBadRequest(w, "Missing cert_type", nil)
		return
	}

	// Get certificate configuration
	config := cert_config.GetCertConfig(req.CertType)
	if config == nil {
		common.ErrorBadRequest(w, fmt.Sprintf("Unknown certificate type: %s", req.CertType), nil)
		return
	}

	// Determine export path
	certDir, err := cache.GetCacheDir()
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to resolve certificate directory: %v", err))
		common.Error(w, common.CodeInternalServer, "Failed to resolve certificate directory", nil)
		return
	}

	exportPath := filepath.Join(certDir, config.FileName+".pem")

	// Generate certificate
	if err := generator.GenerateCertificateFromConfig(config, exportPath); err != nil {
		logger.Error(fmt.Sprintf("Failed to generate certificate: %v", err))
		common.Error(w, common.CodeInternalServer, fmt.Sprintf("Failed to generate certificate: %s", err.Error()), nil)
		return
	}

	// 若生成的是 MITM CA，立即刷新 CAManager（文件已变，让内存追随 + 清域名证书缓存）。
	if config.CertType == cert_config.CertTypeMitmCA {
		if err := client_cert.DefaultCAManager().Reload(); err != nil {
			logger.Warn(fmt.Sprintf("CAManager reload after generating mitm-ca failed: %v", err))
		}
	}

	common.Success(w, map[string]interface{}{
		"cert_type":   req.CertType,
		"message":     "Certificate generated successfully",
		"cert_path":   exportPath,
		"key_path":    exportPath + ".key",
		"cn":          config.CN,
		"issuer":      config.Issuer,
		"valid_years": config.ValidityYears,
	})
}
