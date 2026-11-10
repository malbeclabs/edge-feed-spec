package engine

import (
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

type noopReporter struct{}

func (noopReporter) Record(core.Finding)                   {}
func (noopReporter) TransportLoss(core.Port)               {}
func (noopReporter) TransportCorruption(core.Port, string) {}
func (noopReporter) SnapshotAudit(string)                  {}
func (noopReporter) SetInstrumentState(string, int)        {}

// BenchmarkMBOSnapshotCycle measures one full SnapshotBegin → N SnapshotOrders →
// SnapshotEnd + oracle reconciliation pass against an N-order live delta book.
func BenchmarkMBOSnapshotCycle(b *testing.B) {
	const (
		ch      = uint8(1)
		instrID = uint32(300)
		nOrders = 5000
	)

	cfg := Config{
		Feed:                core.FeedMBO,
		ReorderWindow:       1,
		OracleConfirmCycles: 2,
	}
	e := New(cfg, noopReporter{})
	reachedReadyMBO(e, ch, instrID, 0, 1)

	snapFrames := make([][]byte, nOrders)
	for i := uint32(1); i <= nOrders; i++ {
		orderID := uint64(80000 + i)
		addRaw := buildOrderAddExact(ch, instrID, i, orderID, 0, 0, 1000, int64(100+i%50), 10)
		runMktdataSeq(e, addRaw, uint64(i))
		snapFrames[i-1] = buildSnapOrderFields(ch, 1, orderID, 0, 0, 1000, int64(100+i%50), 10)
	}
	e.Flush()

	snapBegin := buildSnapBeginFull(ch, instrID, nOrders, nOrders, 1, nOrders)
	snapEnd := buildSnapEndFull(ch, instrID, nOrders, 1)

	b.ReportAllocs()
	b.ResetTimer()
	snapSeq := uint64(1)
	for range b.N {
		fBegin, sf := wire.Decode(snapBegin, wire.MagicMBO)
		fBegin.Header.Sequence = snapSeq
		e.Process(srcA, fBegin, core.PortSnapshot, sf)
		snapSeq++

		for _, raw := range snapFrames {
			fOrd, sfOrd := wire.Decode(raw, wire.MagicMBO)
			fOrd.Header.Sequence = snapSeq
			e.Process(srcA, fOrd, core.PortSnapshot, sfOrd)
			snapSeq++
		}

		fEnd, sfEnd := wire.Decode(snapEnd, wire.MagicMBO)
		fEnd.Header.Sequence = snapSeq
		e.Process(srcA, fEnd, core.PortSnapshot, sfEnd)
		snapSeq++
	}
}

// BenchmarkOracleRun measures runOracleForGroup in isolation against a 5,000-order
// live book when delta state and snapshot state match.
func BenchmarkOracleRun(b *testing.B) {
	const (
		ch      = uint8(1)
		instrID = uint32(300)
		nOrders = 5000
	)

	cfg := Config{
		Feed:                core.FeedMBO,
		ReorderWindow:       1,
		OracleConfirmCycles: 2,
	}
	e := New(cfg, noopReporter{})
	reachedReadyMBO(e, ch, instrID, 0, 1)

	snapOrders := make(map[uint64]snapOrderRecord, nOrders)
	for i := uint32(1); i <= nOrders; i++ {
		orderID := uint64(80000 + i)
		addRaw := buildOrderAddExact(ch, instrID, i, orderID, 0, 0, 1000, int64(100+i%50), 10)
		runMktdataSeq(e, addRaw, uint64(i))
		snapOrders[orderID] = snapOrderRecord{
			side:       0,
			orderFlags: 0,
			enterTS:    1000,
			price:      int64(100 + i%50),
			qty:        10,
		}
	}
	e.Flush()

	snap := &openSnapshot{
		instrID:       instrID,
		snapshotID:    1,
		anchorSeq:     nOrders,
		total:         nOrders,
		received:      nOrders,
		lastInstrSeqK: nOrders,
		orders:        snapOrders,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		e.runOracleForGroup(ch, snap, uint64(i+1))
	}
}

// BenchmarkWireDecodeMultiOrder measures wire.Decode on a 10-message OrderAdd datagram.
func BenchmarkWireDecodeMultiOrder(b *testing.B) {
	fb := wb.Frame(wire.MagicMBO).Channel(1).Seq(1)
	for i := range 10 {
		fb = fb.Msg(wire.TypeOrderAdd, 52, func(body *wb.Body) {
			body.U32(300).U16(1).U8(0).U8(0).U32(uint32(i + 1)).
				U64(uint64(80001 + i)).U64(1000).I64(100).U64(10).Pad(4)
		})
	}
	raw := fb.Bytes()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		f, fs := wire.Decode(raw, wire.MagicMBO)
		if len(fs) != 0 || len(f.Messages) != 10 {
			b.Fatalf("unexpected decode result: msgs=%d findings=%d", len(f.Messages), len(fs))
		}
	}
}
