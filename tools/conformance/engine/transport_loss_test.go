package engine

// transport_loss_test.go — transport loss names the publisher, and counts what is
// missing.
//
// Several publishers can serve one channel on one group and one port. Loss on one
// of them is a fault on that path, and loss on all of them points at the publisher
// side, so a loss event that names no publisher cannot tell the two apart. An event
// is also not a datagram: one gap can skip one Sequence Number or a thousand. The
// reporter gets the source address and the count of skipped Sequence Numbers of
// each gap, and each accepted datagram, which give the loss ratio of one
// publisher: missing / (accepted + missing).

import (
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// TestTransportLossNamesThePublisherAndCountsMissingSeqs: A and B serve one
// channel on one port. A skips Sequence Numbers 3, 4 and 5. B skips none.
func TestTransportLossNamesThePublisherAndCountsMissingSeqs(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, cap)
	send := func(src netip.Addr, seq uint64) {
		eng.Process(src, makeHB(wire.MagicTOB, chArmA, seq, 0, (seq+1)*nsPerSec), core.PortMktData, nil)
	}
	for _, seq := range []uint64{0, 1, 2} {
		send(srcA, seq)
	}
	for seq := uint64(100); seq < 105; seq++ {
		send(srcB, seq)
	}
	send(srcA, 6)
	send(srcA, 7)
	eng.Flush()

	if len(cap.losses) != 1 {
		t.Fatalf("got %d loss events, want 1: %+v", len(cap.losses), cap.losses)
	}
	if got := cap.losses[0]; got.src != srcA || got.missing != 3 || got.port != core.PortMktData {
		t.Errorf("loss event %+v, want A on mktdata with 3 missing Sequence Numbers", got)
	}
	if cap.accepted[srcA] != 5 || cap.accepted[srcB] != 5 {
		t.Errorf("accepted datagrams %v, want 5 for A and 5 for B", cap.accepted)
	}
}

// TestDiscardedDatagramIsLossButNotMissing: a late datagram of an era the port
// left is a loss event of its publisher, but no Sequence Number of the current
// series is missing, so the missing count is 0.
func TestDiscardedDatagramIsLossButNotMissing(t *testing.T) {
	cap := &captureAll{}
	eng := New(Config{Feed: core.FeedTOB, ReorderWindow: 1}, cap)
	eng.Process(srcA, makeHB(wire.MagicTOB, chArmA, 0, 1, 1*nsPerSec), core.PortMktData, nil)
	eng.Process(srcA, makeHB(wire.MagicTOB, chArmA, 0, 2, 2*nsPerSec), core.PortMktData, nil) // era 2
	eng.Process(srcA, makeHB(wire.MagicTOB, chArmA, 1, 1, 3*nsPerSec), core.PortMktData, nil) // late, era 1
	eng.Flush()

	if len(cap.losses) != 1 {
		t.Fatalf("got %d loss events, want 1: %+v", len(cap.losses), cap.losses)
	}
	if got := cap.losses[0]; got.src != srcA || got.missing != 0 {
		t.Errorf("loss event %+v, want A with 0 missing", got)
	}
}
