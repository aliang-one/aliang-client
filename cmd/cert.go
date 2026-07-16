package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"aliang.one/nursorgate/app/http/services"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	certImportCertPath     string
	certImportKeyPath      string
	certImportBundlePath   string
	certImportPasswordFile string
)

var certCmd = &cobra.Command{
	Use:   "cert",
	Short: "Manage the MITM certificate authority",
}

var certInspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Validate a custom MITM CA without activating it",
	RunE: func(cmd *cobra.Command, args []string) error {
		certPEM, keyPEM, bundle, password, err := readCertificateImportFlags()
		if err != nil {
			return err
		}
		result, err := services.NewCertService().ValidateMITMCAImport(certPEM, keyPEM, bundle, password)
		if err != nil {
			return err
		}
		return printCertificateJSON(cmd, result)
	},
}

var certImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import and activate a custom MITM CA",
	RunE: func(cmd *cobra.Command, args []string) error {
		certPEM, keyPEM, bundle, password, err := readCertificateImportFlags()
		if err != nil {
			return err
		}
		result, err := services.NewCertService().ImportMITMCA(certPEM, keyPEM, bundle, password)
		if err != nil {
			return err
		}
		return printCertificateJSON(cmd, result)
	},
}

var certStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the active MITM CA status",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := services.NewCertService().GetCertStatus("mitm-ca")
		if err != nil {
			return err
		}
		return printCertificateJSON(cmd, result)
	},
}

var certRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore the previously active MITM CA",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := services.NewCertService().RollbackMITMCA()
		if err != nil {
			return err
		}
		return printCertificateJSON(cmd, result)
	},
}

func init() {
	rootCmd.AddCommand(certCmd)
	certCmd.AddCommand(certInspectCmd, certImportCmd, certStatusCmd, certRollbackCmd)
	for _, command := range []*cobra.Command{certInspectCmd, certImportCmd} {
		command.Flags().StringVar(&certImportCertPath, "cert", "", "Path to a PEM or DER root CA certificate")
		command.Flags().StringVar(&certImportKeyPath, "key", "", "Path to the matching PEM private key")
		command.Flags().StringVar(&certImportBundlePath, "bundle", "", "Path to a PKCS#12/P12/PFX bundle")
		command.Flags().StringVar(&certImportPasswordFile, "password-file", "", "Read the PKCS#12 password from a file")
	}
}

func readCertificateImportFlags() (certPEM, keyPEM, bundle []byte, password string, err error) {
	usingBundle := strings.TrimSpace(certImportBundlePath) != ""
	usingPair := strings.TrimSpace(certImportCertPath) != "" || strings.TrimSpace(certImportKeyPath) != ""
	if usingBundle == usingPair {
		return nil, nil, nil, "", fmt.Errorf("provide either --bundle or both --cert and --key")
	}
	if usingPair && (strings.TrimSpace(certImportCertPath) == "" || strings.TrimSpace(certImportKeyPath) == "") {
		return nil, nil, nil, "", fmt.Errorf("both --cert and --key are required")
	}
	if usingBundle {
		bundle, err = os.ReadFile(certImportBundlePath)
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("read PKCS#12 bundle: %w", err)
		}
		password, err = readPKCS12Password()
		return nil, nil, bundle, password, err
	}
	certPEM, err = os.ReadFile(certImportCertPath)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("read CA certificate: %w", err)
	}
	keyPEM, err = os.ReadFile(certImportKeyPath)
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("read CA private key: %w", err)
	}
	return certPEM, keyPEM, nil, "", nil
}

func readPKCS12Password() (string, error) {
	if strings.TrimSpace(certImportPasswordFile) != "" {
		data, err := os.ReadFile(certImportPasswordFile)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", nil
	}
	_, _ = fmt.Fprint(os.Stderr, "PKCS#12 password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read PKCS#12 password: %w", err)
	}
	return string(password), nil
}

func printCertificateJSON(cmd *cobra.Command, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}
