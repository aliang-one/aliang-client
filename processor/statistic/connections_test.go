package statistic

import (
	"net/netip"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

func metaFor(route string) *M.Metadata {
	return &M.Metadata{
		Network:  M.TCP,
		HostName: "example.com",
		DstIP:    netip.AddrFrom4([4]byte{93, 184, 216, 34}),
		DstPort:  443,
		SrcIP:    netip.AddrFrom4([4]byte{10, 0, 0, 2}),
		SrcPort:  51000,
		Route:    route,
	}
}

func TestActiveConnections_ListsProxiedOnly(t *testing.T) {
	manager := newTestManager()
	proxied := NewTCPTracker(&closeCountingConn{}, metaFor("RouteToALiang"), manager)
	direct := NewTCPTracker(&closeCountingConn{}, metaFor("RouteDirect"), manager)
	udpProxied := NewUDPTracker(&packetConnStub{}, &M.Metadata{
		Network: M.UDP,
		Route:   "RouteToSocks",
	}, manager)
	defer func() {
		_ = proxied.Close()
		_ = direct.Close()
		_ = udpProxied.Close()
	}()

	records := manager.ActiveConnections()
	if len(records) != 2 {
		t.Fatalf("active records = %d, want 2 (direct excluded)", len(records))
	}
	routes := map[string]int{}
	for _, record := range records {
		routes[record.Route]++
		if record.Network == "udp" {
			if record.Route != "RouteToSocks" {
				t.Fatalf("udp record route = %s", record.Route)
			}
			continue
		}
		if record.Route != "RouteToALiang" {
			t.Fatalf("tcp record route = %s", record.Route)
		}
		if record.Host != "example.com" {
			t.Fatalf("host = %s, want example.com", record.Host)
		}
		if record.Destination == "" || record.Source == "" {
			t.Fatalf("destination/source empty: %+v", record)
		}
		if record.StartUnix <= 0 {
			t.Fatalf("startUnix = %d, want >0", record.StartUnix)
		}
	}
	if routes["RouteToALiang"] != 1 || routes["RouteToSocks"] != 1 {
		t.Fatalf("route counts = %+v", routes)
	}
}

func TestClosedConnectionHistory_RingAndOrder(t *testing.T) {
	manager := newTestManager()

	// 建容量+2 条连接并全部关闭:最旧 2 条被淘汰,剩余新→旧。
	for i := 0; i < closedHistoryCapacity+2; i++ {
		conn := NewTCPTracker(&closeCountingConn{}, metaFor("RouteToALiang"), manager)
		conn.Write([]byte("x"))
		_ = conn.Close()
	}

	closed := manager.ClosedConnections()
	if len(closed) != closedHistoryCapacity {
		t.Fatalf("closed history = %d, want %d", len(closed), closedHistoryCapacity)
	}
	for i := 1; i < len(closed); i++ {
		if closed[i-1].EndUnix < closed[i].EndUnix {
			t.Fatalf("history not ordered newest-first at %d", i)
		}
	}
	latest := closed[0]
	if latest.UploadTotal != 1 {
		t.Fatalf("latest upload = %d, want 1", latest.UploadTotal)
	}
	if latest.Route != "RouteToALiang" {
		t.Fatalf("latest route = %s", latest.Route)
	}
	if latest.EndUnix <= 0 {
		t.Fatalf("endUnix = %d, want >0", latest.EndUnix)
	}
}

func TestClosedConnectionHistory_IdempotentCloseSingleRecord(t *testing.T) {
	manager := newTestManager()
	conn := NewTCPTracker(&closeCountingConn{}, metaFor("RouteToSocks"), manager)
	_ = conn.Close()
	_ = conn.Close()

	if got := len(manager.ClosedConnections()); got != 1 {
		t.Fatalf("closed records = %d, want 1 (close is idempotent)", got)
	}
}

func TestClosedConnectionHistory_DirectExcluded(t *testing.T) {
	manager := newTestManager()
	conn := NewTCPTracker(&closeCountingConn{}, metaFor("RouteDirect"), manager)
	_ = conn.Close()

	if got := len(manager.ClosedConnections()); got != 0 {
		t.Fatalf("closed records = %d, want 0 (direct not tracked)", got)
	}
}

func TestActiveConnections_EmptyWhenIdle(t *testing.T) {
	manager := newTestManager()
	if records := manager.ActiveConnections(); len(records) != 0 {
		t.Fatalf("idle active records = %d, want 0", len(records))
	}
	if closed := manager.ClosedConnections(); len(closed) != 0 {
		t.Fatalf("idle closed records = %d, want 0", len(closed))
	}
}
