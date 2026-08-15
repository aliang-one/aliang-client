package cmd

import (
	"reflect"
	"testing"

	"aliang.one/nursorgate/processor/config"
)

func TestHostFlagDefaultsToLoopback(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("host")
	if flag == nil {
		t.Fatal("root command does not define --host")
	}
	if flag.DefValue != config.DefaultServiceBindHost {
		t.Fatalf("--host default = %q, want %q", flag.DefValue, config.DefaultServiceBindHost)
	}
}

func TestServiceArgsPersistOnlyExplicitHost(t *testing.T) {
	if err := config.SetServiceBindHost("0.0.0.0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = config.SetServiceBindHost(config.DefaultServiceBindHost)
	})

	base := []string{"core"}
	if got := serviceArgsWithHost(base, false); !reflect.DeepEqual(got, base) {
		t.Fatalf("implicit host changed service args to %v", got)
	}
	if got, want := serviceArgsWithHost(base, true), []string{"core", "--host", "0.0.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit host service args = %v, want %v", got, want)
	}
}

func TestConfigureServiceBindHostControlsBothListeners(t *testing.T) {
	if err := config.SetServiceBindHost(config.DefaultServiceBindHost); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = config.SetServiceBindHost(config.DefaultServiceBindHost)
	})

	if err := configureServiceBindHost("0.0.0.0"); err != nil {
		t.Fatalf("configureServiceBindHost() error = %v", err)
	}
	if got := config.ManagementListenAddr(); got != "0.0.0.0:56431" {
		t.Fatalf("management listen address = %q", got)
	}
	if got := config.HTTPProxyListenAddr(); got != "0.0.0.0:56432" {
		t.Fatalf("proxy listen address = %q", got)
	}
}
