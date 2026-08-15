package proto

import (
	"fmt"
)

const (
	Direct Proto = iota
	Reject
	HTTP
	Shadowsocks
	//Relay
	HY2
	VLESS
	Aliang
	ShadowTLS
	Socks5
)

type Proto uint8

func (proto Proto) String() string {
	switch proto {
	case Direct:
		return "direct"
	case Reject:
		return "reject"
	case HTTP:
		return "http"
	case Shadowsocks:
		return "ss"
	case HY2:
		return "hy2"
	case VLESS:
		return "vless"
	case Aliang:
		return "aliang"
	case ShadowTLS:
		return "shadowtls"
	case Socks5:
		return "socks5"

	default:
		return fmt.Sprintf("proto(%d)", proto)
	}
}
