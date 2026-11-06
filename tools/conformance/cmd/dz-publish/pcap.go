package main

import (
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

// pcapFile records what the publisher would transmit, as a capture the checker can read with
// `--pcap`.
//
// It exists because grading the feed over the wire needs a host where multicast works, and the
// datagrams are the same either way. A capture is also what a publisher sends when asking why a
// checker rejected it.
type pcapFile struct {
	mu      sync.Mutex
	file    *os.File
	writer  *pcapgo.Writer
	packets int
}

func newPcapFile(path string) (*pcapFile, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", path, err)
	}
	writer := pcapgo.NewWriter(file)
	if err := writer.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		return nil, fmt.Errorf("cannot write the pcap header: %w", err)
	}
	return &pcapFile{file: file, writer: writer}, nil
}

// append writes one datagram as an Ethernet/IPv4/UDP packet.
//
// One source address for every packet, because a channel instance is keyed on it: two addresses
// would read as two publishers on one channel, which is the thing this publisher exists not to do.
func (p *pcapFile) append(dstPort int, datagram []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	ip4 := &layers.IPv4{
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolUDP,
		SrcIP:    net.IP{10, 0, 0, 1},
		DstIP:    net.IP{239, 0, 0, 1},
	}
	udp := &layers.UDP{SrcPort: 50000, DstPort: layers.UDPPort(dstPort)}
	if err := udp.SetNetworkLayerForChecksum(ip4); err != nil {
		return fmt.Errorf("cannot set the UDP checksum layer: %w", err)
	}
	eth := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{0x02, 0, 0, 0, 0, 1},
		DstMAC:       net.HardwareAddr{0x01, 0x00, 0x5e, 0, 0, 1},
		EthernetType: layers.EthernetTypeIPv4,
	}
	if err := gopacket.SerializeLayers(buf, opts, eth, ip4, udp, gopacket.Payload(datagram)); err != nil {
		return fmt.Errorf("cannot serialize a packet: %w", err)
	}
	bytes := buf.Bytes()
	ci := gopacket.CaptureInfo{CaptureLength: len(bytes), Length: len(bytes)}
	if err := p.writer.WritePacket(ci, bytes); err != nil {
		return fmt.Errorf("cannot write a packet: %w", err)
	}
	p.packets++
	return nil
}

func (p *pcapFile) Close() error { return p.file.Close() }

// pcapSender is a sender whose socket is the capture file, so the sequence counter and the
// datagram builders under test are the ones the binary uses.
func pcapSender(capture *pcapFile, dstPort int) *sender {
	return &sender{
		write: func(datagram []byte) error { return capture.append(dstPort, datagram) },
		close: func() error { return nil },
	}
}
