package config

import (
	"sync"
	"time"
)

// tun2socks默认的配置，被我提取除去了proxy

type EngineConf struct {
	MTU                      int           `yaml:"mtu"`
	Mark                     int           `yaml:"fwmark"`
	Device                   string        `yaml:"device"`
	Interface                string        `yaml:"interface"`
	TCPModerateReceiveBuffer bool          `yaml:"tcp-moderate-receive-buffer"`
	TCPSendBufferSize        string        `yaml:"tcp-send-buffer-size"`
	TCPReceiveBufferSize     string        `yaml:"tcp-receive-buffer-size"`
	MulticastGroups          string        `yaml:"multicast-groups"`
	TUNPreUp                 string        `yaml:"tun-pre-up"`
	TUNPostUp                string        `yaml:"tun-post-up"`
	UDPTimeout               time.Duration `yaml:"udp-timeout"`
}

// _defaultEngineConf holds the default key for the engine.
var _defaultEngineConf *EngineConf
var _engineMu sync.Mutex

// Insert loads *Key to the default engine.
func Insert(k *EngineConf) {
	_engineMu.Lock()
	_defaultEngineConf = k
	_engineMu.Unlock()
}

func GetDefaultEngineConf() *EngineConf {
	_engineMu.Lock()
	defer _engineMu.Unlock()
	return _defaultEngineConf
}
