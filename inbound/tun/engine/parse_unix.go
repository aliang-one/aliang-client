//go:build unix

package engine

import (
	"net/url"

	"aliang.one/nursorgate/inbound/tun/device"
	"aliang.one/nursorgate/inbound/tun/device/tun"
)

func parseTUN(u *url.URL, mtu uint32) (device.Device, error) {
	return tun.Open(u.Host, mtu)
}
