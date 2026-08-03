package main

import (
	"bytes"
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

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}

	ip4 := &layers.IPv4{
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    net.IP{10, 0, 0, 1},
		DstIP:    net.IP{10, 0, 0, 2},
	}
	udp := &layers.UDP{
		SrcPort: layers.UDPPort(50000),
		DstPort: layers.UDPPort(testMktDataUDPPort),
	}
	if err := udp.SetNetworkLayerForChecksum(ip4); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}
	if err := gopacket.SerializeLayers(buf, opts,
		&layers.Ethernet{
			SrcMAC:       net.HardwareAddr{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
			DstMAC:       net.HardwareAddr{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip4,
		udp,
		gopacket.Payload(frameBytes),
	); err != nil {
		t.Fatalf("serialize packet: %v", err)
	}
	ci := gopacket.CaptureInfo{
		Timestamp:     time.Unix(1000, 0),
		CaptureLength: len(buf.Bytes()),
		Length:        len(buf.Bytes()),
	}
	if err := w.WritePacket(ci, buf.Bytes()); err != nil {
		t.Fatalf("write packet: %v", err)
	}
	return pcapPath
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
