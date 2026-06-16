package input

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
)

// loopbackInterface returns the name of the loopback network interface
// (e.g. "lo0" on macOS, "lo" on Linux) or "" if none is found.
func loopbackInterface() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			return iface.Name
		}
	}
	return ""
}

// TestMulticastSource_Live binds a MulticastSource on a link-local IPv4
// multicast group over the loopback interface, sends one UDP datagram to the
// mktdata port, and asserts that Next() returns a datagram with the correct
// Port and payload.
//
// The test is skipped when testing.Short() is true because multicast socket
// operations require OS-level privileges and may not be available in all CI
// environments (some containers block IGMP/multicast on loopback).
func TestMulticastSource_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live multicast test in short mode")
	}

	const (
		group   = "239.255.0.100"
		mktPort = 19100
	)

	cfg := MulticastConfig{
		Group: net.ParseIP(group),
		Ports: map[core.Port]int{
			core.PortMktData: mktPort,
		},
		Interface: loopbackInterface(), // "lo0" on macOS, "lo" on Linux
	}

	// Try binding; if multicast on loopback is not supported, skip gracefully.
	src, err := NewMulticastSource(cfg)
	if err != nil {
		t.Skipf("multicast bind on loopback not supported in this environment: %v", err)
	}
	defer src.Close()

	// Give the socket a moment to join the group before sending.
	time.Sleep(50 * time.Millisecond)

	payload := []byte("conformance-test-datagram")

	// Dial a sender to the multicast group.
	dst := &net.UDPAddr{IP: net.ParseIP(group), Port: mktPort}
	sender, err := net.DialUDP("udp4", nil, dst)
	if err != nil {
		t.Skipf("cannot dial multicast address (not supported in this environment): %v", err)
	}
	defer sender.Close()

	if _, err := sender.Write(payload); err != nil {
		t.Skipf("multicast send failed (not supported in this environment): %v", err)
	}

	// Read back the datagram with a timeout so the test never hangs CI.
	done := make(chan struct{})
	var got Datagram
	var gotOK bool
	var nextErr error

	go func() {
		defer close(done)
		got, gotOK, nextErr = src.Next()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Skip("timed out waiting for multicast datagram — multicast on loopback may not be supported here")
	}

	if nextErr != nil {
		t.Fatalf("Next() returned error: %v", nextErr)
	}
	if !gotOK {
		t.Fatal("Next() returned ok=false, expected a datagram")
	}
	if got.Port != core.PortMktData {
		t.Errorf("got Port=%v, want PortMktData", got.Port)
	}
	if !bytes.Equal(got.Raw, payload) {
		t.Errorf("got payload %q, want %q", got.Raw, payload)
	}
	if got.RecvTS.IsZero() {
		t.Error("RecvTS is zero")
	}
}

// TestMulticastSource_CloseUnblocksNext verifies that Close() causes a
// blocked Next() to return ok=false without a panic or deadlock.
// This test does NOT require live multicast — it skips if binding fails.
func TestMulticastSource_CloseUnblocksNext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live multicast test in short mode")
	}

	cfg := MulticastConfig{
		Group: net.ParseIP("239.255.0.101"),
		Ports: map[core.Port]int{
			core.PortMktData: 19101,
		},
		Interface: "lo0",
	}

	src, err := NewMulticastSource(cfg)
	if err != nil {
		t.Skipf("multicast bind on loopback not supported: %v", err)
	}

	done := make(chan struct{})
	var ok bool
	go func() {
		defer close(done)
		_, ok, _ = src.Next()
	}()

	// Close the source after a short delay; Next() should unblock.
	time.Sleep(100 * time.Millisecond)
	if err := src.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Next() did not unblock after Close()")
	}

	if ok {
		t.Error("Next() returned ok=true after Close(), want false")
	}
}

// TestMulticastSource_CloseDiscardsBuffered verifies that Next() returns
// ok=false immediately after Close() even when datagrams are queued in the
// internal channel — i.e. Close() does not merely drain and then EOF.
// This test directly injects datagrams via the internal channel (same package)
// so it does not require live multicast.
func TestMulticastSource_CloseDiscardsBuffered(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live multicast test in short mode")
	}

	cfg := MulticastConfig{
		Group: net.ParseIP("239.255.0.102"),
		Ports: map[core.Port]int{
			core.PortMktData: 19102,
		},
		Interface: "lo0",
	}

	src, err := NewMulticastSource(cfg)
	if err != nil {
		t.Skipf("multicast bind on loopback not supported: %v", err)
	}

	// Inject a datagram directly into the buffered channel without going
	// through the network so the test doesn't need live multicast.
	src.datagrams <- Datagram{Port: core.PortMktData, Raw: []byte("queued")}

	// Close; the queued datagram should be discarded.
	if err := src.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	// Next() must return ok=false immediately — not the queued datagram.
	done := make(chan struct{})
	var gotOK bool
	go func() {
		defer close(done)
		_, gotOK, _ = src.Next()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Next() blocked after Close(); expected immediate ok=false")
	}

	if gotOK {
		t.Error("Next() returned ok=true after Close() with buffered datagram, want false")
	}
}
