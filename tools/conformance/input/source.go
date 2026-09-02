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
	// CaptureDrops is the number of datagrams the capture itself admits it failed
	// to record immediately before this one — pcapng's per-packet epb_dropcount,
	// accumulated across any packets the reader skipped.
	//
	// It is the recorder's own admission, and it is the field that separates
	// capture loss from publisher loss: a sequence gap the capture already owns is
	// not evidence about the publisher. Always 0 for a legacy pcap and for live
	// capture, neither of which has anywhere to say it — an absence of accounting,
	// not an assertion that nothing was lost.
	CaptureDrops uint64
}

// Source yields datagrams until closed/EOF. Live = blocking; pcap = until EOF.
type Source interface {
	Next() (Datagram, bool, error) // (dg, ok, err); ok=false at EOF
	Close() error
}

// CaptureLossReporter is a Source whose input carries the recorder's own loss
// accounting. Only pcapng does, so the run loop type-asserts for it rather than
// widening Source: a live socket has no such number to report, and a legacy pcap
// has no field to have recorded one in.
type CaptureLossReporter interface {
	Source
	// CaptureDrops returns the total datagrams the capture admits it failed to
	// record over everything read so far.
	CaptureDrops() uint64
}
