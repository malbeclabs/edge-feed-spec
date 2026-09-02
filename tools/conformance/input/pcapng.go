package input

// pcapng.go — a pcapng block reader that keeps the capture's own loss accounting.
//
// **Why this file exists rather than a call into gopacket.** pcapgo does have an
// NgReader, and it reads the format correctly, but it discards every per-packet
// option — its own comment on the matter reads `// handle options somehow - this
// would be expensive`. `epb_dropcount` is a per-packet option, and it is the
// recorder's admission of what *it* failed to record. That single field is the
// only thing in an archived segment that separates capture loss from publisher
// loss: a sequence gap the capture already owns is not evidence about the
// publisher. A reader that drops it leaves the rule set structurally unable to
// tell the two apart, so the blocks are parsed here.
//
// The subset implemented is what an archive contains: the Section Header and
// Interface Description blocks that establish byte order, link type and
// timestamp scale, and the three packet-bearing blocks (Enhanced, Simple, and
// the obsolete Packet Block). Every other block type carries nothing this tool
// judges and is skipped by length, which is also what keeps a file with name
// resolution or interface statistics blocks in it readable.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

// Block types (pcapng section 4). ngBlockSectionHeader doubles as the file's
// magic; its byte pattern is a palindrome precisely so that it identifies the
// format before the byte order is known.
const (
	ngBlockSectionHeader        = 0x0a0d0d0a
	ngBlockInterfaceDescription = 0x00000001
	ngBlockPacket               = 0x00000002 // obsolete, superseded by Enhanced
	ngBlockSimplePacket         = 0x00000003
	ngBlockEnhancedPacket       = 0x00000006
)

// Option codes (pcapng section 4). Only the ones that change how a packet is
// read or judged are decoded.
const (
	ngOptEndOfOptions  = 0
	ngOptEPBDropCount  = 4  // per-packet: datagrams the capture lost before this one
	ngOptIfTsResol     = 9  // per-interface: timestamp units per second
	ngOptIfTsOffset    = 14 // per-interface: seconds added to every timestamp
	ngPBDropsUnknown   = 0xffff
	ngByteOrderMagic   = 0x1a2b3c4d
	ngDefaultTsResol   = 1_000_000 // µs, the value assumed when if_tsresol is absent
	ngSupportedVersion = 1         // major version this reader claims to understand
)

// ngMaxBlockLen bounds a single block. A block length is a 32-bit field read
// from the file, so a corrupt one would otherwise size an allocation: 64 MiB is
// far above any jumbo frame plus options and small enough that a garbage length
// fails instead of exhausting memory.
const ngMaxBlockLen = 64 << 20

// ngSectionHeaderMagic is the first four bytes of every pcapng file.
var ngSectionHeaderMagic = []byte{0x0a, 0x0d, 0x0d, 0x0a}

// capturePacket is one packet read from a capture file, in either format.
//
// data aliases the reader's scratch buffer and is only valid until the next
// read; a caller that keeps the bytes must copy them.
type capturePacket struct {
	data     []byte
	ci       gopacket.CaptureInfo
	linkType layers.LinkType
	// drops is what the capture admits it failed to record between the previous
	// packet on this packet's interface and this one. Always 0 for a legacy pcap,
	// which has no field to say it.
	drops uint64
}

// ngIface is one interface's decoding context, established by its Interface
// Description Block and referenced by index from every packet block.
type ngIface struct {
	linkType layers.LinkType
	snapLen  uint32
	// tsUnits is the number of timestamp units in one second (if_tsresol).
	tsUnits uint64
	// tsOffset is the whole-second epoch offset added to every timestamp
	// (if_tsoffset), which is how a capture stores relative timestamps.
	tsOffset int64
}

// timestamp converts a raw 64-bit interface timestamp to wall time.
func (i ngIface) timestamp(raw uint64) time.Time {
	sec := raw / i.tsUnits
	frac := raw % i.tsUnits
	// frac < tsUnits, so frac·1e9/tsUnits < 1e9 and the 128-bit intermediate
	// makes the conversion exact for every resolution the format allows —
	// including the 2^-n ones, where a 64-bit multiply would overflow.
	hi, lo := bits.Mul64(frac, uint64(time.Second))
	nsec, _ := bits.Div64(hi, lo, i.tsUnits)
	return time.Unix(int64(sec)+i.tsOffset, int64(nsec)).UTC()
}

// ngReader reads packets from a pcapng stream.
type ngReader struct {
	r  io.Reader
	bo binary.ByteOrder
	// ifaces is the current section's interface table, indexed by Interface ID.
	// A new Section Header Block restarts the numbering, so it is cleared there.
	ifaces []ngIface
	// block is the scratch buffer every block is read into, reused across reads.
	block []byte
}

// newNgReader reads the leading Section Header Block, which is what establishes
// the byte order every subsequent field is read in.
func newNgReader(r io.Reader) (*ngReader, error) {
	nr := &ngReader{r: r}
	typ, body, err := nr.readBlock()
	if err != nil {
		return nil, fmt.Errorf("pcapng: read section header: %w", err)
	}
	if typ != ngBlockSectionHeader {
		return nil, fmt.Errorf("pcapng: file starts with block type %#x, not a section header", typ)
	}
	if err := nr.readSectionHeader(body); err != nil {
		return nil, err
	}
	return nr, nil
}

// readBlock reads one whole block and returns its type and its body — the bytes
// between the leading type/length pair and the trailing length repeat. The
// returned slice aliases the reader's scratch buffer.
//
// io.EOF is returned only at a block boundary, so a file that ends mid-block
// reports io.ErrUnexpectedEOF and the run records a read error rather than
// reading a truncated archive as a complete one.
func (r *ngReader) readBlock() (uint32, []byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		return 0, nil, err
	}

	var typ uint32
	var bom [4]byte
	pre := 0 // body bytes already consumed (a section header's byte-order magic)
	switch {
	case bytes.Equal(hdr[:4], ngSectionHeaderMagic):
		// A section header carries the byte order for everything after it —
		// including its own length field — so the magic has to be read before the
		// length can be interpreted.
		if _, err := io.ReadFull(r.r, bom[:]); err != nil {
			return 0, nil, err
		}
		switch {
		case binary.LittleEndian.Uint32(bom[:]) == ngByteOrderMagic:
			r.bo = binary.LittleEndian
		case binary.BigEndian.Uint32(bom[:]) == ngByteOrderMagic:
			r.bo = binary.BigEndian
		default:
			return 0, nil, fmt.Errorf("pcapng: section header byte-order magic is %#x, not %#x",
				bom, uint32(ngByteOrderMagic))
		}
		typ, pre = ngBlockSectionHeader, 4
	case r.bo == nil:
		return 0, nil, errors.New("pcapng: no section header seen, so no byte order is established")
	default:
		typ = r.bo.Uint32(hdr[:4])
	}

	total := r.bo.Uint32(hdr[4:8])
	if total < uint32(12+pre) || total%4 != 0 || total > ngMaxBlockLen {
		return 0, nil, fmt.Errorf("pcapng: block type %#x declares an implausible total length of %d", typ, total)
	}
	// rest is the body plus the trailing length repeat.
	rest := r.scratch(int(total) - 8)
	copy(rest, bom[:pre])
	if _, err := io.ReadFull(r.r, rest[pre:]); err != nil {
		return 0, nil, err
	}
	if trailer := r.bo.Uint32(rest[len(rest)-4:]); trailer != total {
		return 0, nil, fmt.Errorf("pcapng: block type %#x opens with length %d and closes with %d",
			typ, total, trailer)
	}
	return typ, rest[:len(rest)-4], nil
}

// scratch returns a buffer of exactly n bytes, growing the reusable one when a
// block needs more than it holds.
func (r *ngReader) scratch(n int) []byte {
	if cap(r.block) < n {
		r.block = make([]byte, n)
	}
	return r.block[:n]
}

// readSectionHeader validates the version and starts a fresh interface table:
// Interface IDs are section-scoped, so carrying the previous section's table
// over would resolve a packet against the wrong interface.
func (r *ngReader) readSectionHeader(body []byte) error {
	if len(body) < 16 { // byte-order magic, major, minor, section length
		return fmt.Errorf("pcapng: section header block is %d bytes, too short to hold a version", len(body))
	}
	if major := r.bo.Uint16(body[4:6]); major != ngSupportedVersion {
		return fmt.Errorf("pcapng: section declares version %d.%d, and this reader implements %d.x",
			major, r.bo.Uint16(body[6:8]), ngSupportedVersion)
	}
	r.ifaces = r.ifaces[:0]
	return nil
}

// readInterface appends the interface an Interface Description Block describes.
func (r *ngReader) readInterface(body []byte) error {
	if len(body) < 8 { // link type, reserved, snap length
		return fmt.Errorf("pcapng: interface description block is %d bytes, too short", len(body))
	}
	iface := ngIface{
		linkType: layers.LinkType(r.bo.Uint16(body[:2])),
		snapLen:  r.bo.Uint32(body[4:8]),
		tsUnits:  ngDefaultTsResol,
	}
	err := r.eachOption(body[8:], func(code uint16, val []byte) error {
		switch code {
		case ngOptIfTsResol:
			if len(val) < 1 {
				return errors.New("pcapng: if_tsresol option is empty")
			}
			// High bit clear: the value is a power of ten. Set: a power of two.
			// Nanosecond captures (9) take the first branch; the second exists
			// because the format allows it, not because a recorder writes it.
			exp := val[0] & 0x7f
			if val[0]&0x80 == 0 {
				if exp > 19 {
					return fmt.Errorf("pcapng: if_tsresol 10^-%d is finer than a uint64 can count per second", exp)
				}
				units := uint64(1)
				for range exp {
					units *= 10
				}
				iface.tsUnits = units
			} else {
				if exp > 63 {
					return fmt.Errorf("pcapng: if_tsresol 2^-%d is finer than a uint64 can count per second", exp)
				}
				iface.tsUnits = uint64(1) << exp
			}
			if iface.tsUnits == 0 {
				return errors.New("pcapng: if_tsresol resolves to zero units per second")
			}
		case ngOptIfTsOffset:
			if len(val) < 8 {
				return fmt.Errorf("pcapng: if_tsoffset option is %d bytes, want 8", len(val))
			}
			iface.tsOffset = int64(r.bo.Uint64(val[:8]))
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.ifaces = append(r.ifaces, iface)
	return nil
}

// eachOption walks a block's trailing option list, calling fn for each option.
// It stops at end-of-options and at the end of the buffer; a writer that omits
// the terminating option is common and not an error.
func (r *ngReader) eachOption(b []byte, fn func(code uint16, val []byte) error) error {
	for len(b) >= 4 {
		code := r.bo.Uint16(b[:2])
		if code == ngOptEndOfOptions {
			return nil
		}
		length := int(r.bo.Uint16(b[2:4]))
		if 4+length > len(b) {
			return fmt.Errorf("pcapng: option %d claims %d bytes with only %d left in the block",
				code, length, len(b)-4)
		}
		if err := fn(code, b[4:4+length]); err != nil {
			return err
		}
		// Option values are padded up to a 4-byte boundary; the final option's
		// padding may be absent, which ends the walk.
		next := 4 + pad4(length)
		if next > len(b) {
			return nil
		}
		b = b[next:]
	}
	return nil
}

// pad4 rounds n up to the next multiple of 4, the format's alignment for both
// option values and packet data.
func pad4(n int) int { return (n + 3) & ^3 }

// readPacket returns the next packet in the stream, skipping the block types
// that carry no packet. io.EOF marks the end of the file.
func (r *ngReader) readPacket() (capturePacket, error) {
	for {
		typ, body, err := r.readBlock()
		if err != nil {
			return capturePacket{}, err
		}
		switch typ {
		case ngBlockSectionHeader:
			if err := r.readSectionHeader(body); err != nil {
				return capturePacket{}, err
			}
		case ngBlockInterfaceDescription:
			if err := r.readInterface(body); err != nil {
				return capturePacket{}, err
			}
		case ngBlockEnhancedPacket:
			return r.readEnhancedPacket(body)
		case ngBlockSimplePacket:
			return r.readSimplePacket(body)
		case ngBlockPacket:
			return r.readObsoletePacket(body)
		default:
			// Name resolution, interface statistics, custom blocks: nothing this
			// tool judges, and skipping by length is what keeps them readable.
		}
	}
}

// iface resolves an Interface ID against the current section's table.
func (r *ngReader) iface(id uint32) (ngIface, error) {
	if id >= uint32(len(r.ifaces)) {
		return ngIface{}, fmt.Errorf("pcapng: packet names interface %d, and the section describes %d",
			id, len(r.ifaces))
	}
	return r.ifaces[id], nil
}

// readEnhancedPacket decodes an Enhanced Packet Block: the block an archive is
// made of, and the only one that carries epb_dropcount.
func (r *ngReader) readEnhancedPacket(body []byte) (capturePacket, error) {
	const hdrLen = 20 // interface id, timestamp high/low, captured len, original len
	if len(body) < hdrLen {
		return capturePacket{}, fmt.Errorf("pcapng: enhanced packet block is %d bytes, too short", len(body))
	}
	iface, err := r.iface(r.bo.Uint32(body[:4]))
	if err != nil {
		return capturePacket{}, err
	}
	ts := uint64(r.bo.Uint32(body[4:8]))<<32 | uint64(r.bo.Uint32(body[8:12]))
	capLen := int(r.bo.Uint32(body[12:16]))
	origLen := int(r.bo.Uint32(body[16:20]))
	if capLen < 0 || capLen > len(body)-hdrLen {
		return capturePacket{}, fmt.Errorf("pcapng: enhanced packet block claims %d captured bytes with %d in the block",
			capLen, len(body)-hdrLen)
	}

	pkt := capturePacket{
		data:     body[hdrLen : hdrLen+capLen],
		linkType: iface.linkType,
		ci: gopacket.CaptureInfo{
			Timestamp:     iface.timestamp(ts),
			CaptureLength: capLen,
			Length:        origLen,
		},
	}
	// Options follow the packet data, padded to a 4-byte boundary.
	if optOff := hdrLen + pad4(capLen); optOff < len(body) {
		err := r.eachOption(body[optOff:], func(code uint16, val []byte) error {
			if code == ngOptEPBDropCount {
				if len(val) < 8 {
					return fmt.Errorf("pcapng: epb_dropcount option is %d bytes, want 8", len(val))
				}
				pkt.drops = r.bo.Uint64(val[:8])
			}
			return nil
		})
		if err != nil {
			return capturePacket{}, err
		}
	}
	return pkt, nil
}

// readSimplePacket decodes a Simple Packet Block. It carries no timestamp and no
// options, so its captured length has to be derived from the interface's snap
// length, and it can never admit a drop.
func (r *ngReader) readSimplePacket(body []byte) (capturePacket, error) {
	if len(body) < 4 {
		return capturePacket{}, fmt.Errorf("pcapng: simple packet block is %d bytes, too short", len(body))
	}
	iface, err := r.iface(0)
	if err != nil {
		return capturePacket{}, err
	}
	origLen := int(r.bo.Uint32(body[:4]))
	capLen := origLen
	if iface.snapLen != 0 && capLen > int(iface.snapLen) {
		capLen = int(iface.snapLen)
	}
	if capLen < 0 || capLen > len(body)-4 {
		return capturePacket{}, fmt.Errorf("pcapng: simple packet block implies %d captured bytes with %d in the block",
			capLen, len(body)-4)
	}
	return capturePacket{
		data:     body[4 : 4+capLen],
		linkType: iface.linkType,
		ci:       gopacket.CaptureInfo{CaptureLength: capLen, Length: origLen},
	}, nil
}

// readObsoletePacket decodes the deprecated Packet Block, whose drop count is a
// 16-bit header field rather than an option. 0xffff means the writer had no
// count to give, which is not the same as none lost.
func (r *ngReader) readObsoletePacket(body []byte) (capturePacket, error) {
	const hdrLen = 20 // interface id, drops count, timestamp high/low, captured len, original len
	if len(body) < hdrLen {
		return capturePacket{}, fmt.Errorf("pcapng: packet block is %d bytes, too short", len(body))
	}
	iface, err := r.iface(uint32(r.bo.Uint16(body[:2])))
	if err != nil {
		return capturePacket{}, err
	}
	ts := uint64(r.bo.Uint32(body[4:8]))<<32 | uint64(r.bo.Uint32(body[8:12]))
	capLen := int(r.bo.Uint32(body[12:16]))
	origLen := int(r.bo.Uint32(body[16:20]))
	if capLen < 0 || capLen > len(body)-hdrLen {
		return capturePacket{}, fmt.Errorf("pcapng: packet block claims %d captured bytes with %d in the block",
			capLen, len(body)-hdrLen)
	}
	var drops uint64
	if d := r.bo.Uint16(body[2:4]); d != ngPBDropsUnknown {
		drops = uint64(d)
	}
	return capturePacket{
		data:     body[hdrLen : hdrLen+capLen],
		linkType: iface.linkType,
		ci: gopacket.CaptureInfo{
			Timestamp:     iface.timestamp(ts),
			CaptureLength: capLen,
			Length:        origLen,
		},
		drops: drops,
	}, nil
}

// sniffCaptureFormat wraps r and reports whether the stream is pcapng. Both
// formats go under the one --pcap flag: which of them the recorder happened to
// write is not a question the caller should have to answer, and pcapng is what
// the recorder archives.
func sniffCaptureFormat(r io.Reader) (*bufio.Reader, bool, error) {
	br := bufio.NewReaderSize(r, 1<<16)
	magic, err := br.Peek(4)
	if err != nil {
		return nil, false, fmt.Errorf("read capture magic: %w", err)
	}
	return br, bytes.Equal(magic, ngSectionHeaderMagic), nil
}
