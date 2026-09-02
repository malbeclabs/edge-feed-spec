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
