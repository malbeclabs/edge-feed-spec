package input

import (
	"io"
	"os"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// PcapSource reads packets from a .pcap file and yields datagrams for each UDP
// packet whose destination port is in the configured port map. Packets whose
// destination port is not in the map are silently skipped.
type PcapSource struct {
	file    *os.File
	reader  *pcapgo.Reader
	portMap map[int]core.Port // dst UDP port → logical Port
}

// NewPcapSource opens a pcap file and returns a Source that yields Datagrams.
// portMap maps UDP destination port numbers to logical core.Port values; packets
// with destination ports not in the map are skipped.
func NewPcapSource(path string, portMap map[int]core.Port) (*PcapSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r, err := pcapgo.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &PcapSource{file: f, reader: r, portMap: portMap}, nil
}

// Next returns the next mapped UDP datagram. ok=false and err=nil signals EOF.
func (s *PcapSource) Next() (Datagram, bool, error) {
	for {
		data, ci, err := s.reader.ReadPacketData()
		if err == io.EOF {
			return Datagram{}, false, nil
		}
		if err != nil {
			return Datagram{}, false, err
		}

		pkt := gopacket.NewPacket(data, s.reader.LinkType(), gopacket.Default)
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

		return Datagram{
			Port:   port,
			Raw:    raw,
			RecvTS: ci.Timestamp,
		}, true, nil
	}
}

// Close releases the underlying file.
func (s *PcapSource) Close() error {
	return s.file.Close()
}
