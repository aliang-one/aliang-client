//go:build integration

package test

import (
	"testing"

	httpServer "aliang.one/nursorgate/app/http"
	"aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/inbound/tun/runner"
)

func TestLaunch(t *testing.T) {
	runner.Start()
}

func TestServer(t *testing.T) {
	httpServer.StartHttpServer()
}

func TestMitmHttp(t *testing.T) {
	http.StartMitmHttp()
}
