package hlbbo

import (
	"encoding/json"
	"net/http"
	"sync"
)

// subscribeMsg is the Hyperliquid subscribe/unsubscribe request:
//
//	{"method":"subscribe","subscription":{"type":"bbo","coin":"ETH"}}
type subscribeMsg struct {
	Method       string `json:"method"`
	Subscription struct {
		Type string `json:"type"`
		Coin string `json:"coin"`
	} `json:"subscription"`
}

// client is a connected websocket subscriber and the coins it wants. A coin of
// "*", "all", or "ALL" subscribes to every instrument (a convenience this
// bridge adds on top of the native per-coin protocol).
type client struct {
	conn  *wsConn
	mu    sync.Mutex
	all   bool
	coins map[string]bool
}

func (c *client) wants(coin string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.all || c.coins[coin]
}

func (c *client) subscribe(coin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if coin == "*" || coin == "all" || coin == "ALL" {
		c.all = true
		return
	}
	c.coins[coin] = true
}

func (c *client) unsubscribe(coin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if coin == "*" || coin == "all" || coin == "ALL" {
		c.all = false
		c.coins = make(map[string]bool)
		return
	}
	delete(c.coins, coin)
}

// Server emulates the Hyperliquid public websocket for the bbo channel. It
// holds the set of connected clients and broadcasts converted BBO messages to
// the ones subscribed to each coin.
type Server struct {
	mu      sync.RWMutex
	clients map[*client]struct{}
}

// NewServer returns a Server ready to register with an http mux at, e.g., "/ws".
func NewServer() *Server {
	return &Server{clients: make(map[*client]struct{})}
}

// ServeHTTP upgrades the request and runs the per-client read loop.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrade(w, r)
	if err != nil {
		http.Error(w, "expected websocket", http.StatusBadRequest)
		return
	}
	c := &client{conn: conn, coins: make(map[string]bool)}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		conn.close()
	}()

	for {
		op, payload, err := conn.readFrame()
		if err != nil {
			return
		}
		switch op {
		case opClose:
			return
		case opPing:
			_ = conn.writeFrame(opPong, payload)
		case opText:
			s.handleControl(c, payload)
		}
	}
}

// handleControl processes a subscribe/unsubscribe message and acks it the way
// Hyperliquid does, with a subscriptionResponse echo.
func (s *Server) handleControl(c *client, payload []byte) {
	var m subscribeMsg
	if err := json.Unmarshal(payload, &m); err != nil {
		return
	}
	if m.Subscription.Type != "bbo" {
		return // only the bbo channel is served
	}
	switch m.Method {
	case "subscribe":
		c.subscribe(m.Subscription.Coin)
	case "unsubscribe":
		c.unsubscribe(m.Subscription.Coin)
	default:
		return
	}

	ack, _ := json.Marshal(Envelope{
		Channel: "subscriptionResponse",
		Data:    json.RawMessage(payload),
	})
	if err := c.conn.writeText(ack); err != nil {
		c.conn.close()
	}
}

// Broadcast converts nothing itself; callers pass an already-converted Bbo and
// it is delivered to every client subscribed to that coin, wrapped in the
// {"channel":"bbo","data":...} envelope.
func (s *Server) Broadcast(b Bbo) {
	msg, err := json.Marshal(Envelope{Channel: "bbo", Data: b})
	if err != nil {
		return
	}

	s.mu.RLock()
	targets := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		if c.wants(b.Coin) {
			targets = append(targets, c)
		}
	}
	s.mu.RUnlock()

	for _, c := range targets {
		if err := c.conn.writeText(msg); err != nil {
			c.conn.close() // read loop will unregister it
		}
	}
}

// ClientCount returns the number of connected clients (for status logging).
func (s *Server) ClientCount() int {
	s.mu.RLock()
	n := len(s.clients)
	s.mu.RUnlock()
	return n
}
