package input

// pcapng_test.go — the format tests, built byte by byte.
//
// The files here are assembled by hand rather than with a writer library for the
// same reason the reader is hand-written: the field under test, epb_dropcount, is
// a per-packet option no Go pcapng writer emits and gopacket's reader discards.
// A fixture produced by a library that cannot write the field could not test the
// field.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/gopacket/layers"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// ngBuilder assembles a pcapng file block by block in a chosen byte order.
type ngBuilder struct {
	bo  binary.ByteOrder
	buf []byte
}

func newNgBuilder(bo binary.ByteOrder) *ngBuilder { return &ngBuilder{bo: bo} }

func (b *ngBuilder) u16(v uint16) []byte {
	out := make([]byte, 2)
	b.bo.PutUint16(out, v)
	return out
}

func (b *ngBuilder) u32(v uint32) []byte {
	out := make([]byte, 4)
	b.bo.PutUint32(out, v)
	return out
}

func (b *ngBuilder) u64(v uint64) []byte {
	out := make([]byte, 8)
	b.bo.PutUint64(out, v)
	return out
}

// pad returns v padded up to a 4-byte boundary, the format's alignment.
func pad(v []byte) []byte {
	if n := len(v) % 4; n != 0 {
		return append(append([]byte{}, v...), make([]byte, 4-n)...)
	}
	return v
}

// option renders one option: code, value length, value padded to 4 bytes.
func (b *ngBuilder) option(code uint16, val []byte) []byte {
	out := append([]byte{}, b.u16(code)...)
	out = append(out, b.u16(uint16(len(val)))...)
	return append(out, pad(val)...)
}

// block appends one block: type, total length, body, total length again.
func (b *ngBuilder) block(typ uint32, body []byte) {
	body = pad(body)
	total := uint32(12 + len(body))
	b.buf = append(b.buf, b.u32(typ)...)
	b.buf = append(b.buf, b.u32(total)...)
	b.buf = append(b.buf, body...)
	b.buf = append(b.buf, b.u32(total)...)
}

// sectionHeader appends a Section Header Block. Its byte-order magic is what
// tells a reader how to read every field after it — including the block's own
// length — so it is written in the builder's order and read back from it.
func (b *ngBuilder) sectionHeader() {
	body := append([]byte{}, b.u32(ngByteOrderMagic)...)
	body = append(body, b.u16(1)...)                  // major
	body = append(body, b.u16(0)...)                  // minor
	body = append(body, b.u64(0xffffffffffffffff)...) // section length: not specified
	b.block(ngBlockSectionHeader, body)
}

// iface appends an Interface Description Block with the given options appended.
func (b *ngBuilder) iface(linkType layers.LinkType, snapLen uint32, opts ...[]byte) {
	body := append([]byte{}, b.u16(uint16(linkType))...)
	body = append(body, b.u16(0)...) // reserved
	body = append(body, b.u32(snapLen)...)
	for _, o := range opts {
		body = append(body, o...)
	}
	b.block(ngBlockInterfaceDescription, body)
}

// enhanced appends an Enhanced Packet Block. A non-nil drops writes the
// epb_dropcount option; nil writes no options at all, which is what a recorder
// that lost nothing before this packet emits.
func (b *ngBuilder) enhanced(ifaceID uint32, ts uint64, data []byte, drops *uint64) {
	body := append([]byte{}, b.u32(ifaceID)...)
	body = append(body, b.u32(uint32(ts>>32))...)
	body = append(body, b.u32(uint32(ts))...)
	body = append(body, b.u32(uint32(len(data)))...) // captured length
	body = append(body, b.u32(uint32(len(data)))...) // original length
	body = append(body, pad(data)...)
	if drops != nil {
		body = append(body, b.option(ngOptEPBDropCount, b.u64(*drops))...)
		body = append(body, b.option(ngOptEndOfOptions, nil)...)
	}
	b.block(ngBlockEnhancedPacket, body)
}

// simple appends a Simple Packet Block: no timestamp, no options, and so no way
// to admit a drop.
func (b *ngBuilder) simple(data []byte) {
	body := append([]byte{}, b.u32(uint32(len(data)))...)
	body = append(body, pad(data)...)
	b.block(ngBlockSimplePacket, body)
}

// obsolete appends the deprecated Packet Block, whose drop count is a 16-bit
// header field rather than an option.
func (b *ngBuilder) obsolete(ifaceID, drops uint16, ts uint64, data []byte) {
	body := append([]byte{}, b.u16(ifaceID)...)
	body = append(body, b.u16(drops)...)
	body = append(body, b.u32(uint32(ts>>32))...)
	body = append(body, b.u32(uint32(ts))...)
	body = append(body, b.u32(uint32(len(data)))...)
	body = append(body, b.u32(uint32(len(data)))...)
	body = append(body, pad(data)...)
	b.block(ngBlockPacket, body)
}

// enhancedShort appends an Enhanced Packet Block declaring more bytes on the
// wire than it holds — a capture recorded with a snap length shorter than the
// feed's frames. Both lengths are the format's, and only one of them is the
// number of bytes the block actually carries.
func (b *ngBuilder) enhancedShort(ifaceID uint32, ts uint64, data []byte, origLen int) {
	body := append([]byte{}, b.u32(ifaceID)...)
	body = append(body, b.u32(uint32(ts>>32))...)
	body = append(body, b.u32(uint32(ts))...)
	body = append(body, b.u32(uint32(len(data)))...) // captured length
	body = append(body, b.u32(uint32(origLen))...)   // original length, on the wire
	body = append(body, pad(data)...)
	b.block(ngBlockEnhancedPacket, body)
}

// mark is the offset the next block will start at, so a test can cut the file
// at an exact position inside that block.
func (b *ngBuilder) mark() int { return len(b.buf) }

// writeCut puts the first n bytes of the assembled file in a temp dir. A
// capture a recorder was killed part-way through writing is the shape this
// produces.
func (b *ngBuilder) writeCut(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cut.pcapng")
	if err := os.WriteFile(path, b.buf[:n], 0o644); err != nil {
		t.Fatalf("write pcapng: %v", err)
	}
	return path
}

// write puts the assembled file in a temp dir and returns its path.
func (b *ngBuilder) write(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.pcapng")
	if err := os.WriteFile(path, b.buf, 0o644); err != nil {
		t.Fatalf("write pcapng: %v", err)
	}
	return path
}

// drain reads a source to EOF and returns every datagram it yielded.
func drain(t *testing.T, src *PcapSource) []Datagram {
	t.Helper()
	var out []Datagram
	for {
		dg, ok, err := src.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return out
		}
		out = append(out, dg)
	}
}

// mktOnly maps a single mktdata port, the minimum a replay needs.
var mktOnly = map[int]core.Port{7001: core.PortMktData}

// --- the symptom in the issue: a pcapng file is opened, not refused ---

// A pcapng file used to fail at open with `Unknown magic a0d0d0a`, which is its
// Section Header Block read as if it were a legacy pcap header. It is the format
// the recorder archives, so refusing it meant the validator could not read the
// archive it exists to judge. Both byte orders, because the header's magic is a
// palindrome precisely so that either is legal.
func TestPcapSourceReadsPcapng(t *testing.T) {
	for _, tc := range []struct {
		name string
		bo   binary.ByteOrder
	}{
		{"little endian", binary.LittleEndian},
		{"big endian", binary.BigEndian},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newNgBuilder(tc.bo)
			b.sectionHeader()
			b.iface(layers.LinkTypeEthernet, 65535)
			b.enhanced(0, 1_000_000_000, udpEthPacket(t, 0, 7001, []byte("hello mktdata")), nil)
			b.enhanced(0, 2_000_000_000, udpEthPacket(t, 1, 7002, []byte("hello refdata")), nil)

			src, err := NewPcapSource(b.write(t), map[int]core.Port{7001: core.PortMktData, 7002: core.PortRefData})
			if err != nil {
				t.Fatalf("NewPcapSource: %v", err)
			}
			defer func() { _ = src.Close() }()

			dgs := drain(t, src)
			if len(dgs) != 2 {
				t.Fatalf("got %d datagrams, want 2", len(dgs))
			}
			if dgs[0].Port != core.PortMktData || !bytes.Equal(dgs[0].Raw, []byte("hello mktdata")) {
				t.Errorf("first datagram: port %v payload %q", dgs[0].Port, dgs[0].Raw)
			}
			if dgs[1].Port != core.PortRefData || !bytes.Equal(dgs[1].Raw, []byte("hello refdata")) {
				t.Errorf("second datagram: port %v payload %q", dgs[1].Port, dgs[1].Raw)
			}
			// The source address is part of a channel instance's identity, so a
			// pcapng replay has to carry it exactly as the legacy path does.
			if got := dgs[0].Src.String(); got != "10.0.0.1" {
				t.Errorf("source address %s, want 10.0.0.1", got)
			}
			// Default resolution is microseconds when no if_tsresol says otherwise.
			if want := time.Unix(1000, 0); !dgs[0].RecvTS.Equal(want) {
				t.Errorf("timestamp %v, want %v", dgs[0].RecvTS, want)
			}
		})
	}
}

// --- the field the conversion was dropping ---

// epb_dropcount is the recorder's own admission of what it failed to record, and
// it reaches the engine per datagram: the drops precede this one, so they taint
// the windows it is about to be judged against.
func TestPcapngSurfacesDropCount(t *testing.T) {
	seven := uint64(7)
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("first")), nil)
	b.enhanced(0, 0, udpEthPacket(t, 1, 7001, []byte("after the drop")), &seven)
	b.enhanced(0, 0, udpEthPacket(t, 2, 7001, []byte("clean again")), nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	dgs := drain(t, src)
	if len(dgs) != 3 {
		t.Fatalf("got %d datagrams, want 3", len(dgs))
	}
	want := []uint64{0, 7, 0}
	for i, dg := range dgs {
		if dg.CaptureDrops != want[i] {
			t.Errorf("datagram %d: CaptureDrops = %d, want %d", i, dg.CaptureDrops, want[i])
		}
	}
	if got := src.CaptureDrops(); got != 7 {
		t.Errorf("CaptureDrops total = %d, want 7", got)
	}
}

// A drop reported on a packet this reader skips is loss all the same. Charging it
// to the next datagram the engine does see is what keeps it from being discarded
// with the packet that admitted it — and the trailing one, which no datagram
// follows, still has to reach the run's total.
func TestPcapngCarriesDropsAcrossSkippedPackets(t *testing.T) {
	three, five, eleven := uint64(3), uint64(5), uint64(11)
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("first")), nil)
	// Two drops admitted on packets bound for a port that is not mapped.
	b.enhanced(0, 0, udpEthPacket(t, 1, 9999, []byte("unmapped")), &three)
	b.enhanced(0, 0, udpEthPacket(t, 2, 9999, []byte("unmapped")), &five)
	b.enhanced(0, 0, udpEthPacket(t, 3, 7001, []byte("second")), nil)
	// Admitted after the last mapped packet: no datagram can carry it.
	b.enhanced(0, 0, udpEthPacket(t, 4, 9999, []byte("unmapped")), &eleven)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	dgs := drain(t, src)
	if len(dgs) != 2 {
		t.Fatalf("got %d datagrams, want 2", len(dgs))
	}
	if dgs[0].CaptureDrops != 0 {
		t.Errorf("first datagram: CaptureDrops = %d, want 0", dgs[0].CaptureDrops)
	}
	if dgs[1].CaptureDrops != 8 {
		t.Errorf("second datagram: CaptureDrops = %d, want 8 (3+5 from skipped packets)", dgs[1].CaptureDrops)
	}
	if got := src.CaptureDrops(); got != 19 {
		t.Errorf("CaptureDrops total = %d, want 19 (3+5+11, the last after every datagram)", got)
	}
}

// Loss admitted after the last mapped datagram has no datagram left to carry it.
//
// It reaches the run's total, which is what the report shows, and it used to
// reach nothing else: the end-of-run findings were graded as though the capture
// had been clean over the window that loss falls in, because the total is read
// after they are produced. PendingDrops is what the run loop hands the engine
// before it flushes, so the last window is graded on the same terms as every
// other one.
func TestPcapngResidualDropsAreReadableAfterEOF(t *testing.T) {
	drops := uint64(4)
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("the last datagram")), nil)
	// Admitted on a packet no port role owns, so no datagram follows it.
	b.enhanced(0, 0, udpEthPacket(t, 1, 9999, []byte("unmapped")), &drops)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	got := drain(t, src)
	if len(got) != 1 {
		t.Fatalf("got %d datagram(s), want 1", len(got))
	}
	if got[0].CaptureDrops != 0 {
		t.Errorf("the datagram before the admission carries %d drop(s), want 0: the drops "+
			"follow it", got[0].CaptureDrops)
	}
	if n := src.PendingDrops(); n != drops {
		t.Errorf("PendingDrops() after EOF = %d, want %d: loss no datagram carried is loss "+
			"the end-of-run findings are still graded inside", n, drops)
	}
	if n := src.CaptureDrops(); n != drops {
		t.Errorf("CaptureDrops() = %d, want %d", n, drops)
	}
}

// A legacy pcap has no field in which a recorder can admit a drop, so it reports
// none — an absence of accounting, not an assertion that nothing was lost. This
// is what the conversion in the issue's workaround costs.
func TestLegacyPcapAdmitsNoDrops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pcap")
	writePcap(t, path, [][]byte{[]byte("one"), []byte("two")}, []uint16{7001, 7001})

	src, err := NewPcapSource(path, mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	for _, dg := range drain(t, src) {
		if dg.CaptureDrops != 0 {
			t.Errorf("legacy pcap reported %d drops", dg.CaptureDrops)
		}
	}
	if got := src.CaptureDrops(); got != 0 {
		t.Errorf("CaptureDrops total = %d, want 0", got)
	}
}

// --- timestamps ---

// Nanosecond timestamps are the point of archiving the segment, so if_tsresol has
// to be honoured rather than assumed: read at the default microseconds, a
// nanosecond capture's clock runs a thousand times slow. if_tsoffset is how a
// capture stores a relative clock and is added on top.
func TestPcapngHonoursTimestampResolutionAndOffset(t *testing.T) {
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535,
		b.option(ngOptIfTsResol, []byte{9}),             // 10^-9: nanoseconds
		b.option(ngOptIfTsOffset, b.u64(1_700_000_000)), // seconds added to every timestamp
	)
	b.enhanced(0, 42_000_000_123, udpEthPacket(t, 0, 7001, []byte("ns")), nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	dgs := drain(t, src)
	if len(dgs) != 1 {
		t.Fatalf("got %d datagrams, want 1", len(dgs))
	}
	want := time.Unix(1_700_000_042, 123).UTC()
	if !dgs[0].RecvTS.Equal(want) {
		t.Errorf("timestamp %v, want %v", dgs[0].RecvTS, want)
	}
}

// --- the rest of the format an archive may contain ---

// A file with blocks this tool does not judge in it — interface statistics, name
// resolution, anything future — stays readable: unknown blocks are skipped by
// their own length. The same walk carries the two other packet-bearing block
// types.
func TestPcapngSkipsUnknownBlocksAndReadsEveryPacketBlock(t *testing.T) {
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("enhanced")), nil)
	// An Interface Statistics Block (type 5) between packets.
	b.block(0x00000005, append(append([]byte{}, b.u32(0)...), make([]byte, 8)...))
	b.simple(udpEthPacket(t, 1, 7001, []byte("simple")))
	b.obsolete(0, 4, 0, udpEthPacket(t, 2, 7001, []byte("obsolete")))
	// A vendor block nobody has to understand (type 0x40000001, "custom").
	b.block(0x40000001, b.u32(0xdeadbeef))
	b.enhanced(0, 0, udpEthPacket(t, 3, 7001, []byte("last")), nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	dgs := drain(t, src)
	var got []string
	for _, dg := range dgs {
		got = append(got, string(dg.Raw))
	}
	want := []string{"enhanced", "simple", "obsolete", "last"}
	if len(got) != len(want) {
		t.Fatalf("payloads %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("payload %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The obsolete Packet Block carries its drop count in the header, not an option.
	if dgs[2].CaptureDrops != 4 {
		t.Errorf("packet block: CaptureDrops = %d, want 4", dgs[2].CaptureDrops)
	}
	if got := src.CaptureDrops(); got != 4 {
		t.Errorf("CaptureDrops total = %d, want 4", got)
	}
}

// Interface IDs are section-scoped, so a second section restarts the numbering.
// Carrying the first section's table over would resolve a packet against the
// wrong interface — and so against the wrong link type and clock.
func TestPcapngSecondSectionRestartsInterfaces(t *testing.T) {
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 1_000_000, udpEthPacket(t, 0, 7001, []byte("section one")), nil)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535, b.option(ngOptIfTsResol, []byte{9}))
	b.enhanced(0, 5_000_000_000, udpEthPacket(t, 1, 7001, []byte("section two")), nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	dgs := drain(t, src)
	if len(dgs) != 2 {
		t.Fatalf("got %d datagrams, want 2", len(dgs))
	}
	// The second section's own if_tsresol applies, not the first's default.
	if want := time.Unix(5, 0); !dgs[1].RecvTS.Equal(want) {
		t.Errorf("second section timestamp %v, want %v", dgs[1].RecvTS, want)
	}
}

// --- what must fail rather than be read as complete ---

// A capture that ends mid-block is a truncated archive, and reading it as a
// complete one would report the frames before the tear as the whole story. The
// run loop turns this error into exit 2 and a `read_error` in the report.
func TestPcapngTruncatedFileErrors(t *testing.T) {
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("whole")), nil)
	b.enhanced(0, 0, udpEthPacket(t, 1, 7001, []byte("torn in half")), nil)

	full := b.buf
	path := filepath.Join(t.TempDir(), "truncated.pcapng")
	if err := os.WriteFile(path, full[:len(full)-24], 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	src, err := NewPcapSource(path, mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	if _, ok, err := src.Next(); err != nil || !ok {
		t.Fatalf("first datagram: ok=%v err=%v, want the whole packet before the tear", ok, err)
	}
	_, ok, err := src.Next()
	if err == nil {
		t.Fatalf("truncated capture read as complete (ok=%v); want an error", ok)
	}
}

// A file that ends exactly at the start of a block *body* is the case the
// truncation test above cannot reach, and the one that used to read as a
// complete capture.
//
// io.ReadFull reports bare io.EOF when it reads no bytes and
// io.ErrUnexpectedEOF only when it reads some, and pcap.go turns io.EOF into a
// clean end of file. Cut mid-body — the test above — some bytes are read and the
// error is already right; cut at the boundary, none are, and the run exited 0
// with an empty read_error over a segment it had only partly read. The
// end-of-file signal has to mean a whole file, or every other check in the tool
// is being reported over an unknown fraction of the archive.
func TestPcapngFileEndingAtABlockBoundaryErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		// build appends the whole file and returns the offset the last block
		// starts at; the file is cut 8 bytes past it, which is the type/length
		// pair read and the body never begun.
		build func(*ngBuilder) int
	}{
		{
			// The recorder was killed between writing a packet's header and its
			// body, which is where a segment on a full disk ends.
			name: "trailing packet block cut to its header",
			build: func(b *ngBuilder) int {
				at := b.mark()
				b.enhanced(0, 0, udpEthPacket(t, 1, 7001, []byte("never written")), nil)
				return at
			},
		},
		{
			// A section header is worse: its length field cannot even be read
			// until its byte-order magic has been, so the cut lands between two
			// reads of the same block.
			name: "second section header cut before its byte-order magic",
			build: func(b *ngBuilder) int {
				at := b.mark()
				b.sectionHeader()
				return at
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newNgBuilder(binary.LittleEndian)
			b.sectionHeader()
			b.iface(layers.LinkTypeEthernet, 65535)
			b.enhanced(0, 0, udpEthPacket(t, 0, 7001, []byte("whole")), nil)
			at := tc.build(b)

			src, err := NewPcapSource(b.writeCut(t, at+8), mktOnly)
			if err != nil {
				t.Fatalf("NewPcapSource: %v", err)
			}
			defer func() { _ = src.Close() }()

			if _, ok, err := src.Next(); err != nil || !ok {
				t.Fatalf("first datagram: ok=%v err=%v, want the whole packet before the cut", ok, err)
			}
			_, ok, err := src.Next()
			if err == nil {
				t.Fatalf("a capture cut at a block boundary read as a complete one (ok=%v): the "+
					"run exits 0 with an empty read_error over a segment it only partly read", ok)
			}
		})
	}
}

// A packet the capture's snap length cut short is the capture's missing bytes,
// not the publisher's.
//
// Both lengths are in the block, and origLen used to be parsed and never read:
// the surviving bytes went to the decoder, where a frame declaring a length past
// its own datagram reads as publisher truncation and a payload hashed short of
// its twin reads as a divergent duplicate. That is the recorder-artifact-charged-
// to-the-feed error epb_dropcount was threaded in to prevent, from a field the
// reader already had.
func TestPcapngSnaplenTruncatedPacketIsCaptureOwned(t *testing.T) {
	whole := udpEthPacket(t, 0, 7001, []byte("recorded whole"))
	short := udpEthPacket(t, 1, 7001, []byte("cut by the snap length"))

	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 96)
	b.enhancedShort(0, 0, short[:len(short)-20], len(short))
	b.enhanced(0, 0, whole, nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	got := drain(t, src)
	if len(got) != 1 {
		t.Fatalf("got %d datagram(s), want 1: a partly-recorded datagram is not the "+
			"publisher's bytes and must not reach the decoder", len(got))
	}
	if string(got[0].Raw) != "recorded whole" {
		t.Errorf("yielded payload %q, want the whole packet", got[0].Raw)
	}
	// Capture-owned, and it has to reach the engine as such: the sequence number
	// the block carried is now missing from the series, and a gap nothing owns is
	// a gap charged to the publisher.
	if got[0].CaptureDrops != 1 {
		t.Errorf("the datagram after the cut-short one carries %d capture drop(s), want 1",
			got[0].CaptureDrops)
	}
	if n := src.CaptureDrops(); n != 1 {
		t.Errorf("CaptureDrops() = %d, want 1", n)
	}
	if n := src.SnaplenTruncated(); n != 1 {
		t.Errorf("SnaplenTruncated() = %d, want 1: an operator fixes a short snap length by "+
			"re-recording, which is not what they would do about a dropped datagram", n)
	}
}

// A packet naming an interface the section never described cannot be decoded —
// its link type and clock are unknown — so it is an error and not a silent skip.
func TestPcapngUnknownInterfaceErrors(t *testing.T) {
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.iface(layers.LinkTypeEthernet, 65535)
	b.enhanced(3, 0, udpEthPacket(t, 0, 7001, []byte("orphan")), nil)

	src, err := NewPcapSource(b.write(t), mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	if _, _, err := src.Next(); err == nil {
		t.Fatal("packet naming an undescribed interface was accepted")
	}
}

// A file whose leading block is not a section header, or whose byte-order magic
// is not one of the two, is not a pcapng file and must be refused at open.
func TestPcapngBadHeaderRefusedAtOpen(t *testing.T) {
	// The magic identifies the format, so a corrupt byte-order magic gets past the
	// sniff and has to be caught by the reader.
	b := newNgBuilder(binary.LittleEndian)
	b.sectionHeader()
	b.buf[8] = 0x00 // break the byte-order magic
	if _, err := NewPcapSource(b.write(t), mktOnly); err == nil {
		t.Error("a section header with a corrupt byte-order magic was accepted")
	}

	// A file that is neither format at all.
	path := filepath.Join(t.TempDir(), "garbage.pcap")
	if err := os.WriteFile(path, []byte("not a capture file at all"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := NewPcapSource(path, mktOnly); err == nil {
		t.Error("a file in neither capture format was accepted")
	}
}
