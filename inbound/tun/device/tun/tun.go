// Package tun provides TUN which implemented device.Device interface.
package tun

import (
	"aliang.one/nursorgate/inbound/tun/device"
)

const Driver = "tun"

func (t *TUN) Type() string {
	return Driver
}

var _ device.Device = (*TUN)(nil)
