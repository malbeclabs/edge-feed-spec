package input

import (
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

type Datagram struct {
	Port   core.Port
	Raw    []byte
	RecvTS time.Time
}

// Source yields datagrams until closed/EOF. Live = blocking; pcap = until EOF.
type Source interface {
	Next() (Datagram, bool, error) // (dg, ok, err); ok=false at EOF
	Close() error
}
