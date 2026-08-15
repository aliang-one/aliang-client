package engine

import (
	"fmt"
	"net/netip"
	"net/url"
	"runtime"
	"strings"

	"aliang.one/nursorgate/inbound/tun/device"
	"aliang.one/nursorgate/inbound/tun/device/fdbased"
	"aliang.one/nursorgate/inbound/tun/device/tun"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/direct"
	"aliang.one/nursorgate/outbound/proxy/http"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

func parseProxy(s string) (proxy.Proxy, error) {
	if !strings.Contains(s, "://") {
		//s = fmt.Sprintf("%s://%s", proto.Socks5 /* default protocol */, s)
	}

	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	protocol := strings.ToLower(u.Scheme)

	switch protocol {
	case proto.Direct.String():
		return direct.NewDirect(), nil
	case proto.Reject.String():
		return proxy.NewReject(), nil
	case proto.HTTP.String():
		return parseHTTP(u)

	case proto.Shadowsocks.String():
		// Shadowsocks proxy should be configured through the API or FFI
		return nil, fmt.Errorf("Shadowsocks proxy not configured, please register it via API or FFI")

	case proto.VLESS.String():
		// VLESS proxy should be configured through the API or FFI
		return nil, fmt.Errorf("VLESS proxy not configured, please register it via API or FFI")
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

func parseHTTP(u *url.URL) (proxy.Proxy, error) {
	address, username := u.Host, u.User.Username()
	password, _ := u.User.Password()
	return http.NewHTTP(address, username, password)
}

func parseDevice(s string, mtu uint32) (device.Device, error) {
	if !strings.Contains(s, "://") {
		s = fmt.Sprintf("%s://%s", tun.Driver /* default driver */, s)
	}

	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	driver := strings.ToLower(u.Scheme)

	switch driver {
	case fdbased.Driver:
		return parseFD(u, mtu)
	case tun.Driver:
		return parseTUN(u, mtu)
	default:
		return nil, fmt.Errorf("unsupported driver: %s", driver)
	}
}

func parseFD(u *url.URL, mtu uint32) (device.Device, error) {
	offset := 0
	// fd offset in ios
	// https://stackoverflow.com/questions/69260852/ios-network-extension-packet-parsing/69487795#69487795
	if runtime.GOOS == "ios" {
		offset = 4
	}
	return fdbased.Open(u.Host, mtu, offset)
}

func parseMulticastGroups(s string) (multicastGroups []netip.Addr, _ error) {
	for _, ip := range strings.Split(s, ",") {
		if ip = strings.TrimSpace(ip); ip == "" {
			continue
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return nil, err
		}
		if !addr.IsMulticast() {
			return nil, fmt.Errorf("invalid multicast IP: %s", addr)
		}
		multicastGroups = append(multicastGroups, addr)
	}
	return
}
