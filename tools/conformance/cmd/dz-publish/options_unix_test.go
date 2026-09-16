package main

import (
	"net"
	"testing"

	"golang.org/x/net/ipv4"
)

// A multicast datagram ignores IP_TTL and reads IP_MULTICAST_TTL, which defaults to 1. Setting the
// wrong one leaves the datagrams visible in a tcpdump on the sending host and dead at the first
// router, so read the option back rather than trusting the call.
func TestSetMulticastOptionsSetsMulticastTTL(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	if err := setMulticastOptions(conn, nil, 8); err != nil {
		t.Fatalf("setMulticastOptions: %v", err)
	}
	got, err := ipv4.NewPacketConn(conn).MulticastTTL()
	if err != nil {
		t.Fatalf("read back multicast TTL: %v", err)
	}
	if got != 8 {
		t.Errorf("multicast TTL = %d, want 8", got)
	}
}
