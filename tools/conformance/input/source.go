package input

import (
	"net/netip"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

type Datagram struct {
	Port core.Port
	// Src is the sending address. It is part of the channel instance's identity:
	// sequencing keys on (source address, channel, destination port), so a source
	// this reader discards is a source whose series cannot be told from another's.
	// The zero Addr means the source could not be determined.
	Src    netip.Addr
	Raw    []byte
	RecvTS time.Time
}

// Source yields datagrams until closed/EOF. Live = blocking; pcap = until EOF.
type Source interface {
	Next() (Datagram, bool, error) // (dg, ok, err); ok=false at EOF
	Close() error
}
