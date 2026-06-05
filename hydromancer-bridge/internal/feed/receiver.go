// Package feed receives DoubleZero top-of-book frames over UDP multicast and
// dispatches the decoded messages to handlers. It binds the two-port channel
// model from the Reference Data Distribution supplement: a market-data port
// (Quote/Trade/Heartbeat) and a reference-data port (InstrumentDefinition/
// ManifestSummary), both on the same multicast group.
package feed

import (
	"log"
	"net"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

// Config describes one feed to subscribe to.
type Config struct {
	Name      string // label for logging
	Group     string // multicast group address, e.g. "239.255.0.1"
	MktPort   int    // market-data port
	RefPort   int    // reference-data port
	Interface string // optional interface name; "" lets the OS choose
}

// Handlers receives decoded messages. Implementations must be safe for
// concurrent use: market-data and reference-data ports are read on separate
// goroutines.
type Handlers struct {
	OnQuote    func(feed string, q wire.Quote)
	OnDef      func(feed string, d wire.InstrumentDefinition)
	OnManifest func(feed string, m wire.ManifestSummary)
}

// Receiver subscribes to a single feed's two ports.
type Receiver struct {
	cfg      Config
	handlers Handlers
}

// New returns a Receiver for cfg.
func New(cfg Config, h Handlers) *Receiver {
	return &Receiver{cfg: cfg, handlers: h}
}

// Run binds both ports and reads until the process exits. It launches the
// reference-data reader in a goroutine and blocks on the market-data reader.
func (r *Receiver) Run() error {
	ref, err := r.listen(r.cfg.RefPort)
	if err != nil {
		return err
	}
	go r.readLoop(ref, "refdata")

	mkt, err := r.listen(r.cfg.MktPort)
	if err != nil {
		ref.Close()
		return err
	}
	r.readLoop(mkt, "mktdata")
	return nil
}

func (r *Receiver) listen(port int) (*net.UDPConn, error) {
	var iface *net.Interface
	if r.cfg.Interface != "" {
		ni, err := net.InterfaceByName(r.cfg.Interface)
		if err != nil {
			return nil, err
		}
		iface = ni
	}
	addr := &net.UDPAddr{IP: net.ParseIP(r.cfg.Group), Port: port}
	conn, err := net.ListenMulticastUDP("udp", iface, addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(1 << 20)
	return conn, nil
}

func (r *Receiver) readLoop(conn *net.UDPConn, port string) {
	defer conn.Close()
	buf := make([]byte, wire.MaxFrameSize)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("feed %s/%s: read error: %v", r.cfg.Name, port, err)
			return
		}
		r.dispatch(buf[:n])
	}
}

// dispatch parses one frame and routes each application message to a handler.
func (r *Receiver) dispatch(frame []byte) {
	fh, err := wire.DecodeFrameHeader(frame)
	if err != nil {
		return
	}
	off := wire.FrameHeaderSize
	for i := 0; i < int(fh.MsgCount); i++ {
		if off+wire.MsgHeaderSize > len(frame) {
			return
		}
		mh, err := wire.DecodeMsgHeader(frame[off:])
		if err != nil || mh.Length < wire.MsgHeaderSize {
			return
		}
		end := off + int(mh.Length)
		if end > len(frame) {
			return
		}
		r.route(mh.Type, frame[off:end])
		off = end
	}
}

func (r *Receiver) route(typ uint8, msg []byte) {
	switch typ {
	case wire.TypeQuote:
		if q, err := wire.DecodeQuote(msg); err == nil && r.handlers.OnQuote != nil {
			r.handlers.OnQuote(r.cfg.Name, q)
		}
	case wire.TypeInstrumentDefinition:
		if d, err := wire.DecodeInstrumentDefinition(msg); err == nil && r.handlers.OnDef != nil {
			r.handlers.OnDef(r.cfg.Name, d)
		}
	case wire.TypeManifestSummary:
		if m, err := wire.DecodeManifestSummary(msg); err == nil && r.handlers.OnManifest != nil {
			r.handlers.OnManifest(r.cfg.Name, m)
		}
	default:
		// Unknown / unused types (Heartbeat, Trade, EndOfSession) are skipped.
	}
}
