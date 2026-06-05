// Command bridge arbitrates one or more DoubleZero top-of-book multicast feeds,
// takes the first received copy of each quote, converts it from the reference
// wire format into the Hyperliquid / Hydromancer BBO schema, and serves it over
// a local websocket that emulates the Hyperliquid public WS endpoint.
//
// A trader points an existing Hydromancer/Hyperliquid bbo client at
// ws://localhost:8080/ws and receives {"channel":"bbo","data":{...}} messages
// without integrating the DoubleZero wire format at all.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/arbiter"
	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/feed"
	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/hlbbo"
	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/refdata"
	"github.com/malbeclabs/edge-feed-spec/hydromancer-bridge/internal/wire"
)

// feedList collects repeated -feed flags of the form name@group:mktPort:refPort.
type feedList []feed.Config

func (f *feedList) String() string { return fmt.Sprintf("%v", *f) }

func (f *feedList) Set(s string) error {
	cfg, err := parseFeed(s)
	if err != nil {
		return err
	}
	*f = append(*f, cfg)
	return nil
}

func parseFeed(s string) (feed.Config, error) {
	// name@group:mktPort:refPort
	name := ""
	rest := s
	if at := strings.IndexByte(s, '@'); at >= 0 {
		name, rest = s[:at], s[at+1:]
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return feed.Config{}, fmt.Errorf("feed %q: want [name@]group:mktPort:refPort", s)
	}
	mkt, err := strconv.Atoi(parts[1])
	if err != nil {
		return feed.Config{}, fmt.Errorf("feed %q: bad mkt port: %w", s, err)
	}
	ref, err := strconv.Atoi(parts[2])
	if err != nil {
		return feed.Config{}, fmt.Errorf("feed %q: bad ref port: %w", s, err)
	}
	if name == "" {
		name = parts[0]
	}
	return feed.Config{Name: name, Group: parts[0], MktPort: mkt, RefPort: ref}, nil
}

func main() {
	var feeds feedList
	flag.Var(&feeds, "feed", "feed to subscribe to, [name@]group:mktPort:refPort (repeatable)")
	listen := flag.String("listen", ":8080", "websocket listen address")
	path := flag.String("path", "/ws", "websocket path")
	iface := flag.String("iface", "", "multicast interface name (optional)")
	flag.Parse()

	if len(feeds) == 0 {
		// Default matches the bundled simulator for a zero-config local demo.
		feeds = feedList{{Name: "sim", Group: "239.255.0.1", MktPort: 5000, RefPort: 5001}}
		log.Printf("no -feed given; using default %s@%s:%d:%d",
			feeds[0].Name, feeds[0].Group, feeds[0].MktPort, feeds[0].RefPort)
	}

	store := refdata.New()
	arb := arbiter.New()
	srv := hlbbo.NewServer()

	var dropped atomic.Uint64
	handlers := feed.Handlers{
		OnDef: func(_ string, d wire.InstrumentDefinition) {
			store.Put(d)
		},
		OnQuote: func(_ string, q wire.Quote) {
			def, ok := store.Lookup(q.InstrumentID)
			if !ok {
				dropped.Add(1) // definition not yet received; will fill in next cycle
				return
			}
			if !arb.Accept(q) {
				return // duplicate from a slower feed
			}
			srv.Broadcast(hlbbo.Convert(q, def))
		},
	}

	for _, cfg := range feeds {
		cfg.Interface = *iface
		r := feed.New(cfg, handlers)
		go func(c feed.Config) {
			log.Printf("subscribing to feed %s on %s (mkt:%d ref:%d)",
				c.Name, c.Group, c.MktPort, c.RefPort)
			if err := r.Run(); err != nil {
				log.Printf("feed %s stopped: %v", c.Name, err)
			}
		}(cfg)
	}

	go func() {
		for range time.NewTicker(10 * time.Second).C {
			log.Printf("status: %d instruments known, %d ws clients, %d quotes dropped (awaiting defs)",
				store.Len(), srv.ClientCount(), dropped.Load())
		}
	}()

	mux := http.NewServeMux()
	mux.Handle(*path, srv)
	log.Printf("serving Hyperliquid-compatible bbo websocket on %s%s", *listen, *path)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("websocket server: %v", err)
	}
}
