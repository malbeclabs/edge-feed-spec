package engine

// mbp_replacement_test.go — a replacement publisher on the same Channel ID.
//
// The market-by-price state is keyed by channel. A publisher host is replaced: the
// old one stops, and a new one starts the same Channel ID from a new source IP address,
// with its own Per-Instrument Seq series and its own ladders. If the new address
// does not trigger a wipe (its Reset Count is the era the channel's state is built
// for, or the era the channel last left), the old session's per-instrument state
// stays. These tests pin that the new session is then not judged against it.

import (
	"net/netip"
	"testing"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
	wb "github.com/malbeclabs/edge-feed-spec/tools/conformance/wire/wirebuild"
)

// mbpSession is one publisher session for instrument 1. Each delta adds a bid at
// priceBase + its Per-Instrument Seq, and the session keeps the ladder it has
// published, so a group states the whole ladder as of its Last Instrument Seq.
// frameSeq holds the next frame sequence number per port of this address.
type mbpSession struct {
	src       netip.Addr
	ch, era   uint8
	priceBase int64
	frameSeq  map[core.Port]uint64
	levels    [][2]int64
}

func newMBPSession(src netip.Addr, era uint8, priceBase int64) *mbpSession {
	return &mbpSession{src: src, ch: 101, era: era, priceBase: priceBase, frameSeq: map[core.Port]uint64{}}
}

func (s *mbpSession) frame(port core.Port, typ, length uint8, flags uint16, body func(*wb.Body)) mbpAddrStep {
	seq := s.frameSeq[port]
	s.frameSeq[port]++
	return mbpAddrStep{s.src, mbpFrame(port, seq, s.ch, s.era, typ, length, flags, body)}
}

// deltas returns n LevelUpdate(New) deltas from Per-Instrument Seq first.
func (s *mbpSession) deltas(first uint32, n int) []mbpAddrStep {
	var out []mbpAddrStep
	for k := first; k < first+uint32(n); k++ {
		price := s.priceBase + int64(k)
		out = append(out, s.frame(core.PortMktData, wire.TypeLevelUpdate, 48, 0,
			mbpLevelUpdate(1, mbpClearSideBid, mbpActionNew, k, price, 10)))
		s.levels = append(s.levels, [2]int64{price, 10})
	}
	return out
}

// group returns one snapshot group of the session's ladder at Last Instrument Seq
// lastK. mutate changes the quantity of its first level, which is a real publisher
// mismatch.
func (s *mbpSession) group(lastK, snapID uint32, mutate bool) []mbpAddrStep {
	anchor := uint64(lastK)
	out := []mbpAddrStep{s.frame(core.PortSnapshot, wire.TypeSnapshotBegin, 40, 1,
		mbpSnapBegin(1, anchor, uint32(len(s.levels)), snapID, lastK, 0))}
	for j, l := range s.levels {
		qty := uint64(l[1])
		if mutate && j == 0 {
			qty++
		}
		out = append(out, s.frame(core.PortSnapshot, wire.TypeSnapshotLevel, 32, 1,
			mbpSnapLevel(snapID, l[0], qty, mbpClearSideBid)))
	}
	return append(out, s.frame(core.PortSnapshot, wire.TypeSnapshotEnd, 20, 1, mbpSnapEnd(1, anchor, snapID)))
}

// cycles returns n cycles of two deltas and a group, the first delta at
// Per-Instrument Seq first and the first group at snapID. mutate applies to the
// last group.
func (s *mbpSession) cycles(first uint32, n int, snapID uint32, mutate bool) []mbpAddrStep {
	var out []mbpAddrStep
	for c := 0; c < n; c++ {
		out = append(out, s.deltas(first, 2)...)
		first += 2
		out = append(out, s.group(first-1, snapID, mutate && c == n-1)...)
		snapID++
	}
	return out
}

// TestMBPReplacementPublisherIsNotJudgedAgainstTheOldLadder: the old session ends at
// Per-Instrument Seq 106 with a complete ladder. The replacement starts on a new
// address in the same Reset Count, so mbpObserveEra does not wipe, and its series
// is either below the old one or above it. Neither case is a publisher defect.
func TestMBPReplacementPublisherIsNotJudgedAgainstTheOldLadder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first uint32
		// tail is the number of deltas the old session sends after its last group,
		// which stay in the journal past the baseline.
		tail int
	}{
		{"new_series_below_the_old_one", 1, 0},
		{"new_series_above_the_old_one", 5000, 0},
		{"old_session_ends_between_groups", 1, 4},
		// The new series is past the old baseline and below the old session's last
		// delta, so the old deltas past the baseline replay into each comparison.
		{"new_series_inside_the_old_journal", 109, 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := newMBPSession(srcA, 3, 1000)
			replacement := newMBPSession(srcB, 3, 9000)
			steps := old.cycles(101, 3, 1, false)
			steps = append(steps, old.deltas(107, tc.tail)...)
			steps = append(steps, replacement.cycles(tc.first, 4, 1, false)...)
			fs := runMBPAddrSteps(t, steps)

			for _, rule := range []string{"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", "MBP.DELTA.PERINSTR_DENSITY"} {
				if viol, _ := countByStatus(t, fs, rule); viol != 0 {
					t.Errorf("%s: %d violation(s) against a replacement publisher that is conformant on its own; it was judged against the old session's state",
						rule, viol)
				}
			}
			// The replacement's later groups are compared, not only skipped.
			passes := 0
			for _, f := range fs {
				if f.RuleID == "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT" && f.Status == core.Pass {
					passes++
				}
			}
			if passes < 2+2 {
				t.Errorf("MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT: %d pass(es), want at least 4 (2 per session after its baseline)", passes)
			}
		})
	}
}

// TestMBPReplacementGroupBeforeItsDeltas: the replacement's first datagram the
// engine classifies is a group, past the old baseline and inside the old journal,
// and its deltas follow. The group cannot be diffed against the old session's
// ladder; it is unverifiable, and the replacement's later groups are compared.
func TestMBPReplacementGroupBeforeItsDeltas(t *testing.T) {
	old := newMBPSession(srcA, 3, 1000)
	replacement := newMBPSession(srcB, 3, 9000)
	steps := old.cycles(101, 3, 1, false)
	steps = append(steps, old.deltas(107, 14)...)
	// The replacement published 109 and 110 before the engine saw it.
	replacement.levels = [][2]int64{{9109, 10}, {9110, 10}}
	steps = append(steps, replacement.group(110, 1, false)...)
	steps = append(steps, replacement.cycles(111, 3, 2, false)...)
	fs := runMBPAddrSteps(t, steps)

	for _, rule := range []string{"MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT", "MBP.DELTA.PERINSTR_DENSITY"} {
		if viol, _ := countByStatus(t, fs, rule); viol != 0 {
			t.Errorf("%s: %d violation(s) against a replacement publisher that is conformant on its own", rule, viol)
		}
	}
}

// TestMBPReplacementPublisherMismatchIsStillReported: the converse. Once the
// replacement has a baseline of its own, a group that disagrees with its own deltas
// is still a violation.
func TestMBPReplacementPublisherMismatchIsStillReported(t *testing.T) {
	old := newMBPSession(srcA, 3, 1000)
	replacement := newMBPSession(srcB, 3, 9000)
	steps := old.cycles(101, 3, 1, false)
	steps = append(steps, replacement.cycles(1, 4, 1, true)...)
	fs := runMBPAddrSteps(t, steps)
	if viol, _ := countByStatus(t, fs, "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"); viol != 1 {
		t.Errorf("MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT: want 1 violation for the replacement's own mismatch, got %d", viol)
	}
}

// TestMBPReplacementTwoGroupsBeforeItsDeltas: like the test above, with a second
// replacement group before the replacement's first delta. The first group is
// adopted from a new address, so the old session's lastSeq and its journal past the
// group's Last Instrument Seq are still in place when the second group arrives. The
// second group is not diffed against them: it is unverifiable until a delta from
// the replacement's address is applied, and the replacement's later groups compare.
func TestMBPReplacementTwoGroupsBeforeItsDeltas(t *testing.T) {
	old := newMBPSession(srcA, 3, 1000)
	replacement := newMBPSession(srcB, 3, 9000)
	steps := old.cycles(101, 3, 1, false)
	steps = append(steps, old.deltas(107, 14)...)
	replacement.levels = [][2]int64{{9110, 10}}
	steps = append(steps, replacement.group(110, 1, false)...)
	replacement.levels = [][2]int64{{9110, 10}, {9111, 10}, {9112, 10}, {9113, 10}, {9114, 10}, {9115, 10}}
	steps = append(steps, replacement.group(115, 2, false)...)
	replacement.levels = [][2]int64{{9110, 10}}
	steps = append(steps, replacement.deltas(111, 5)...)
	steps = append(steps, replacement.cycles(116, 3, 3, false)...)
	fs := runMBPAddrSteps(t, steps)

	const oracle = "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"
	for _, rule := range []string{oracle, "MBP.DELTA.PERINSTR_DENSITY"} {
		if viol, _ := countByStatus(t, fs, rule); viol != 0 {
			t.Errorf("%s: %d violation(s) against a replacement publisher that is conformant on its own", rule, viol)
		}
	}
	passes := 0
	for _, f := range fs {
		if f.RuleID == oracle && f.Status == core.Pass {
			passes++
		}
	}
	if passes == 0 {
		t.Errorf("%s: no pass for the replacement's later groups", oracle)
	}
}

// TestMBPTwoLivePathsOnOneChannelAreNotJudged pins what the engine does with two
// paths that serve one Channel ID at the same time with the same Per-Instrument Seq
// series, which the spec allows (Redundant Channel Instances). The per-instrument
// state is keyed by channel, not by channel instance, so the two paths feed one
// tracker, and each delta from the other address that does not continue the series
// reads as a new session and restarts the instrument. The intended result is the
// conservative one: no finding is fabricated, the reconstruction rule reports
// unverifiable rather than staying silent, and a real gap or a real mismatch on one
// path is missed. Keying the per-instrument state by channel instance (see the
// README's known limitations) is what would let these rules judge each path.
func TestMBPTwoLivePathsOnOneChannelAreNotJudged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gap    bool // path A skips Per-Instrument Seq 8
		mutate bool // path A's last group disagrees with its deltas
	}{
		{name: "conformant"},
		{name: "gap on one path", gap: true},
		{name: "mismatch on one path", mutate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newMBPSession(srcA, 3, 1000)
			b := newMBPSession(srcB, 3, 1000)
			var steps []mbpAddrStep
			k, snapID := uint32(1), uint32(1)
			for c := 0; c < 4; c++ {
				for j := 0; j < 2; j++ {
					if !(tc.gap && k == 8) {
						steps = append(steps, a.deltas(k, 1)...)
					} else {
						a.levels = append(a.levels, [2]int64{a.priceBase + int64(k), 10})
					}
					steps = append(steps, b.deltas(k, 1)...)
					k++
				}
				steps = append(steps, a.group(k-1, snapID, tc.mutate && c == 3)...)
				steps = append(steps, b.group(k-1, snapID, false)...)
				snapID++
			}
			fs := runMBPAddrSteps(t, steps)

			const oracle = "MBP.SNAP.RECONSTRUCTED_BOOK_MATCHES_SNAPSHOT"
			for _, rule := range []string{oracle, "MBP.DELTA.PERINSTR_DENSITY"} {
				if viol, _ := countByStatus(t, fs, rule); viol != 0 {
					t.Errorf("%s: %d violation(s) on two paths whose state the engine cannot tell apart", rule, viol)
				}
			}
			unverifiable := 0
			for _, f := range fs {
				if f.RuleID != oracle {
					continue
				}
				switch f.Status {
				case core.Pass:
					t.Errorf("%s: a pass on two paths whose state the engine cannot tell apart: %s", oracle, f.Detail)
				case core.Unverifiable:
					unverifiable++
				}
			}
			if unverifiable != 8 {
				t.Errorf("%s: %d unverifiable finding(s), want one per group (8)", oracle, unverifiable)
			}
		})
	}
}
