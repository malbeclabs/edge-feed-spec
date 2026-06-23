package main

// golden_test.go — Golden pcap regression tests (Task 29).
//
// For each feed (MBO, TOB, Midpoint) a conformant capture is written to a
// temp pcap file, then Run() is executed over it.  Two assertions are made:
//
//  1. Run() returns exit code 0 (which means zero must-severity Violations —
//     the Aggregator only sets exit code 1 when mustViol > 0, with strict=false).
//  2. A separate engine pass with a capturing reporter confirms that no
//     Must+Violation finding was emitted.
//
// Cadence flags (ExpectManifest*, ExpectHeartbeat, etc.) are all left at zero
// so timing-based conditional rules never fire on the synthetic captures.
// OracleConfirmCycles=2, ReorderWindow=1 (frames classified immediately).

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/engine"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/report"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// golden UDP port constants — arbitrary, must not collide with run_test.go.
const (
	goldenMktDataPort  = 18001
	goldenRefDataPort  = 18002
	goldenSnapDataPort = 18003
)

// goldenPortMap is the port map used for all golden tests.
var goldenPortMap = map[int]core.Port{
	goldenMktDataPort:  core.PortMktData,
	goldenRefDataPort:  core.PortRefData,
	goldenSnapDataPort: core.PortSnapshot,
}

// goldenCapture collects all findings emitted by an engine run.
type goldenCapture struct {
	findings []core.Finding
}

func (g *goldenCapture) Record(f core.Finding)                 { g.findings = append(g.findings, f) }
func (g *goldenCapture) TransportLoss(core.Port)               {}
func (g *goldenCapture) TransportCorruption(core.Port, string) {}
func (g *goldenCapture) SnapshotAudit(string)                  {}
func (g *goldenCapture) SetInstrumentState(string, int)        {}

// mustViolationCount returns the count of Must+Violation findings.
func mustViolationCount(gc *goldenCapture) int {
	n := 0
	for _, f := range gc.findings {
		if f.Severity == core.Must && f.Status == core.Violation {
			n++
		}
	}
	return n
}

// ---- pcap writer ----

// goldenPcapEntry is one packet: raw payload + destination UDP port.
type goldenPcapEntry struct {
	payload []byte
	dstPort uint16
}

// writeGoldenPcap writes a pcap file from a slice of goldenPcapEntry values.
// Each entry becomes one Ethernet/IPv4/UDP packet.
func writeGoldenPcap(t *testing.T, path string, entries []goldenPcapEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pcap %q: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatalf("write pcap header: %v", err)
	}

	buf := gopacket.NewSerializeBuffer()
	serOpts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}

	for i, e := range entries {
		_ = buf.Clear()
		ip4 := &layers.IPv4{
			Version:  4,
			TTL:      64,
			Protocol: layers.IPProtocolUDP,
			SrcIP:    net.IP{10, 0, 0, 1},
			DstIP:    net.IP{10, 0, 0, 2},
		}
		udp := &layers.UDP{
			SrcPort: layers.UDPPort(50000 + i),
			DstPort: layers.UDPPort(e.dstPort),
		}
		if err := udp.SetNetworkLayerForChecksum(ip4); err != nil {
			t.Fatalf("SetNetworkLayerForChecksum packet %d: %v", i, err)
		}
		if err := gopacket.SerializeLayers(buf, serOpts,
			&layers.Ethernet{
				SrcMAC:       net.HardwareAddr{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
				DstMAC:       net.HardwareAddr{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b},
				EthernetType: layers.EthernetTypeIPv4,
			},
			ip4,
			udp,
			gopacket.Payload(e.payload),
		); err != nil {
			t.Fatalf("serialize packet %d: %v", i, err)
		}
		ci := gopacket.CaptureInfo{
			Timestamp:     time.Unix(int64(1000+i), 0),
			CaptureLength: len(buf.Bytes()),
			Length:        len(buf.Bytes()),
		}
		if err := w.WritePacket(ci, buf.Bytes()); err != nil {
			t.Fatalf("write packet %d: %v", i, err)
		}
	}
}

// ---- MBO golden capture ----

// buildMBOGoldenEntries builds the conformant MBO frame sequence that mirrors
// TestMBOIntegrationConformant (engine/mbo_integration_test.go, Task 27):
//
//   - refdata: ManifestSummary + InstrumentDef + ManifestSummary
//   - initial snapshot: empty-book bootstrap (anchor=0, K=0)
//   - deltas: OrderAdd(perSeq=1), OrderAdd(perSeq=2), OrderCancel(perSeq=3)
//   - heartbeat at mktSeq=4 to drain the reorder buffer (ensures mkt seq=3 is
//     classified before the periodic snapshot is processed)
//   - periodic snapshot at anchor=3, K=3: exactly {orderID=1001, price=100, qty=10}
//
// Reorder-buffer note: with ReorderWindow=1, a frame at seq N is not classified
// until seq N+1 arrives.  The heartbeat at mkt seq=4 forces mkt seq=3 to be
// classified (highestMktdataSeq=3) before the periodic snapshot is processed,
// satisfying SNAP.ANCHOR_IS_MKTDATA_SEQ (anchor=3 ≤ highestMktdataSeq=3).
func buildMBOGoldenEntries() []goldenPcapEntry {
	const ch = uint8(1)
	const instrID = uint32(600)
	var entries []goldenPcapEntry

	addRef := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenRefDataPort})
	}
	addSnap := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenSnapDataPort})
	}
	addMkt := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenMktDataPort})
	}

	// --- refdata bootstrap ---
	// ManifestSummary(valid=1, seq=1, count=1)
	mf1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeManifest, 24, func(b *wb.Body) {
			b.U8(ch)
			b.U8(1)
			b.Pad(2)
			b.U16(1)
			b.Pad(2)
			b.U32(1)
			b.U64(100_000)
		}).Bytes()
	addRef(mf1, 1)

	// InstrumentDefinition (80 bytes)
	idef := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID  body[0..3]
			b.Pad(69)      // opaque fields  body[4..72]
			b.U8(0)        // priceBound=0   body[73]
			b.U16(1)       // Manifest Seq=1 body[74..75]
		}).Bytes()
	addRef(idef, 2)

	// ManifestSummary again (closes bootstrap cycle)
	addRef(mf1, 3)

	// --- initial snapshot: empty-book bootstrap (anchor=0, K=0) ---
	// SnapshotBegin: instrID=600, anchor=0, totalOrders=0, snapID=1, K=0
	snapBegin1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID
			b.U64(0)       // Anchor Seq = 0
			b.U32(0)       // Total Orders = 0
			b.U32(1)       // Snapshot ID = 1
			b.U32(0)       // Last Instrument Seq (K) = 0
			b.Pad(8)       // padding
		}).Bytes()
	addSnap(snapBegin1, 1)

	// SnapshotEnd for initial bootstrap
	snapEnd1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotEnd, 20, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID
			b.U64(0)       // Anchor Seq = 0
			b.U32(1)       // Snapshot ID = 1
		}).Bytes()
	addSnap(snapEnd1, 2)

	// --- mktdata deltas ---
	// OrderAdd(perSeq=1, orderID=1001, price=100, qty=10) at mktSeq=1
	oa1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID
			b.U16(1)       // Source ID
			b.U8(0)        // Side = bid
			b.U8(0)        // OrderFlags = 0
			b.U32(1)       // Per-Instrument Seq = 1
			b.U64(1001)    // Order ID
			b.U64(1000)    // Enter Timestamp
			b.I64(100)     // Price
			b.U64(10)      // Quantity
			b.Pad(4)       // Reserved
		}).Bytes()
	addMkt(oa1, 1)

	// OrderAdd(perSeq=2, orderID=1002, price=200, qty=5) at mktSeq=2
	oa2 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeOrderAdd, 52, func(b *wb.Body) {
			b.U32(instrID)
			b.U16(1)
			b.U8(0)
			b.U8(0)
			b.U32(2) // Per-Instrument Seq = 2
			b.U64(1002)
			b.U64(1001)
			b.I64(200)
			b.U64(5)
			b.Pad(4)
		}).Bytes()
	addMkt(oa2, 2)

	// OrderCancel(perSeq=3, orderID=1002) at mktSeq=3
	// Delta book at K=3: {1001: side=bid, price=100, qty=10}
	oc1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeOrderCancel, 32, func(b *wb.Body) {
			b.U32(instrID)
			b.U16(1)
			b.Pad(2)
			b.U32(3) // Per-Instrument Seq = 3
			b.U64(1002)
			b.U64(0) // Reserved
		}).Bytes()
	addMkt(oc1, 3)

	// Heartbeat at mktSeq=4: forces mkt seq=3 to be classified (drains reorder
	// buffer) so that highestMktdataSeq=3 when the periodic snapshot is processed.
	// Body[0] = channel ID (heartbeat channel_id must equal frame channel_id to
	// avoid HEARTBEAT.CHANNEL_ID_MATCH should-violation).
	hb := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.U8(ch); b.Pad(11) }).Bytes()
	addMkt(hb, 4)

	// anchorMktSeq = 3 (the mktdata frame seq at the last delta).
	// The periodic snapshot uses anchor=3.

	// --- periodic snapshot at anchor=3, K=3 ---
	// Book: {1001: side=bid, price=100, qty=10}
	snapBegin2 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) {
			b.U32(instrID)
			b.U64(3) // Anchor Seq = 3
			b.U32(1) // Total Orders = 1
			b.U32(2) // Snapshot ID = 2
			b.U32(3) // Last Instrument Seq (K) = 3
			b.Pad(8)
		}).Bytes()
	addSnap(snapBegin2, 3)

	snapOrder1 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotOrder, 44, func(b *wb.Body) {
			b.U32(2)    // Snapshot ID = 2
			b.U64(1001) // Order ID
			b.U8(0)     // Side = bid
			b.U8(0)     // OrderFlags = 0
			b.Pad(2)    // padding
			b.U64(1000) // Enter Timestamp
			b.I64(100)  // Price
			b.U64(10)   // Quantity
		}).Bytes()
	addSnap(snapOrder1, 4)

	snapEnd2 := wb.Frame(wire.MagicMBO).Channel(ch).
		Msg(wire.TypeSnapshotEnd, 20, func(b *wb.Body) {
			b.U32(instrID)
			b.U64(3) // Anchor Seq = 3
			b.U32(2) // Snapshot ID = 2
		}).Bytes()
	addSnap(snapEnd2, 5)

	return entries
}

// withSeq returns a copy of the raw frame with the Sequence field at bytes [4:12]
// set to seq (little-endian).  This is safe because wirebuild always emits a
// 24-byte frame header; seq lives at offset 4.
func withSeq(raw []byte, seq uint64) []byte {
	out := make([]byte, len(raw))
	copy(out, raw)
	// Frame header layout: magic(2) schema(1) channel(1) seq(8) sendTS(8) ...
	// seq starts at byte 4.
	out[4] = byte(seq)
	out[5] = byte(seq >> 8)
	out[6] = byte(seq >> 16)
	out[7] = byte(seq >> 24)
	out[8] = byte(seq >> 32)
	out[9] = byte(seq >> 40)
	out[10] = byte(seq >> 48)
	out[11] = byte(seq >> 56)
	return out
}

// ---- TOB golden capture ----

// buildTOBGoldenEntries builds a conformant TOB session:
//   - refdata: ManifestSummary + InstrumentDef + ManifestSummary
//   - one Quote for the registered instrument (known instrument → silent)
func buildTOBGoldenEntries() []goldenPcapEntry {
	const ch = uint8(1)
	const instrID = uint32(700)
	var entries []goldenPcapEntry

	addRef := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenRefDataPort})
	}
	addMkt := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenMktDataPort})
	}

	// ManifestSummary(valid=1, seq=1, count=1)
	mf := wb.Frame(wire.MagicTOB).Channel(ch).
		Msg(wire.TypeManifest, 24, func(b *wb.Body) {
			b.U8(ch)
			b.U8(1)
			b.Pad(2)
			b.U16(1)
			b.Pad(2)
			b.U32(1)
			b.U64(100_000)
		}).Bytes()
	addRef(mf, 1)

	// InstrumentDefinition (80 bytes, TOB layout)
	idef := wb.Frame(wire.MagicTOB).Channel(ch).
		Msg(wire.TypeInstrumentDef, 80, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID  body[0..3]
			b.Pad(70)      // opaque fields  body[4..73]
			b.U16(1)       // Manifest Seq=1 body[74..75]
		}).Bytes()
	addRef(idef, 2)

	// ManifestSummary again (closes bootstrap cycle)
	addRef(mf, 3)

	// Quote for the known instrument (Source ID=1 ∈ [1,1023] → conformant)
	quote := wb.Frame(wire.MagicTOB).Channel(ch).
		Msg(wire.TypeQuote, 60, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID  body[0]
			b.U16(1)             // SourceID       body[4]
			b.U8(0x03)           // UpdateFlags: bid+ask body[6]
			b.U8(0)              // Reserved      body[7]
			b.U64(1_000_000_000) // SourceTimestamp body[8]
			b.I64(100)           // BidPrice      body[16]
			b.U64(10)            // BidQty        body[24]
			b.I64(200)           // AskPrice      body[32]
			b.U64(10)            // AskQty        body[40]
			b.U16(1)             // BidSourceCount body[48]
			b.U16(1)             // AskSourceCount body[50]
			b.Pad(4)             // Reserved      body[52]
		}).Bytes()
	addMkt(quote, 1)

	return entries
}

// ---- Midpoint golden capture ----

// buildMidpointGoldenEntries builds a conformant Midpoint session:
//   - refdata: ManifestSummary + InstrumentDef (defaultMethod=1, priceBound=0) +
//     ManifestSummary
//   - one Midpoint message with Method=1 and midPrice=500 (non-negative → pass)
func buildMidpointGoldenEntries() []goldenPcapEntry {
	const ch = uint8(1)
	const instrID = uint32(800)
	var entries []goldenPcapEntry

	addRef := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenRefDataPort})
	}
	addMkt := func(raw []byte, seq uint64) {
		entries = append(entries, goldenPcapEntry{payload: withSeq(raw, seq), dstPort: goldenMktDataPort})
	}

	// ManifestSummary(valid=1, seq=1, count=1)
	mf := wb.Frame(wire.MagicMid).Channel(ch).
		Msg(wire.TypeManifest, 24, func(b *wb.Body) {
			b.U8(ch)
			b.U8(1)
			b.Pad(2)
			b.U16(1)
			b.Pad(2)
			b.U32(1)
			b.U64(100_000)
		}).Bytes()
	addRef(mf, 1)

	// InstrumentDefinition (64 bytes, Midpoint layout)
	// defaultMethod=1 (non-zero → MID.METHOD0_REQUIRES_DEFAULT won't fire for Method=1)
	// priceBound=0 (no price constraint)
	idef := wb.Frame(wire.MagicMid).Channel(ch).
		Msg(wire.TypeInstrumentDef, 64, func(b *wb.Body) {
			b.U32(instrID) // Instrument ID  body[0:4]
			b.Pad(34)      // opaque fields  body[4:38]
			b.U8(1)        // Default Method body[38] = 1 (non-zero)
			b.U8(0)        // Price Bound    body[39] = 0 (no constraint)
			b.Pad(16)      // opaque fields  body[40:56]
			b.U16(1)       // Manifest Seq   body[56:58]
			b.Pad(2)       // Reserved       body[58:60]
		}).Bytes()
	addRef(idef, 2)

	// ManifestSummary again
	addRef(mf, 3)

	// Midpoint message: Method=1 (explicit, non-zero), midPrice=500 (positive)
	mid := wb.Frame(wire.MagicMid).Channel(ch).
		Msg(wire.TypeMidpoint, 40, func(b *wb.Body) {
			b.U32(instrID)       // InstrumentID   body[0:4]
			b.Pad(2)             // Reserved        body[4:6]
			b.U8(1)              // Method=1        body[6]
			b.U8(0)              // QualityFlags    body[7]
			b.U64(1_000_000_000) // BookTS          body[8:16]
			b.U64(1_000_000_001) // ComputeTS       body[16:24]
			b.I64(500)           // MidPrice=500    body[24:32]
			b.Pad(4)             // Reserved        body[32:36]
		}).Bytes()
	addMkt(mid, 1)

	return entries
}

// ---- test runner ----

// goldenOpts returns RunOpts for a golden pcap test with all cadence flags off.
func goldenOpts(feed core.Feed, pcapPath string) RunOpts {
	return RunOpts{
		Cfg: engine.Config{
			Feed:                feed,
			Strict:              false,
			OracleConfirmCycles: 2,
			ReorderWindow:       1,
			// All Expect* left at zero → cadence rules downgrade to info/NA.
		},
		MktDataPort:  goldenMktDataPort,
		RefDataPort:  goldenRefDataPort,
		SnapshotPort: goldenSnapDataPort,
		PcapPath:     pcapPath,
	}
}

// runGoldenCapture runs the engine directly over the golden entries using a
// capturing reporter and returns the count of Must+Violation findings.
// This is the stronger assertion: it counts must-violations without going
// through Run() (which only exposes an exit code).
func runGoldenCapture(t *testing.T, feed core.Feed, magic uint16, entries []goldenPcapEntry) int {
	t.Helper()
	gc := &goldenCapture{}
	cfg := engine.Config{
		Feed:                feed,
		Strict:              false,
		OracleConfirmCycles: 2,
		ReorderWindow:       1,
	}
	eng := engine.New(cfg, gc)

	portMap := goldenPortMap
	for _, e := range entries {
		port, ok := portMap[int(e.dstPort)]
		if !ok {
			continue
		}
		f, sf := wire.Decode(e.payload, magic)
		eng.Process(f, port, sf)
	}
	eng.Flush()
	eng.EndRun()

	return mustViolationCount(gc)
}

// writeGoldenPcapToTemp writes the golden pcap to a file in t.TempDir() and
// returns that path.  The primary golden tests use this so each run gets a
// freshly generated pcap independent of the committed testdata/ fixtures.
func writeGoldenPcapToTemp(t *testing.T, name string, entries []goldenPcapEntry) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	writeGoldenPcap(t, path, entries)
	return path
}

// ---- golden tests ----

// TestGoldenMBO runs Run() over a conformant MBO capture and asserts:
//   - exit code 0 (no must-violations via Aggregator)
//   - zero must-violations via direct engine capture
func TestGoldenMBO(t *testing.T) {
	entries := buildMBOGoldenEntries()
	pcapPath := writeGoldenPcapToTemp(t, "conformant_mbo.pcap", entries)

	// Assertion 1: Run() returns 0.
	code := Run(goldenOpts(core.FeedMBO, pcapPath))
	if code != 0 {
		t.Errorf("MBO golden: Run() returned exit code %d; expected 0", code)
	}

	// Assertion 2: direct engine pass shows zero must-Violations.
	if n := runGoldenCapture(t, core.FeedMBO, wire.MagicMBO, entries); n != 0 {
		t.Errorf("MBO golden: engine direct capture has %d must-Violation(s); expected 0", n)
	}
}

// TestGoldenTOB runs Run() over a conformant TOB capture and asserts:
//   - exit code 0
//   - zero must-violations
func TestGoldenTOB(t *testing.T) {
	entries := buildTOBGoldenEntries()
	pcapPath := writeGoldenPcapToTemp(t, "conformant_tob.pcap", entries)

	code := Run(goldenOpts(core.FeedTOB, pcapPath))
	if code != 0 {
		t.Errorf("TOB golden: Run() returned exit code %d; expected 0", code)
	}

	if n := runGoldenCapture(t, core.FeedTOB, wire.MagicTOB, entries); n != 0 {
		t.Errorf("TOB golden: engine direct capture has %d must-Violation(s); expected 0", n)
	}
}

// TestGoldenMidpoint runs Run() over a conformant Midpoint capture and asserts:
//   - exit code 0
//   - zero must-violations
func TestGoldenMidpoint(t *testing.T) {
	entries := buildMidpointGoldenEntries()
	pcapPath := writeGoldenPcapToTemp(t, "conformant_midpoint.pcap", entries)

	code := Run(goldenOpts(core.FeedMidpoint, pcapPath))
	if code != 0 {
		t.Errorf("Midpoint golden: Run() returned exit code %d; expected 0", code)
	}

	if n := runGoldenCapture(t, core.FeedMidpoint, wire.MagicMid, entries); n != 0 {
		t.Errorf("Midpoint golden: engine direct capture has %d must-Violation(s); expected 0", n)
	}
}

// TestGoldenPcapFixtures compares the byte-for-byte output of each golden entry
// builder against the committed testdata/*.pcap files.  This is the fixture
// drift guard: if the wire encoder or frame layout changes, the generated bytes
// will differ from the committed file and the test fails.
//
// To regenerate fixtures after an intentional protocol change, set the
// environment variable TESTDATA_UPDATE=1 before running the test:
//
//	TESTDATA_UPDATE=1 go test -run TestGoldenPcapFixtures ./...
func TestGoldenPcapFixtures(t *testing.T) {
	const testdataDir = "testdata"
	update := os.Getenv("TESTDATA_UPDATE") == "1"

	if update {
		if err := os.MkdirAll(testdataDir, 0o755); err != nil {
			t.Fatalf("MkdirAll testdata: %v", err)
		}
	}

	cases := []struct {
		name    string
		entries []goldenPcapEntry
	}{
		{"conformant_mbo.pcap", buildMBOGoldenEntries()},
		{"conformant_tob.pcap", buildTOBGoldenEntries()},
		{"conformant_midpoint.pcap", buildMidpointGoldenEntries()},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Generate fresh bytes into a temp file.
			tmpPath := filepath.Join(t.TempDir(), tc.name)
			writeGoldenPcap(t, tmpPath, tc.entries)
			got, err := os.ReadFile(tmpPath)
			if err != nil {
				t.Fatalf("read generated pcap: %v", err)
			}

			fixturePath := filepath.Join(testdataDir, tc.name)
			if update {
				if err := os.WriteFile(fixturePath, got, 0o644); err != nil {
					t.Fatalf("write fixture %q: %v", fixturePath, err)
				}
				t.Logf("updated %s (%d bytes)", fixturePath, len(got))
				return
			}

			want, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read committed fixture %q (run with TESTDATA_UPDATE=1 to regenerate): %v", fixturePath, err)
			}
			if len(got) != len(want) {
				t.Errorf("fixture byte-length mismatch: generated %d bytes, committed %d bytes (run with TESTDATA_UPDATE=1 to regenerate)", len(got), len(want))
				return
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("fixture byte drift at offset %d: generated 0x%02x, committed 0x%02x (run with TESTDATA_UPDATE=1 to regenerate)", i, got[i], want[i])
					return
				}
			}
		})
	}
}

// TestGoldenCommittedFixtures runs Run() over the committed testdata/*.pcap
// fixtures and asserts exit code 0.  This is the regression guard: a stale or
// hand-corrupted fixture causes the test to fail, revealing the drift.
func TestGoldenCommittedFixtures(t *testing.T) {
	cases := []struct {
		name string
		feed core.Feed
		file string
	}{
		{"mbo", core.FeedMBO, "testdata/conformant_mbo.pcap"},
		{"tob", core.FeedTOB, "testdata/conformant_tob.pcap"},
		{"midpoint", core.FeedMidpoint, "testdata/conformant_midpoint.pcap"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := os.Stat(tc.file); err != nil {
				t.Skipf("committed fixture %q not present: %v", tc.file, err)
			}
			code := Run(goldenOpts(tc.feed, tc.file))
			if code != 0 {
				t.Errorf("committed fixture %s: Run() returned exit code %d; expected 0", tc.file, code)
			}
		})
	}
}

// Ensure goldenCapture satisfies report.Reporter at compile time.
var _ report.Reporter = (*goldenCapture)(nil)
