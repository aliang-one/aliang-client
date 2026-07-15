package engine

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/runner/utils"
	"aliang.one/nursorgate/outbound"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/direct"
	"github.com/sagernet/gvisor/pkg/tcpip/stack"

	"aliang.one/nursorgate/inbound/tun"
	"aliang.one/nursorgate/inbound/tun/device"
	"aliang.one/nursorgate/inbound/tun/dialer"
	"aliang.one/nursorgate/inbound/tun/option"
	"aliang.one/nursorgate/inbound/tun/tunnel"
	proxyRegistry "aliang.one/nursorgate/outbound"
	config "aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/dns"
	"github.com/docker/go-units"
	"github.com/google/shlex"
)

var (
	_engineMu sync.Mutex

	// _defaultProxy holds the default proxy for the engine.
	_defaultProxy proxy.Proxy

	// _defaultDevice holds the default device for the engine.
	_defaultDevice device.Device

	// _defaultStack holds the default stack for the engine.
	_defaultStack *stack.Stack
)

// Start starts the default engine up.
func Start() error {
	if err := start(); err != nil {
		logger.Error(fmt.Sprintf("[ENGINE] failed to start: %v", err))
		return err
	}
	return nil
}

// Stop shuts the default engine down.
func Stop() {
	if err := stop(); err != nil {
		logger.Error(fmt.Sprintf("[ENGINE] failed to stop: %v", err))
	}
}

func start() error {
	_engineMu.Lock()
	defer _engineMu.Unlock()

	if config.GetDefaultEngineConf() == nil {
		return errors.New("empty key")
	}

	for _, f := range []func(*config.EngineConf) error{
		general,
		netstack,
	} {
		if err := f(config.GetDefaultEngineConf()); err != nil {
			return err
		}
	}
	return nil
}

func stop() (err error) {
	_engineMu.Lock()
	defer _engineMu.Unlock()

	device := _defaultDevice
	stack := _defaultStack
	_defaultDevice = nil
	_defaultStack = nil
	_defaultProxy = nil

	if device != nil {
		device.Close()
	}
	closedConnections := tunnel.T().CloseActiveTCPConnections()
	if closedConnections > 0 {
		logger.Debug(fmt.Sprintf("[ENGINE] closed %d active TUN TCP connection(s)", closedConnections))
	}
	if stack != nil {
		stack.Close()
		stack.Wait()
	}
	return nil
}

func execCommand(cmd string) error {
	parts, err := shlex.Split(cmd)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return errors.New("empty command")
	}
	// cmds := exec.Command(parts[0], parts[1:]...)
	// cmds.SysProcAttr = &syscall.SysProcAttr{
	// 	HideWindow: true,
	// }
	// _, err = cmds.Output()
	err = utils.RunCommand(parts[0], parts[1:]...)
	return err
}

func general(k *config.EngineConf) error {
	//TODO: Auto here
	if k.Interface != "" {
		iface, err := net.InterfaceByName(k.Interface)
		if err != nil {
			return err
		}
		dialer.DefaultInterfaceName.Store(iface.Name)
		dialer.DefaultInterfaceIndex.Store(int32(iface.Index))
	}

	if k.Mark != 0 {
		dialer.DefaultRoutingMark.Store(int32(k.Mark))

	}

	if k.UDPTimeout > 0 {
		if k.UDPTimeout < time.Second {
			return errors.New("invalid udp timeout value")
		}
		tunnel.T().SetUDPTimeout(k.UDPTimeout)
	}
	return nil
}

func netstack(k *config.EngineConf) (err error) {
	if k.Device == "" {
		return errors.New("empty device")
	}

	if k.TUNPreUp != "" {
		print(fmt.Sprintf("[TUN] pre-execute command: `%s`", k.TUNPreUp))
		if preUpErr := execCommand(k.TUNPreUp); preUpErr != nil {
			logger.Info(fmt.Sprintf("[TUN] failed to pre-execute: %s: %v", k.TUNPreUp, preUpErr))
		}
	}

	defer func() {
		if k.TUNPostUp == "" || err != nil {
			return
		}
		print(fmt.Sprintf("[TUN] post-execute command: `%s`", k.TUNPostUp))
		if postUpErr := execCommand(k.TUNPostUp); postUpErr != nil {
			logger.Info(fmt.Sprintf("[TUN] failed to post-execute: %s: %v", k.TUNPostUp, postUpErr))
		}
	}()

	// 使用硬编码的 direct 代理作为默认代理
	_defaultProxy, err = proxyRegistry.GetRegistry().Get("direct")
	if err != nil {
		// 如果 direct 代理未注册，创建一个新的
		_defaultProxy = direct.NewDirect()
		logger.Warn("Direct proxy not registered, creating new instance")
	}

	// 设置代理到 tunnel 的 dialer（用于 direct dialing）
	tunnel.T().SetDialer(_defaultProxy)

	// 获取direct代理用于回退
	registry := outbound.GetRegistry()
	directProxy, err := registry.Get("direct")
	if err != nil {
		logger.Warn(fmt.Sprintf("Direct proxy not available for DNS resolver: %v", err))
		directProxy = nil
	}

	// 使用混合DNS解析器，主/回退均使用 direct（无 door）
	hybridResolver := dns.CreateDefaultHybridResolver(directProxy, directProxy)
	dns.SetGlobalResolver(hybridResolver)
	if _defaultDevice, err = parseDevice(k.Device, uint32(k.MTU)); err != nil {
		return err
	}

	var multicastGroups []netip.Addr
	if multicastGroups, err = parseMulticastGroups(k.MulticastGroups); err != nil {
		return err
	}

	var opts []option.Option
	if k.TCPModerateReceiveBuffer {
		opts = append(opts, option.WithTCPModerateReceiveBuffer(true))
	}

	if k.TCPSendBufferSize != "" {
		size, err := units.RAMInBytes(k.TCPSendBufferSize)
		if err != nil {
			return err
		}
		opts = append(opts, option.WithTCPSendBufferSize(int(size)))
	}

	if k.TCPReceiveBufferSize != "" {
		size, err := units.RAMInBytes(k.TCPReceiveBufferSize)
		if err != nil {
			return err
		}
		opts = append(opts, option.WithTCPReceiveBufferSize(int(size)))
	}

	if _defaultStack, err = tun.CreateStack(&tun.Config{
		LinkEndpoint:     _defaultDevice,
		TransportHandler: tunnel.T(),
		MulticastGroups:  multicastGroups,
		Options:          opts,
	}); err != nil {
		return
	}

	logger.Info(
		fmt.Sprintf("[STACK] %s://%s <-> %s://%s",
			_defaultDevice.Type(), _defaultDevice.Name(),
			_defaultProxy.Proto(), _defaultProxy.Addr(),
		),
	)
	return nil
}
