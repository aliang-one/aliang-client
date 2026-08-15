package client

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"fmt"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

//go:embed client.crt
var mtlsClientCertPEM []byte

//go:embed client.key
var mtlsClientKeyPEM []byte

//go:embed ca.pem
var mtlsCACertPEM []byte

var (
	mtlsMaterialOnce sync.Once
	mtlsMaterial     mtlsClientMaterial
	mtlsMaterialErr  error
)

type mtlsClientMaterial struct {
	cert   tls.Certificate
	rootCA *x509.CertPool
}

// GetMTLSClientTLSConfig returns the outbound TLS config used by the aliang mTLS connector.
// The certificate material is embedded from processor/cert/client so the runtime consistently
// uses the dedicated client-auth certificate rather than the MITM server certificate.
//
// When isHTTP2 is false, the config intentionally does not advertise any ALPN.
// This keeps the mTLS channel as a raw encrypted tunnel that can carry arbitrary
// upper-layer payloads, including both HTTP/1.1 and HTTP/2 bytes.
func GetMTLSClientTLSConfig(isHTTP2 bool, serverName string) (*tls.Config, error) {
	material, err := getMTLSClientMaterial()
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		RootCAs:      material.rootCA,
		Certificates: []tls.Certificate{material.cert},
		ServerName:   serverName,

		// We intentionally disable standard hostname verification because the upstream
		// uses a routing/SNI domain that may not match the certificate SANs. The actual
		// trust decision is enforced by VerifyConnection against our pinned CA.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign}
			}

			opts := x509.VerifyOptions{
				Roots:         material.rootCA,
				Intermediates: x509.NewCertPool(),
			}

			for _, cert := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(cert)
			}

			if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {
				logger.Error(fmt.Sprintf("mTLS verification failed for server %s: %v", serverName, err))
				return err
			}

			logger.Debug(fmt.Sprintf("mTLS verification succeeded for server %s", serverName))
			return nil
		},
	}

	if isHTTP2 {
		tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	}

	return tlsConfig, nil
}

func getMTLSClientMaterial() (*mtlsClientMaterial, error) {
	mtlsMaterialOnce.Do(func() {
		cert, err := tls.X509KeyPair(mtlsClientCertPEM, mtlsClientKeyPEM)
		if err != nil {
			mtlsMaterialErr = fmt.Errorf("load embedded mTLS client key pair: %w", err)
			return
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(mtlsCACertPEM) {
			mtlsMaterialErr = fmt.Errorf("load embedded mTLS CA certificate: append failed")
			return
		}

		mtlsMaterial = mtlsClientMaterial{
			cert:   cert,
			rootCA: caCertPool,
		}
	})

	if mtlsMaterialErr != nil {
		return nil, mtlsMaterialErr
	}
	return &mtlsMaterial, nil
}
