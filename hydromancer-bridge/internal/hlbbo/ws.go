package hlbbo

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Minimal RFC 6455 websocket server, stdlib-only, sufficient for serving small
// JSON text frames and reading client subscribe messages. It is deliberately
// not a general-purpose websocket library.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// websocket opcodes.
const (
	opText  = 0x1
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

var errNotWebsocket = errors.New("not a websocket upgrade request")

// wsConn is a single upgraded client connection. Writes are serialised by mu so
// the broadcast hub and control replies cannot interleave frames.
type wsConn struct {
	rw *bufio.ReadWriter
	c  io.Closer
	mu sync.Mutex
}

// upgrade performs the RFC 6455 handshake over a hijacked HTTP connection.
func upgrade(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return nil, errNotWebsocket
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errNotWebsocket
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("response writer does not support hijack")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{rw: rw, c: conn}, nil
}

// writeText sends a single unmasked text frame (server frames are never masked).
func (w *wsConn) writeText(payload []byte) error {
	return w.writeFrame(opText, payload)
}

func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var hdr [10]byte
	hdr[0] = 0x80 | opcode // FIN + opcode
	n := len(payload)
	var hn int
	switch {
	case n <= 125:
		hdr[1] = byte(n)
		hn = 2
	case n <= 0xFFFF:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
		hn = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
		hn = 10
	}
	if _, err := w.rw.Write(hdr[:hn]); err != nil {
		return err
	}
	if _, err := w.rw.Write(payload); err != nil {
		return err
	}
	return w.rw.Flush()
}

// readFrame reads one frame, returning its opcode and unmasked payload. It
// transparently fails on fragmented messages, which the subscribe protocol does
// not use.
func (w *wsConn) readFrame() (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(w.rw, h[:]); err != nil {
		return 0, nil, err
	}
	opcode := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := int(h[1] & 0x7F)

	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(w.rw, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(w.rw, ext[:]); err != nil {
			return 0, nil, err
		}
		n = int(binary.BigEndian.Uint64(ext[:]))
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(w.rw, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(w.rw, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return opcode, payload, nil
}

func (w *wsConn) close() error { return w.c.Close() }
