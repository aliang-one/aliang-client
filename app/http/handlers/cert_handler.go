package handlers

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
)

const maxCertificateImportBytes = 4 << 20

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
	if !allowLocalCertificateMutation(w, r) {
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
		"message":   "Certificate installed and trusted successfully",
	})
}

// HandleRemove removes a certificate from the system trust store
func (ch *CertHandler) HandleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}
	if !allowLocalCertificateMutation(w, r) {
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
	if !allowLocalCertificateMutation(w, r) {
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

	result, err := ch.certService.RegenerateCert(req.CertType)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to rotate certificate: %v", err))
		details := map[string]string{"error": err.Error()}
		if stage := services.CertificateOperationStage(err); stage != "" {
			details["stage"] = stage
		}
		common.Error(w, common.CodeInternalServer, "Failed to rotate certificate", details)
		return
	}
	common.Success(w, result)
}

func (ch *CertHandler) HandleValidateImport(w http.ResponseWriter, r *http.Request) {
	ch.handleMITMCAImport(w, r, true)
}

func (ch *CertHandler) HandleImport(w http.ResponseWriter, r *http.Request) {
	ch.handleMITMCAImport(w, r, false)
}

func (ch *CertHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}
	if !allowLocalCertificateMutation(w, r) {
		return
	}
	result, err := ch.certService.RollbackMITMCA()
	if err != nil {
		common.ErrorBadRequest(w, "Failed to rollback MITM CA", map[string]string{"error": err.Error()})
		return
	}
	common.Success(w, result)
}

func (ch *CertHandler) handleMITMCAImport(w http.ResponseWriter, r *http.Request, validateOnly bool) {
	if r.Method != http.MethodPost {
		common.ErrorBadRequest(w, "Method not allowed", nil)
		return
	}
	if !allowLocalCertificateMutation(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCertificateImportBytes)
	if err := r.ParseMultipartForm(maxCertificateImportBytes); err != nil {
		common.ErrorBadRequest(w, "Invalid or oversized certificate upload", nil)
		return
	}
	defer r.MultipartForm.RemoveAll()

	certificate, err := readOptionalUpload(r, "certificate")
	if err != nil {
		common.ErrorBadRequest(w, "Failed to read certificate file", nil)
		return
	}
	privateKey, err := readOptionalUpload(r, "private_key")
	if err != nil {
		common.ErrorBadRequest(w, "Failed to read private key file", nil)
		return
	}
	bundle, err := readOptionalUpload(r, "bundle")
	if err != nil {
		common.ErrorBadRequest(w, "Failed to read PKCS#12 bundle", nil)
		return
	}
	password := r.FormValue("password")

	var result services.CertImportResult
	if validateOnly {
		result, err = ch.certService.ValidateMITMCAImport(certificate, privateKey, bundle, password)
	} else {
		result, err = ch.certService.ImportMITMCA(certificate, privateKey, bundle, password)
	}
	for i := range privateKey {
		privateKey[i] = 0
	}
	for i := range bundle {
		bundle[i] = 0
	}
	if err != nil {
		common.ErrorBadRequest(w, "Invalid MITM CA certificate pair", map[string]string{"error": err.Error()})
		return
	}
	common.Success(w, result)
}

func readOptionalUpload(r *http.Request, field string) ([]byte, error) {
	file, _, err := r.FormFile(field)
	if err != nil {
		if err == http.ErrMissingFile {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxCertificateImportBytes+1))
}

func allowLocalCertificateMutation(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		common.ErrorForbidden(w, "Certificate private-key operations are only available from localhost")
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	originIP := net.ParseIP(parsed.Hostname())
	originIsLoopback := strings.EqualFold(parsed.Hostname(), "localhost") || (originIP != nil && originIP.IsLoopback())
	if err != nil || !originIsLoopback || r.Header.Get("X-Aliang-Local-Request") != "1" {
		common.ErrorForbidden(w, "Certificate operation failed same-origin validation")
		return false
	}
	return true
}
