package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/cache"
	cert_config "aliang.one/nursorgate/processor/cert"
	client_cert "aliang.one/nursorgate/processor/cert/client"
	cert_generator "aliang.one/nursorgate/processor/cert/generator"
	cert_installer "aliang.one/nursorgate/processor/cert/installer"
)

type handlerImportInstaller struct{}

func (handlerImportInstaller) IsInstalled(string, []byte) (bool, error) { return true, nil }
func (handlerImportInstaller) Install(string, string) error             { return nil }
func (handlerImportInstaller) Remove(string, []byte) error              { return nil }
func (handlerImportInstaller) GetCertInfo(string, []byte) (cert_installer.CertInfo, error) {
	return cert_installer.CertInfo{}, nil
}
func (handlerImportInstaller) GetInstallPath(string) string           { return "" }
func (handlerImportInstaller) IsTrusted(string, []byte) (bool, error) { return true, nil }
func (handlerImportInstaller) GetTrustStatus(string, []byte) (string, error) {
	return "system_trusted", nil
}

type handlerFailInstallInstaller struct{ handlerImportInstaller }

func (handlerFailInstallInstaller) IsInstalled(string, []byte) (bool, error) { return false, nil }
func (handlerFailInstallInstaller) IsTrusted(string, []byte) (bool, error)   { return false, nil }
func (handlerFailInstallInstaller) Install(string, string) error {
	return errors.New("injected install failure")
}
func (handlerFailInstallInstaller) GetTrustStatus(string, []byte) (string, error) {
	return "not_found", nil
}

func TestCertificateImportRejectsRemoteRequests(t *testing.T) {
	handler := &CertHandler{}
	request := httptest.NewRequest(http.MethodPost, "/api/cert/import", strings.NewReader(""))
	request.RemoteAddr = "192.0.2.10:41234"
	recorder := httptest.NewRecorder()
	handler.HandleImport(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestCertificateInstallRejectsRemoteRequests(t *testing.T) {
	handler := &CertHandler{}
	request := httptest.NewRequest(http.MethodPost, "/api/cert/install", strings.NewReader(`{"cert_type":"mitm-ca"}`))
	request.RemoteAddr = "192.0.2.10:41234"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.HandleInstall(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestCertificateImportRejectsCrossSiteOrigin(t *testing.T) {
	handler := &CertHandler{}
	request := httptest.NewRequest(http.MethodPost, "/api/cert/import", strings.NewReader(""))
	request.RemoteAddr = "127.0.0.1:41234"
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set("X-Aliang-Local-Request", "1")
	recorder := httptest.NewRecorder()
	handler.HandleImport(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestCertificateImportAllowsLocalViteOrigin(t *testing.T) {
	handler := &CertHandler{}
	request := httptest.NewRequest(http.MethodPost, "/api/cert/import", strings.NewReader(""))
	request.RemoteAddr = "127.0.0.1:41234"
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("X-Aliang-Local-Request", "1")
	recorder := httptest.NewRecorder()
	handler.HandleImport(recorder, request)
	if recorder.Code == http.StatusForbidden {
		t.Fatalf("local Vite origin should pass local security gate; body=%s", recorder.Body.String())
	}
}

func TestCertificateImportValidatesAndActivatesPEMPair(t *testing.T) {
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	fixturePath := filepath.Join(t.TempDir(), "custom-ca.pem")
	if err := cert_generator.GenerateCertificateFromConfig(&cert_config.CertConfig{
		CN: "handler-import-root", Issuer: "handler-import-root", Country: "US",
		Organization: "Aliang Test", ValidityYears: 1, KeySize: 2048,
		FileName: "custom-ca", CertType: cert_config.CertTypeMitmCA,
	}, fixturePath); err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(fixturePath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewCertHandler(services.NewCertServiceWithInstaller(handlerImportInstaller{}))

	validateResponse := performCertificateUpload(t, handler.HandleValidateImport, certPEM, keyPEM)
	if validateResponse.Code != http.StatusOK {
		t.Fatalf("validate status = %d body=%s", validateResponse.Code, validateResponse.Body.String())
	}
	importResponse := performCertificateUpload(t, handler.HandleImport, certPEM, keyPEM)
	if importResponse.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", importResponse.Code, importResponse.Body.String())
	}
	var payload struct {
		Data struct {
			Source  string `json:"source"`
			Subject string `json:"subject"`
		} `json:"data"`
	}
	if err := json.Unmarshal(importResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Source != "imported" || !strings.Contains(payload.Data.Subject, "handler-import-root") {
		t.Fatalf("unexpected import payload: %s", importResponse.Body.String())
	}
}

func TestCertificateGenerateReportsInstallStage(t *testing.T) {
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert_generator.GenerateCertificateFromConfig(&cert_config.MitmCAConfig, certPath); err != nil {
		t.Fatal(err)
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		t.Fatal(err)
	}

	handler := NewCertHandler(services.NewCertServiceWithInstaller(handlerFailInstallInstaller{}))
	request := httptest.NewRequest(http.MethodPost, "/api/cert/generate", strings.NewReader(`{"cert_type":"mitm-ca"}`))
	request.RemoteAddr = "127.0.0.1:41234"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Aliang-Local-Request", "1")
	recorder := httptest.NewRecorder()
	handler.HandleGenerateCert(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Details struct {
				Stage string `json:"stage"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Details.Stage != "install" {
		t.Fatalf("stage = %q, want install; body=%s", payload.Data.Details.Stage, recorder.Body.String())
	}
}

func performCertificateUpload(t *testing.T, handler http.HandlerFunc, certPEM, keyPEM []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	certPart, err := writer.CreateFormFile("certificate", "ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = certPart.Write(certPEM)
	keyPart, err := writer.CreateFormFile("private_key", "ca.key")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = keyPart.Write(keyPEM)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/cert/import", &body)
	request.RemoteAddr = "127.0.0.1:41234"
	request.Host = "127.0.0.1:56431"
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("X-Aliang-Local-Request", "1")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}
