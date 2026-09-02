package input

import (
	"io"
	"net/netip"
	"os"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// captureReader is the shape both capture formats present: one packet at a
// time, io.EOF at the end, with the link type it was captured on and whatever
// the capture admits it failed to record.
type captureReader interface {
	readPacket() (capturePacket, error)
}

// legacyReader adapts pcapgo's legacy-pcap reader. A legacy file has no field
// in which a recorder can admit a drop, so every packet from it reports none —
// which is an absence of accounting, not an assertion that nothing was lost.
type legacyReader struct{ r *pcapgo.Reader }

func (l legacyReader) readPacket() (capturePacket, error) {
	data, ci, err := l.r.ReadPacketData()
	if err != nil {
		return capturePacket{}, err
	}
	return capturePacket{data: data, ci: ci, linkType: l.r.LinkType()}, nil
}

// PcapSource reads packets from a capture file — legacy pcap or pcapng, chosen
// by the file's own magic — and yields datagrams for each UDP packet whose
// destination port is in the configured port map. Packets whose destination port
// is not in the map are silently skipped.
type PcapSource struct {
	file    *os.File
	reader  captureReader
	portMap map[int]core.Port // dst UDP port → logical Port
	// pendingDrops carries admitted capture loss across packets this reader
	// skips. A drop that precedes an unmapped packet is loss all the same, so it
	// is charged to the next datagram the engine does see rather than discarded
	// with the packet that reported it.
	pendingDrops uint64
	// dropTotal is every drop the file admitted, including any after the last
	// datagram yielded — which is why the total is read from here and not summed
	// from the datagrams.
	dropTotal uint64
}

// NewPcapSource opens a capture file and returns a Source that yields Datagrams.
// Both legacy pcap and pcapng are accepted, transparently: pcapng is what the
// recorder archives, and the caller usually does not care which format the file
// on disk is.
//
// portMap maps UDP destination port numbers to logical core.Port values; packets
// with destination ports not in the map are skipped.
func NewPcapSource(path string, portMap map[int]core.Port) (*PcapSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := newCaptureReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &PcapSource{file: f, reader: r, portMap: portMap}, nil
}

// newCaptureReader picks the reader the file's magic names. pcapgo's legacy
// reader already takes both legacy magics and both byte orders; pcapng is read
// by this package, because pcapng is the only format that carries the capture's
// own loss accounting and gopacket's reader discards it (see pcapng.go).
func newCaptureReader(r io.Reader) (captureReader, error) {
	br, isNg, err := sniffCaptureFormat(r)
	if err != nil {
		return nil, err
	}
	if isNg {
		return newNgReader(br)
	}
	lr, err := pcapgo.NewReader(br)
	if err != nil {
		return nil, err
	}
	return legacyReader{r: lr}, nil
}

// Next returns the next mapped UDP datagram. ok=false and err=nil signals EOF.
func (s *PcapSource) Next() (Datagram, bool, error) {
	for {
		cp, err := s.reader.readPacket()
		if err == io.EOF {
			return Datagram{}, false, nil
		}
		if err != nil {
			return Datagram{}, false, err
		}
		s.pendingDrops += cp.drops
		s.dropTotal += cp.drops

		pkt := gopacket.NewPacket(cp.data, cp.linkType, gopacket.Default)
		udpLayer := pkt.Layer(layers.LayerTypeUDP)
		if udpLayer == nil {
			continue
		}
		udp, _ := udpLayer.(*layers.UDP)
		port, ok := s.portMap[int(udp.DstPort)]
		if !ok {
			continue
		}

		payload := udp.Payload
		raw := make([]byte, len(payload))
		copy(raw, payload)

		drops := s.pendingDrops
		s.pendingDrops = 0

		return Datagram{
			Port:         port,
			Src:          srcAddr(pkt),
			Raw:          raw,
			RecvTS:       cp.ci.Timestamp,
			CaptureDrops: drops,
		}, true, nil
	}
}

// CaptureDrops implements CaptureLossReporter: the running total of datagrams
// the capture admits it failed to record, over everything read so far. It
// includes drops admitted after the last datagram this source yielded, which a
// caller summing Datagram.CaptureDrops would never see.
func (s *PcapSource) CaptureDrops() uint64 { return s.dropTotal }

// srcAddr returns the packet's source address, so a replay keys channel
// instances exactly as a live capture does. A capture whose link type carries no
// network layer yields the zero Addr — every datagram then reads as one instance,
// which is what a pre-instance capture is.
func srcAddr(pkt gopacket.Packet) netip.Addr {
	switch ip := pkt.NetworkLayer().(type) {
	case *layers.IPv4:
		a, _ := netip.AddrFromSlice(ip.SrcIP)
		return a.Unmap()
	case *layers.IPv6:
		a, _ := netip.AddrFromSlice(ip.SrcIP)
		return a.Unmap()
	}
	return netip.Addr{}
}

// Close releases the underlying file.
func (s *PcapSource) Close() error {
	return s.file.Close()
}
