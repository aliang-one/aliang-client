//go:build !linux

package tun

import (
	"fmt"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/device"
	"aliang.one/nursorgate/inbound/tun/device/iobased"

	"golang.zx2c4.com/wireguard/tun"
)

// endpointWaitTimeout 是 Close 后等待 Endpoint 读写协程退场的兜底上界。
const endpointWaitTimeout = 3 * time.Second

type TUN struct {
	*iobased.Endpoint

	nt     *tun.NativeTun
	mtu    uint32
	name   string
	offset int

	rSizes []int
	rBuffs [][]byte
	wBuffs [][]byte
	rMutex sync.Mutex
	wMutex sync.Mutex
}

func Open(name string, mtu uint32) (_ device.Device, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("open tun: %s", logger.SafeRecoveredValueString(r))
		}
	}()

	t := &TUN{
		name:   name,
		mtu:    mtu,
		offset: offset,
		rSizes: make([]int, 1),
		rBuffs: make([][]byte, 1),
		wBuffs: make([][]byte, 1),
	}

	forcedMTU := defaultMTU
	if t.mtu > 0 {
		forcedMTU = int(t.mtu)
	}

	nt, err := createTUN(t.name, forcedMTU)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}
	t.nt = nt.(*tun.NativeTun)

	tunMTU, err := nt.MTU()
	if err != nil {
		return nil, fmt.Errorf("get mtu: %w", err)
	}
	t.mtu = uint32(tunMTU)

	ep, err := iobased.New(t, t.mtu, offset)
	if err != nil {
		return nil, fmt.Errorf("create endpoint: %w", err)
	}
	t.Endpoint = ep

	return t, nil
}

func (t *TUN) Read(packet []byte) (int, error) {
	t.rMutex.Lock()
	defer t.rMutex.Unlock()
	t.rBuffs[0] = packet
	_, err := t.nt.Read(t.rBuffs, t.rSizes, t.offset)
	return t.rSizes[0], err
}

func (t *TUN) Write(packet []byte) (int, error) {
	t.wMutex.Lock()
	defer t.wMutex.Unlock()
	t.wBuffs[0] = packet
	return t.nt.Write(t.wBuffs, t.offset)
}

func (t *TUN) Name() string {
	name, _ := t.nt.Name()
	return name
}

func (t *TUN) Close() {
	if t.Endpoint != nil {
		t.Endpoint.Close()
	}
	if t.nt != nil {
		_ = t.nt.Close()
	}
	if t.Endpoint != nil {
		// Endpoint.Wait 等读写协程退场;device 已关,正常毫秒级返回。给
		// 3s 上界:异常挂死时放弃等待优于让整个停止链挂死(2026-11-16
		// Windows 停止链 StopPending 挂死实锤)。退出场景下泄漏安全;
		// 单例已置 nil,复用取新设备,不会与被放弃的 device 重组。
		done := make(chan struct{})
		go func() {
			t.Endpoint.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(endpointWaitTimeout):
			logger.Warn("TUN endpoint wait timed out after close (goroutines leaked; safe on shutdown)")
		}
	}
}
