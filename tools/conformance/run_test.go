package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
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
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

const (
	testMktDataUDPPort = 17001
)

// writeMBOPcap writes a pcap file containing a single well-formed MBO heartbeat frame
// on the given UDP port. Returns the path to the created pcap file.
func writeMBOPcap(t *testing.T, dir string) string {
	t.Helper()

	// Build a well-formed MBO heartbeat frame using wirebuild.
	// Heartbeat body: TypeHeartbeat (0x01), length = 4 (header only) + 12 body bytes.
	// The spec requires at least one message per frame. We use an empty heartbeat body
	// (12 pad bytes = enough for a minimal heartbeat message with MsgHeader=4 + body=12).
	frameBytes := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
		Bytes()

	pcapPath := filepath.Join(dir, "mbo_heartbeat.pcap")
	f, err := os.Create(pcapPath)
	if err != nil {
		t.Fatalf("create pcap: %v", err)
	}
	defer func() { _ = f.Close() }()

	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatalf("write pcap header: %v", err)
	}

	data := udpEthFrame(t, 0, testMktDataUDPPort, frameBytes)
	ci := gopacket.CaptureInfo{
		Timestamp:     time.Unix(1000, 0),
		CaptureLength: len(data),
		Length:        len(data),
	}
	if err := w.WritePacket(ci, data); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	return pcapPath
}

// writeMBOPcapng writes a **pcapng** file carrying well-formed MBO heartbeat
// frames, with the recorder admitting it failed to record `drops` datagrams
// before the last one.
//
// The blocks are assembled by hand because the field that matters here —
// `epb_dropcount` — is a per-packet option no Go pcapng writer emits, so a
// fixture produced by a library could not carry it. Layout: Section Header,
// Interface Description, then one Enhanced Packet Block per frame.
func writeMBOPcapng(t *testing.T, dir string, drops uint64) string {
	t.Helper()

	bo := binary.LittleEndian
	u16 := func(v uint16) []byte { b := make([]byte, 2); bo.PutUint16(b, v); return b }
	u32 := func(v uint32) []byte { b := make([]byte, 4); bo.PutUint32(b, v); return b }
	u64 := func(v uint64) []byte { b := make([]byte, 8); bo.PutUint64(b, v); return b }

	var out []byte
	block := func(typ uint32, body []byte) {
		if n := len(body) % 4; n != 0 {
			body = append(body, make([]byte, 4-n)...)
		}
		total := u32(uint32(12 + len(body)))
		out = append(out, u32(typ)...)
		out = append(out, total...)
		out = append(out, body...)
		out = append(out, total...)
	}

	// Section Header: byte-order magic, version 1.0, unspecified section length.
	shb := append([]byte{}, u32(0x1a2b3c4d)...)
	shb = append(shb, u16(1)...)
	shb = append(shb, u16(0)...)
	shb = append(shb, u64(0xffffffffffffffff)...)
	block(0x0a0d0d0a, shb)

	// Interface Description: Ethernet, 64 KiB snap length.
	idb := append([]byte{}, u16(uint16(layers.LinkTypeEthernet))...)
	idb = append(idb, u16(0)...)
	idb = append(idb, u32(65535)...)
	block(0x00000001, idb)

	for i, seq := range []uint64{0, 1} {
		frame := wb.Frame(wire.MagicMBO).Seq(seq).
			Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
			Bytes()
		data := udpEthFrame(t, i, testMktDataUDPPort, frame)

		epb := append([]byte{}, u32(0)...)           // interface id
		epb = append(epb, u32(0)...)                 // timestamp, high
		epb = append(epb, u32(uint32(i))...)         // timestamp, low
		epb = append(epb, u32(uint32(len(data)))...) // captured length
		epb = append(epb, u32(uint32(len(data)))...) // original length
		epb = append(epb, data...)
		if n := len(data) % 4; n != 0 {
			epb = append(epb, make([]byte, 4-n)...)
		}
		// The admission rides on the last packet, so the datagrams before it are
		// read from a window the capture vouches for.
		if drops > 0 && i == 1 {
			epb = append(epb, u16(4)...) // epb_dropcount
			epb = append(epb, u16(8)...)
			epb = append(epb, u64(drops)...)
			epb = append(epb, u16(0)...) // end of options
			epb = append(epb, u16(0)...)
		}
		block(0x00000006, epb)
	}

	path := filepath.Join(dir, "mbo_heartbeat.pcapng")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write pcapng: %v", err)
	}
	return path
}

// writeMBOOpenSnapPcapng writes a pcapng carrying one mktdata frame and a
// snapshot group that is never closed: SnapshotBegin, one SnapshotOrder, and no
// SnapshotEnd. When drops > 0 a trailing Enhanced Packet Block on a port no role
// owns admits them, which is loss after the last mapped datagram — the residual
// nothing carries.
func writeMBOOpenSnapPcapng(t *testing.T, dir string, snapPort int, drops uint64) string {
	t.Helper()

	bo := binary.LittleEndian
	u16 := func(v uint16) []byte { b := make([]byte, 2); bo.PutUint16(b, v); return b }
	u32 := func(v uint32) []byte { b := make([]byte, 4); bo.PutUint32(b, v); return b }
	u64 := func(v uint64) []byte { b := make([]byte, 8); bo.PutUint64(b, v); return b }

	var out []byte
	block := func(typ uint32, body []byte) {
		if n := len(body) % 4; n != 0 {
			body = append(body, make([]byte, 4-n)...)
		}
		total := u32(uint32(12 + len(body)))
		out = append(out, u32(typ)...)
		out = append(out, total...)
		out = append(out, body...)
		out = append(out, total...)
	}

	shb := append([]byte{}, u32(0x1a2b3c4d)...)
	shb = append(shb, u16(1)...)
	shb = append(shb, u16(0)...)
	shb = append(shb, u64(0xffffffffffffffff)...)
	block(0x0a0d0d0a, shb)

	idb := append([]byte{}, u16(uint16(layers.LinkTypeEthernet))...)
	idb = append(idb, u16(0)...)
	idb = append(idb, u32(65535)...)
	block(0x00000001, idb)

	packet := func(i int, data []byte, drops uint64) {
		epb := append([]byte{}, u32(0)...)
		epb = append(epb, u32(0)...)
		epb = append(epb, u32(uint32(i))...)
		epb = append(epb, u32(uint32(len(data)))...)
		epb = append(epb, u32(uint32(len(data)))...)
		epb = append(epb, data...)
		if n := len(data) % 4; n != 0 {
			epb = append(epb, make([]byte, 4-n)...)
		}
		if drops > 0 {
			epb = append(epb, u16(4)...) // epb_dropcount
			epb = append(epb, u16(8)...)
			epb = append(epb, u64(drops)...)
			epb = append(epb, u16(0)...) // end of options
			epb = append(epb, u16(0)...)
		}
		block(0x00000006, epb)
	}

	// One mktdata frame, so the channel's mktdata series exists at all.
	mkt := wb.Frame(wire.MagicMBO).Channel(1).Seq(0).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
		Bytes()
	packet(0, udpEthFrame(t, 0, testMktDataUDPPort, mkt), 0)

	// SnapshotBegin declaring two orders, then one of them, and nothing else: the
	// group the end-of-run findings are about.
	begin := wb.Frame(wire.MagicMBO).Channel(1).Seq(0).
		Msg(wire.TypeSnapshotBegin, 36, func(b *wb.Body) {
			b.U32(200) // instrument id
			b.U64(1)   // anchor seq
			b.U32(2)   // total orders
			b.U32(1)   // snapshot id
			b.U32(1)   // last instrument seq
			b.Pad(8)
		}).Bytes()
	packet(1, udpEthFrame(t, 1, snapPort, begin), 0)

	order := wb.Frame(wire.MagicMBO).Channel(1).Seq(1).
		Msg(wire.TypeSnapshotOrder, 44, func(b *wb.Body) {
			b.U32(1)    // snapshot id
			b.U64(1001) // order id
			b.U8(0)     // side
			b.U8(0)     // order flags
			b.Pad(2)
			b.U64(0)   // enter timestamp
			b.U64(100) // price
			b.U64(10)  // quantity
		}).Bytes()
	packet(2, udpEthFrame(t, 2, snapPort, order), 0)

	if drops > 0 {
		// A port no role owns, so the admission has no datagram after it.
		packet(3, udpEthFrame(t, 3, snapPort+1000, []byte("unmapped")), drops)
	}

	path := filepath.Join(dir, "mbo_open_snap.pcapng")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write pcapng: %v", err)
	}
	return path
}

// udpEthFrame serialises one Ethernet/IPv4/UDP packet carrying payload.
func udpEthFrame(t *testing.T, srcPortOffset, dstPort int, payload []byte) []byte {
	t.Helper()
	buf := gopacket.NewSerializeBuffer()
	ip4 := &layers.IPv4{
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    net.IP{10, 0, 0, 1},
		DstIP:    net.IP{10, 0, 0, 2},
	}
	udp := &layers.UDP{
		SrcPort: layers.UDPPort(50000 + srcPortOffset),
		DstPort: layers.UDPPort(dstPort),
	}
	if err := udp.SetNetworkLayerForChecksum(ip4); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}
	if err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&layers.Ethernet{
			SrcMAC:       net.HardwareAddr{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
			DstMAC:       net.HardwareAddr{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip4,
		udp,
		gopacket.Payload(payload),
	); err != nil {
		t.Fatalf("serialize packet: %v", err)
	}
	data := make([]byte, len(buf.Bytes()))
	copy(data, buf.Bytes())
	return data
}

// TestRunReplaysPcapngAndReportsItsAdmittedLoss covers the whole path the issue
// names: `--pcap` used to refuse the format the recorder archives, so every
// replay went through a conversion that silently stripped the capture's own loss
// accounting. Now the archive is read directly and the number it admits reaches
// the report, which is the only place a one-shot CI run can carry it.
func TestRunReplaysPcapngAndReportsItsAdmittedLoss(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")

	code := Run(RunOpts{
		Cfg:         engine.Config{Feed: core.FeedMBO, ReorderWindow: 8},
		MktDataPort: testMktDataUDPPort,
		PcapPath:    writeMBOPcapng(t, dir, 9),
		JSONReport:  reportPath,
	})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0: admitted capture loss is not the publisher's "+
			"fault and must not move the exit code", code)
	}

	var rep struct {
		ReadError    string `json:"read_error"`
		CaptureDrops uint64 `json:"capture_drops"`
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if rep.ReadError != "" {
		t.Errorf("read_error = %q, but the pcapng was consumed to the end", rep.ReadError)
	}
	if rep.CaptureDrops != 9 {
		t.Errorf("capture_drops = %d, want 9: reading it out of the segment manifest by hand "+
			"was the only check there was, and nothing enforced it", rep.CaptureDrops)
	}

	// A capture that admits nothing reports nothing, so a reader can tell the two
	// apart rather than reading a missing field as clean.
	clean := filepath.Join(dir, "clean")
	if err := os.Mkdir(clean, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanReport := filepath.Join(clean, "report.json")
	if code := Run(RunOpts{
		Cfg:         engine.Config{Feed: core.FeedMBO, ReorderWindow: 8},
		MktDataPort: testMktDataUDPPort,
		PcapPath:    writeMBOPcapng(t, clean, 0),
		JSONReport:  cleanReport,
	}); code != 0 {
		t.Fatalf("Run returned %d on a clean pcapng, want 0", code)
	}
	data, err = os.ReadFile(cleanReport)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	rep.CaptureDrops = 1
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if rep.CaptureDrops != 0 {
		t.Errorf("capture_drops = %d on a capture that admits nothing, want 0", rep.CaptureDrops)
	}
}

// A snapshot group the capture admits it lost the end of is not a grouping
// violation, and only the run loop can know that.
//
// The end-of-run findings are graded against the window the loss falls in, and
// the loss admitted after the last mapped datagram used to be read out of the
// source only *after* they had been produced — so the report showed non-zero
// capture_drops beside a MUST violation graded as though the capture had been
// clean, and exited 1 for it. This pins the ordering in Run, which neither the
// reader's nor the engine's own tests can see.
func TestRunGradesTheLastWindowAgainstLossAdmittedAfterIt(t *testing.T) {
	const snapPort = testMktDataUDPPort + 1
	for _, tc := range []struct {
		name  string
		drops uint64
		// want is the status the unfinished group is reported at.
		want string
		code int
	}{
		// The control: nothing admitted, so the group left open is the publisher's.
		{name: "unadmitted", drops: 0, want: "violation", code: 1},
		{name: "admitted after the last datagram", drops: 3, want: "unverifiable", code: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			reportPath := filepath.Join(dir, "report.json")

			code := Run(RunOpts{
				Cfg:          engine.Config{Feed: core.FeedMBO, ReorderWindow: 8},
				MktDataPort:  testMktDataUDPPort,
				SnapshotPort: snapPort,
				PcapPath:     writeMBOOpenSnapPcapng(t, dir, snapPort, tc.drops),
				JSONReport:   reportPath,
			})
			if code != tc.code {
				t.Errorf("Run returned %d, want %d", code, tc.code)
			}

			var rep struct {
				CaptureDrops uint64 `json:"capture_drops"`
				Rules        []struct {
					RuleID string         `json:"rule_id"`
					Counts map[string]int `json:"counts"`
				} `json:"rules"`
			}
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatalf("read report: %v", err)
			}
			if err := json.Unmarshal(data, &rep); err != nil {
				t.Fatalf("unmarshal report: %v", err)
			}
			if rep.CaptureDrops != tc.drops {
				t.Errorf("capture_drops = %d, want %d", rep.CaptureDrops, tc.drops)
			}
			var counts map[string]int
			for _, r := range rep.Rules {
				if r.RuleID == "SNAP.BEGIN_ORDER_END_GROUPING" {
					counts = r.Counts
				}
			}
			if counts == nil {
				t.Fatalf("no SNAP.BEGIN_ORDER_END_GROUPING row in the report: %s", data)
			}
			if counts[tc.want] != 1 {
				t.Errorf("the group left open at EOF was reported %v, want one %q: a report "+
					"showing drops beside a finding graded as though the capture were clean",
					counts, tc.want)
			}
		})
	}
}

func TestRunWellFormedMBOPcap(t *testing.T) {
	dir := t.TempDir()
	pcapPath := writeMBOPcap(t, dir)

	// Verify the frame we built is actually well-formed by decoding it
	// independently before feeding it to Run.
	rawFrame := wb.Frame(wire.MagicMBO).
		Msg(wire.TypeHeartbeat, 16, func(b *wb.Body) { b.Pad(12) }).
		Bytes()
	_, sf := wire.Decode(rawFrame, wire.MagicMBO)
	if len(sf) > 0 {
		// Non-transport findings mean our test pcap is not well-formed.
		for _, s := range sf {
			if !s.Transport {
				t.Fatalf("test frame is not well-formed: %v: %v", s.RuleID, s.Detail)
			}
		}
	}

	cfg := engine.Config{
		Feed:          core.FeedMBO,
		Strict:        false,
		ReorderWindow: 8,
	}
	opts := RunOpts{
		Cfg:         cfg,
		MktDataPort: testMktDataUDPPort,
		PcapPath:    pcapPath,
		MetricsAddr: "", // no metrics server in tests
	}

	code := Run(opts)
	if code != 0 {
		// Read back the JSON report to understand failures if requested.
		t.Fatalf("Run() returned exit code %d; expected 0 for well-formed pcap", code)
	}
}

// TestRunWellFormedMBOPcapWithJSONReport also exercises the JSON report path.
func TestRunWellFormedMBOPcapWithJSONReport(t *testing.T) {
	dir := t.TempDir()
	pcapPath := writeMBOPcap(t, dir)
	reportPath := filepath.Join(dir, "report.json")

	cfg := engine.Config{
		Feed:          core.FeedMBO,
		Strict:        false,
		ReorderWindow: 8,
	}
	opts := RunOpts{
		Cfg:         cfg,
		MktDataPort: testMktDataUDPPort,
		PcapPath:    pcapPath,
		MetricsAddr: "",
		JSONReport:  reportPath,
	}

	code := Run(opts)
	if code != 0 {
		t.Fatalf("Run() returned exit code %d; expected 0", code)
	}

	// Verify the JSON report was written.
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", reportPath, err)
	}
	if !bytes.Contains(data, []byte("rules")) {
		t.Errorf("JSON report missing 'rules' key; got: %s", data)
	}
}

// TestRunWithoutSnapshotPortReportsStarvedRules pins the two-step trap that
// motivated reportStarvedRules: an MBO/MBP run with --snapshot-port unset used to
// produce an empty report and exit 0, which reads as a clean pass while every
// snapshot-driven rule was starved of input.
//
// The assertion is on the JSON report rather than stderr, because the report is what
// a CI job keeps.
func TestRunWithoutSnapshotPortReportsStarvedRules(t *testing.T) {
	dir := t.TempDir()
	pcapPath := writeMBOPcap(t, dir)
	reportPath := filepath.Join(dir, "report.json")

	opts := RunOpts{
		Cfg:         engine.Config{Feed: core.FeedMBO, ReorderWindow: 8},
		MktDataPort: testMktDataUDPPort,
		PcapPath:    pcapPath,
		JSONReport:  reportPath,
		// SnapshotPort deliberately unset.
	}
	if code := Run(opts); code != 0 {
		t.Fatalf("Run() returned exit code %d; expected 0 (a two-port run is legitimate)", code)
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", reportPath, err)
	}
	var got struct {
		Rules []struct {
			RuleID string         `json:"rule_id"`
			Counts map[string]int `json:"counts"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	na := map[string]int{}
	for _, r := range got.Rules {
		if n := r.Counts["na"]; n > 0 {
			na[r.RuleID] = n
		}
	}

	// Every snapshot-driven rule for this feed must be accounted for, so the report
	// can never be empty while they are unreachable.
	want := core.SnapshotDrivenRules(core.FeedMBO)
	if len(want) == 0 {
		t.Fatal("no snapshot-driven rules registered for mbo: this test would be vacuous")
	}
	for _, id := range want {
		if na[id] == 0 {
			t.Errorf("%s is unreachable without --snapshot-port but the report does not mark it na; "+
				"an empty report reads as a pass", id)
		}
	}

	// And the run must not have claimed any of them passed.
	for _, r := range got.Rules {
		if r.Counts["pass"] > 0 && na[r.RuleID] > 0 {
			t.Errorf("%s is both na and passing, which cannot be true in the same run", r.RuleID)
		}
	}
}
