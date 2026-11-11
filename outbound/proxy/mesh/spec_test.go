package mesh

import "testing"

func TestPeerSpecCacheKeyOrderInsensitive(t *testing.T) {
	a := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"1.1.1.1:56432", "2.2.2.2:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	b := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"2.2.2.2:56432", "1.1.1.1:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	if a.CacheKey("pk", "") != b.CacheKey("pk", "") {
		t.Fatal("same set, different order → same key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk2", "") {
		t.Fatal("private key change must change key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk", "pw") {
		t.Fatal("passphrase change must change key")
	}
	c := a
	c.HostKey = "h2"
	if a.CacheKey("pk", "") == c.CacheKey("pk", "") {
		t.Fatal("host key change must change key")
	}
	d := a
	d.DeviceID = "dev-c"
	if a.CacheKey("pk", "") == d.CacheKey("pk", "") {
		t.Fatal("device id change must change key")
	}
	e := a
	e.SSHUser = "mesh2"
	if a.CacheKey("pk", "") == e.CacheKey("pk", "") {
		t.Fatal("ssh user change must change key")
	}
}
