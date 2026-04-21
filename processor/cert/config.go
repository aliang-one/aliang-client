package cert

// CertConfig holds configuration for a certificate
type CertConfig struct {
	CN               string // Common Name
	Issuer           string // Issuer/颁发机构
	Country          string // Country
	Organization     string // Organization/Company
	OrganizationUnit string // Organizational Unit
	ValidityYears    int    // Validity period in years
	KeySize          int    // RSA key size (2048, 4096, etc.)
	FileName         string // File name for export (without extension)
	CertType         string // Unique identifier for this certificate type
}

// Certificate type constants
const (
	CertTypeMitmCA     = "mitm-ca"
	CertTypeMtlsClient = "mtls-cert"
)

// MitmCAConfig is the configuration for MITM CA certificate
var MitmCAConfig = CertConfig{
	CN:               "aliang",
	Issuer:           "aliang.com",
	Country:          "US",
	Organization:     "Subtraffic Inc",
	OrganizationUnit: "Security",
	ValidityYears:    10,
	KeySize:          2048,
	FileName:         "mitm-ca",
	CertType:         CertTypeMitmCA,
}

// MtlsClientConfig is the configuration for mTLS Client certificate
var MtlsClientConfig = CertConfig{
	CN:               "aliang",
	Issuer:           "aliang.com",
	Country:          "US",
	Organization:     "Subtraffic Inc",
	OrganizationUnit: "Security",
	ValidityYears:    10,
	KeySize:          2048,
	FileName:         "mtls-client",
	CertType:         CertTypeMtlsClient,
}

// GetCertConfig returns the configuration for a certificate type
func GetCertConfig(certType string) *CertConfig {
	switch certType {
	case CertTypeMitmCA:
		return &MitmCAConfig
	case CertTypeMtlsClient:
		return &MtlsClientConfig
	default:
		return nil
	}
}

// AllCertTypes returns all certificate types
func AllCertTypes() []string {
	return []string{
		CertTypeMitmCA,
		CertTypeMtlsClient,
	}
}
