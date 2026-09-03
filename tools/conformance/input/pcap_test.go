package input

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// udpEthPacket serialises one Ethernet/IPv4/UDP frame carrying payload, which is
// the only packet shape either capture reader has to get through. srcPortOffset
// varies the source port so consecutive packets are distinguishable.
func udpEthPacket(t *testing.T, srcPortOffset int, dstPort uint16, payload []byte) []byte {
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
	err := gopacket.SerializeLayers(buf, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		&layers.Ethernet{
			SrcMAC:       net.HardwareAddr{0x00, 0x01, 0x02, 0x03, 0x04, 0x05},
			DstMAC:       net.HardwareAddr{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b},
			EthernetType: layers.EthernetTypeIPv4,
		},
		ip4,
		udp,
		gopacket.Payload(payload),
	)
	if err != nil {
		t.Fatalf("serialize packet: %v", err)
	}
	out := make([]byte, len(buf.Bytes()))
	copy(out, buf.Bytes())
	return out
}

func writePcap(t *testing.T, path string, packets [][]byte, dstPorts []uint16) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pcap: %v", err)
	}
	defer func() { _ = f.Close() }()

	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		t.Fatalf("write pcap header: %v", err)
	}

	for i, payload := range packets {
		data := udpEthPacket(t, i, dstPorts[i], payload)
		ci := gopacket.CaptureInfo{
			Timestamp:     time.Unix(int64(1000+i), 0),
			CaptureLength: len(data),
			Length:        len(data),
		}
		if err := w.WritePacket(ci, data); err != nil {
			t.Fatalf("write packet %d: %v", i, err)
		}
	}
}

func TestPcapSource(t *testing.T) {
	dir := t.TempDir()
	pcapPath := filepath.Join(dir, "test.pcap")

	mktPayload := []byte("hello mktdata")
	refPayload := []byte("hello refdata")

	writePcap(t, pcapPath,
		[][]byte{mktPayload, refPayload},
		[]uint16{7001, 7002},
	)

	portMap := map[int]core.Port{
		7001: core.PortMktData,
		7002: core.PortRefData,
	}
	src, err := NewPcapSource(pcapPath, portMap)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	// First datagram: mktdata
	dg, ok, err := src.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for first datagram")
	}
	if dg.Port != core.PortMktData {
		t.Errorf("got port %v, want PortMktData", dg.Port)
	}
	if !bytes.Equal(dg.Raw, mktPayload) {
		t.Errorf("got payload %q, want %q", dg.Raw, mktPayload)
	}
	if !dg.RecvTS.Equal(time.Unix(1000, 0)) {
		t.Errorf("got RecvTS %v, want %v", dg.RecvTS, time.Unix(1000, 0))
	}

	// Second datagram: refdata
	dg, ok, err = src.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true for second datagram")
	}
	if dg.Port != core.PortRefData {
		t.Errorf("got port %v, want PortRefData", dg.Port)
	}
	if !bytes.Equal(dg.Raw, refPayload) {
		t.Errorf("got payload %q, want %q", dg.Raw, refPayload)
	}
	if !dg.RecvTS.Equal(time.Unix(1001, 0)) {
		t.Errorf("got RecvTS %v, want %v", dg.RecvTS, time.Unix(1001, 0))
	}

	// Third call: EOF
	_, ok, err = src.Next()
	if err != nil {
		t.Fatalf("Next() at EOF error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false at EOF")
	}
}

// The snap length is the one piece of capture loss a legacy pcap *can* state.
//
// It carries no epb_dropcount — a converted file has had it stripped, and that
// is the reason to replay the archive itself — but every packet record declares
// both its included and its original length, so a capture recorded with `-s`
// shorter than the feed says so in either format. Handing those bytes to the
// decoder charges the recorder's own snap length to the publisher, so the check
// lives in the reader both formats go through rather than in the pcapng one.
func TestLegacyPcapSnaplenTruncatedPacketIsCaptureOwned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short-snaplen.pcap")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pcap: %v", err)
	}
	w := pcapgo.NewWriter(f)
	if err := w.WriteFileHeader(96, layers.LinkTypeEthernet); err != nil {
		t.Fatalf("write pcap header: %v", err)
	}
	short := udpEthPacket(t, 0, 7001, []byte("cut by the snap length"))
	err = w.WritePacket(gopacket.CaptureInfo{
		Timestamp:     time.Unix(1000, 0),
		CaptureLength: len(short) - 20,
		Length:        len(short),
	}, short[:len(short)-20])
	if err != nil {
		t.Fatalf("write truncated packet: %v", err)
	}
	whole := udpEthPacket(t, 1, 7001, []byte("recorded whole"))
	err = w.WritePacket(gopacket.CaptureInfo{
		Timestamp:     time.Unix(1001, 0),
		CaptureLength: len(whole),
		Length:        len(whole),
	}, whole)
	if err != nil {
		t.Fatalf("write whole packet: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	src, err := NewPcapSource(path, mktOnly)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	got := drain(t, src)
	if len(got) != 1 {
		t.Fatalf("got %d datagram(s), want 1: a partly-recorded datagram must not reach the decoder", len(got))
	}
	if string(got[0].Raw) != "recorded whole" {
		t.Errorf("yielded payload %q, want the whole packet", got[0].Raw)
	}
	if got[0].CaptureDrops != 1 || src.SnaplenTruncated() != 1 {
		t.Errorf("drops=%d truncated=%d, want 1 and 1: the missing bytes are the capture's",
			got[0].CaptureDrops, src.SnaplenTruncated())
	}
}

func TestPcapSourceSkipsUnmappedPorts(t *testing.T) {
	dir := t.TempDir()
	pcapPath := filepath.Join(dir, "test.pcap")

	mktPayload := []byte("important data")
	unknownPayload := []byte("should be skipped")

	// Write: unknown port first, then mktdata port
	writePcap(t, pcapPath,
		[][]byte{unknownPayload, mktPayload},
		[]uint16{9999, 7001},
	)

	portMap := map[int]core.Port{
		7001: core.PortMktData,
	}
	src, err := NewPcapSource(pcapPath, portMap)
	if err != nil {
		t.Fatalf("NewPcapSource: %v", err)
	}
	defer func() { _ = src.Close() }()

	// First Next() should skip port 9999 and return the mktdata packet
	dg, ok, err := src.Next()
	if err != nil {
		t.Fatalf("Next() error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if dg.Port != core.PortMktData {
		t.Errorf("got port %v, want PortMktData", dg.Port)
	}
	if !bytes.Equal(dg.Raw, mktPayload) {
		t.Errorf("got payload %q, want %q", dg.Raw, mktPayload)
	}

	// Next: EOF
	_, ok, err = src.Next()
	if err != nil {
		t.Fatalf("Next() at EOF error: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false at EOF")
	}
}
