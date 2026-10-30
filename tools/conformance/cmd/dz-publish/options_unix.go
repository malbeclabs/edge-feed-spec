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
	// SetMulticastTTL, not SetTTL: SetTTL sets IP_TTL, which a multicast datagram ignores. The
	// multicast default is 1, so with the wrong call every datagram leaves this host, shows up in
	// a local tcpdump, and dies at the first router.
	if err := p.SetMulticastTTL(ttl); err != nil {
		return fmt.Errorf("cannot set multicast TTL to %d: %w", ttl, err)
	}
	if iface != nil {
		if err := p.SetMulticastInterface(iface); err != nil {
			return fmt.Errorf("cannot send from interface %s: %w", iface.Name, err)
		}
	}
	// Loopback is the sender's setting, not the receiver's: it decides whether the kernel delivers
	// a copy to sockets on this host that joined the group. The default is on, but a host that has
	// turned it off makes a subscriber here see nothing while the datagrams are plainly on the
	// wire, which is a long way to chase for one setsockopt.
	if err := p.SetMulticastLoopback(true); err != nil {
		return fmt.Errorf("cannot enable multicast loopback: %w", err)
	}
	return nil
}
