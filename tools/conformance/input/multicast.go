package input

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

const (
	multicastBufSize = 65535
	datagramChanSize = 256

	// readErrBackoff and maxConsecutiveReadErrs bound a persistent ReadFromUDP
	// error (e.g. the interface going down) so it cannot busy-spin the read
	// goroutine at 100% CPU. After maxConsecutiveReadErrs failures in a row the
	// loop gives up so the process can exit/restart rather than peg a core.
	readErrBackoff         = 5 * time.Millisecond
	maxConsecutiveReadErrs = 256
)

// MulticastSource binds one UDP multicast socket per logical port and delivers
// datagrams through the Source interface.  Next() blocks until a datagram
// arrives or the source is closed.
type MulticastSource struct {
	datagrams chan Datagram
	conns     []*net.UDPConn
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	// closed is closed after all goroutines have exited. Next() selects on it so
	// that Close() causes an immediate ok=false return even if datagrams are
	// buffered in the channel.
	closed chan struct{}
	// mu guards readErr — the first terminal read-loop error (a sustained
	// ReadFromUDP failure, e.g. the interface going down). It is surfaced through
	// Next so live capture fails loudly (exit code 2) instead of finishing as a
	// silent clean pass when a socket dies.
	mu      sync.Mutex
	readErr error
}

// setReadErr records the first terminal read error; later ones are ignored.
func (ms *MulticastSource) setReadErr(err error) {
	ms.mu.Lock()
	if ms.readErr == nil {
		ms.readErr = err
	}
	ms.mu.Unlock()
}

// readError returns the recorded terminal read error, or nil if none occurred.
func (ms *MulticastSource) readError() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return ms.readErr
}

// MulticastConfig holds the parameters for a MulticastSource.
type MulticastConfig struct {
	// Group is the multicast group IP (e.g. net.ParseIP("239.255.0.1")).
	Group net.IP
	// Ports maps logical core.Port values to UDP port numbers.
	Ports map[core.Port]int
	// Interface is the optional network interface name to join on (e.g. "eth0").
	// If empty, the OS chooses the interface.
	Interface string
}

// NewMulticastSource creates a MulticastSource and begins listening.
// One socket and one goroutine are spawned per logical port.
//
// All sockets are opened before any goroutine is started so that a bind error
// on a later port leaves no goroutines running.
func NewMulticastSource(cfg MulticastConfig) (*MulticastSource, error) {
	var iface *net.Interface
	if cfg.Interface != "" {
		var err error
		iface, err = net.InterfaceByName(cfg.Interface)
		if err != nil {
			return nil, fmt.Errorf("resolving interface %q: %w", cfg.Interface, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	ms := &MulticastSource{
		datagrams: make(chan Datagram, datagramChanSize),
		closed:    make(chan struct{}),
		cancel:    cancel,
	}

	// Phase 1: open all sockets before starting any goroutine.
	// If one bind fails, we never started goroutines, so there is nothing to
	// synchronize — just close the already-opened conns and return.
	type portEntry struct {
		logical core.Port
		conn    *net.UDPConn
	}
	entries := make([]portEntry, 0, len(cfg.Ports))
	for logicalPort, udpPort := range cfg.Ports {
		addr := &net.UDPAddr{IP: cfg.Group, Port: udpPort}
		conn, err := net.ListenMulticastUDP("udp4", iface, addr)
		if err != nil {
			cancel()
			for _, e := range entries {
				_ = e.conn.Close()
			}
			return nil, fmt.Errorf("joining multicast group %s port %d: %w", cfg.Group, udpPort, err)
		}
		entries = append(entries, portEntry{logicalPort, conn})
		ms.conns = append(ms.conns, conn)
	}

	// Phase 2: all sockets open — start goroutines.
	for _, e := range entries {
		ms.wg.Add(1)
		go ms.readLoop(ctx, e.conn, e.logical)
	}

	// Signal closed once all goroutines have exited.
	go func() {
		ms.wg.Wait()
		close(ms.closed)
	}()

	return ms, nil
}

func (ms *MulticastSource) readLoop(ctx context.Context, conn *net.UDPConn, port core.Port) {
	defer ms.wg.Done()

	buf := make([]byte, multicastBufSize)
	consecutiveErrs := 0
	for {
		// Non-blocking done check before we block on ReadFromUDP.
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			// If Close() has been called, treat any error as EOF.
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Back off briefly so a persistent error can't busy-spin at 100% CPU,
			// and bail after a sustained failure run so the process can exit/restart
			// instead of pegging a core. Select on ctx so backoff never delays
			// shutdown.
			consecutiveErrs++
			if consecutiveErrs >= maxConsecutiveReadErrs {
				// A sustained read failure (e.g. the interface going down) is fatal
				// for live capture. Record the error and cancel the whole source so
				// Next surfaces it (exit code 2) rather than silently dropping this
				// port and letting Run finish as a clean pass.
				ms.setReadErr(fmt.Errorf("multicast %v port: read failed %d times consecutively: %w",
					port, consecutiveErrs, err))
				ms.cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(readErrBackoff):
			}
			continue
		}
		consecutiveErrs = 0

		// Copy received bytes — buf is reused on the next iteration.
		raw := make([]byte, n)
		copy(raw, buf[:n])

		dg := Datagram{Port: port, Raw: raw, RecvTS: time.Now()}

		// Select on done so a stalled consumer cannot prevent shutdown.
		select {
		case ms.datagrams <- dg:
		case <-ctx.Done():
			return
		}
	}
}

// Next blocks until the next datagram is available or the source is closed.
// It returns ok=false once the source is closed. The error is nil for a normal
// Close, but non-nil when a terminal read failure (a sustained ReadFromUDP
// error) brought the source down — so live capture fails loudly (exit code 2)
// instead of finishing as a silent clean pass.
//
// Semantics: any call to Next that starts after Close returns will return
// ok=false immediately; the pre-check on ms.closed guarantees this.  A call
// that was already blocking inside the second select when Close completes may
// still return a datagram that was received before Close was signalled —
// consistent with how net/context cancellation behaves during a concurrent
// race between a read and a cancel.
//
// Any datagrams buffered in the internal channel but not yet consumed are
// silently discarded once closed is signalled.
func (ms *MulticastSource) Next() (Datagram, bool, error) {
	// Pre-check: any call starting after Close returns sees ok=false
	// immediately, even if datagrams are queued.
	select {
	case <-ms.closed:
		return Datagram{}, false, ms.readError()
	default:
	}

	// Block until a datagram arrives or the source closes.
	select {
	case dg, ok := <-ms.datagrams:
		if !ok {
			return Datagram{}, false, ms.readError()
		}
		return dg, true, nil
	case <-ms.closed:
		return Datagram{}, false, ms.readError()
	}
}

// Close signals all read goroutines to stop, closes the underlying UDP
// connections, and waits for all goroutines to exit.  It is safe to call
// Close concurrently with Next.  After Close returns, any subsequent call
// to Next returns ok=false immediately.
func (ms *MulticastSource) Close() error {
	ms.closeOnce.Do(func() {
		// Cancel context first so goroutines know to exit on the next error.
		ms.cancel()
		// Unblock any goroutines blocked inside ReadFromUDP.
		for _, c := range ms.conns {
			_ = c.Close()
		}
	})
	// Wait until all goroutines have finished.
	<-ms.closed
	return nil
}
