package main

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// recorder is a real sender whose socket is a slice, so the sequence counter under test is the
// production one rather than a copy the test keeps in step by hand.
func recorder(sent *[][]byte) *sender {
	return &sender{
		write: func(datagram []byte) error {
			*sent = append(*sent, datagram)
			return nil
		},
		close: func() error { return nil },
	}
}

// decodeOne decodes a datagram and fails on any structural finding. The decoder is the same one
// dz-conformance runs, so a datagram this accepts is one the checker will accept structurally.
func decodeOne(t *testing.T, raw []byte) *wire.Frame {
	t.Helper()
	f, findings := wire.Decode(raw, wire.MagicTOB)
	if len(findings) != 0 {
		t.Fatalf("decoder rejected a datagram this publisher sent: %+v", findings)
	}
	return f
}

// Every datagram the publisher builds has to decode cleanly. Break any length, any padding or the
// magic, and this fails.
func TestEveryDatagramDecodes(t *testing.T) {
	var refSent [][]byte
	refData := recorder(&refSent)
	if err := sendManifestCycle(refData); err != nil {
		t.Fatalf("manifest cycle: %v", err)
	}
	var mktSent [][]byte
	mktData := recorder(&mktSent)
	if err := sendQuote(mktData, 1); err != nil {
		t.Fatalf("quote: %v", err)
	}
	if err := sendHeartbeat(mktData); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	wantRef := []uint8{wire.TypeManifest, wire.TypeInstrumentDef, wire.TypeManifest}
	if len(refSent) != len(wantRef) {
		t.Fatalf("refdata sent %d datagrams, want %d", len(refSent), len(wantRef))
	}
	for i, raw := range refSent {
		f := decodeOne(t, raw)
		if f.Messages[0].Type != wantRef[i] {
			t.Errorf("refdata datagram %d carries type %#x, want %#x", i, f.Messages[0].Type, wantRef[i])
		}
	}

	wantMkt := []uint8{wire.TypeQuote, wire.TypeHeartbeat}
	for i, raw := range mktSent {
		f := decodeOne(t, raw)
		if f.Messages[0].Type != wantMkt[i] {
			t.Errorf("mktdata datagram %d carries type %#x, want %#x", i, f.Messages[0].Type, wantMkt[i])
		}
	}
}

// The bootstrap cycle has to be manifest, definition, manifest. A definition outside a pair of
// summaries is not attributable to a manifest sequence, so a subscriber cannot grade the quotes
// that follow it.
func TestTheDefinitionSitsBetweenTwoSummaries(t *testing.T) {
	var refSent [][]byte
	refData := recorder(&refSent)
	if err := sendManifestCycle(refData); err != nil {
		t.Fatalf("manifest cycle: %v", err)
	}
	if got := len(refSent); got != 3 {
		t.Fatalf("bootstrap sent %d datagrams, want 3", got)
	}
	if decodeOne(t, refSent[1]).Messages[0].Type != wire.TypeInstrumentDef {
		t.Error("the definition is not the middle datagram of the cycle")
	}
}

// Sequence numbers start at 0, which the spec requires of a new session, and advance on every
// datagram, which is the whole reason this publisher exists rather than a loop over a fixed
// capture. A repeated sequence reads to a subscriber as backward
// motion, which latches its verifiability window for the life of the process.
func TestSequenceAdvancesOnEveryDatagram(t *testing.T) {
	var mktSent [][]byte
	mktData := recorder(&mktSent)
	for tick := uint64(1); tick <= 5; tick++ {
		if err := sendQuote(mktData, tick); err != nil {
			t.Fatalf("quote %d: %v", tick, err)
		}
	}
	for i, raw := range mktSent {
		// Starting from 0, which top-of-book/spec.md requires of a new session's series.
		want := uint64(i)
		if got := decodeOne(t, raw).Header.Sequence; got != want {
			t.Errorf("datagram %d carries sequence %d, want %d", i, got, want)
		}
	}
}

// The two ports are two channel instances, keyed on destination port, so each owns its own
// sequence series. One shared counter would read as two publishers alternating on one series.
func TestEachPortKeepsItsOwnSequence(t *testing.T) {
	var refSent, mktSent [][]byte
	refData, mktData := recorder(&refSent), recorder(&mktSent)
	if err := sendManifestCycle(refData); err != nil {
		t.Fatalf("manifest cycle: %v", err)
	}
	if err := sendQuote(mktData, 1); err != nil {
		t.Fatalf("quote: %v", err)
	}
	if got := decodeOne(t, mktSent[0]).Header.Sequence; got != 0 {
		t.Errorf("the first quote carries sequence %d, want 0; the ports share a counter", got)
	}
	if got := decodeOne(t, refSent[2]).Header.Sequence; got != 2 {
		t.Errorf("the third refdata datagram carries sequence %d, want 2", got)
	}
}
