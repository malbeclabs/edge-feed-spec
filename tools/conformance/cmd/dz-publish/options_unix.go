package main

import (
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

// setMulticastOptions pins the outbound interface and the TTL.
//
// Both matter and neither has a useful default. Without an interface the route table picks, which
// on a host with a tunnel and a management NIC is a coin toss, and the datagrams leave on the wrong
// one silently. The default TTL for multicast is 1, which never leaves the first hop.
func setMulticastOptions(conn *net.UDPConn, iface *net.Interface, ttl int) error {
	p := ipv4.NewPacketConn(conn)
	if err := p.SetTTL(ttl); err != nil {
		return fmt.Errorf("cannot set multicast TTL to %d: %w", ttl, err)
	}
	if iface != nil {
		if err := p.SetMulticastInterface(iface); err != nil {
			return fmt.Errorf("cannot send from interface %s: %w", iface.Name, err)
		}
	}
	return nil
}
