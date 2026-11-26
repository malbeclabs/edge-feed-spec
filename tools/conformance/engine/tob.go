package engine

// tob.go — TOB feed validator (Task 16).
//
// checkTOB is the per-feed hook for frames arriving on the mktdata port of the
// TOB feed.  It runs only the Tier-2 refdata-consumer checks; Tier-1 structural
// checks (TOB.QUOTE.STRUCT_LEN_TYPE, TOB.QUOTE.GONE_VS_ZERO_PRICE, etc.) already
// ran in checkTier1/checkTier1TOB and MUST NOT be repeated here (no duplicate
// findings).
//
// Rule implemented here:
//
//	TOB.QUOTE.REFDATA_KNOWN (should, T2, StateRefdata)
//	    Applicable to both Quote (0x03) and Trade (0x04) messages.
//	    Gate: if the channel's refdata has not yet reached ready(), the instrument
//	    may simply not be delivered yet (cold start) → emit NA, never a Violation.
//	    If ready() AND the instrument IS known → Pass.
//	    If ready() AND the instrument ID is absent from the current def set, the
//	    verdict waits for the refdata that the publisher sent at or before the
//	    message (see holdUnknown), and is then decided on the set current at that
//	    point: Pass, NA or Violation.

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// checkTOB is called from the classify loop for mktdata-port frames on the TOB
// feed.  It routes Quote and Trade messages to the refdata-consumer check.
func (e *Engine) checkTOB(f *wire.Frame, port core.Port, ch uint8) {
	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeQuote:
			e.checkTOBRefdata(port, f.Header.Sequence, f.Header.SendTS, ch, quoteInstrumentID(m))
		case wire.TypeTrade:
			e.checkTOBRefdata(port, f.Header.Sequence, f.Header.SendTS, ch, tradeInstrumentID(m))
		}
	}
}

// checkTOBRefdata implements TOB.QUOTE.REFDATA_KNOWN for a single message.
//
// Semantics:
//   - refdata nil or channel not ready → NA (cold start / not yet collected).
//   - channel ready AND instrID in defs → Pass.
//   - channel ready AND instrID NOT in defs → the verdict waits for the refdata
//     sent at or before the message; see holdUnknown.
//
// The NA arms were always reported; the passing arm was silent, which left the
// rule's denominator short by exactly the messages it judged clean — a channel
// whose refdata never arrives and one whose instruments all check out both showed
// zero. See engine/denominator.go.
func (e *Engine) checkTOBRefdata(port core.Port, seq, sendTS uint64, ch uint8, instrID uint32) {
	if e.refdata == nil {
		// No refdata frames received at all: cold start, rule is NA.
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.NA, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: refdata not yet received (cold start)", instrID))
		return
	}

	k, s := e.refdata.forChannelKey(ch)
	if s != nil && len(s.heldUnknown) > 0 && e.refdataStopped(k, sendTS) {
		// The refdata path stopped, so no refdata datagram releases the held
		// verdicts before Flush. Decide them now.
		e.releaseUnknown(k, s, sendTS)
	}
	if s == nil || !channelReady(s) {
		// Channel exists but has not reached ready(): NA — not conclusive.
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.NA, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: refdata not yet ready for channel %d", instrID, ch))
		return
	}
	if _, known := s.defs[instrID]; known {
		e.passed("TOB.QUOTE.REFDATA_KNOWN", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d is in channel %d's refdata def set", instrID, ch))
		return
	}
	e.holdUnknown(k, s, heldUnknown{
		src: e.curSrc, port: port, seq: seq, sendTS: sendTS, ch: ch, instrID: instrID,
		unknownSchema: e.curUnknownSchema,
	})
}

// heldUnknown is a TOB.QUOTE.REFDATA_KNOWN verdict for an Instrument ID that is
// not in a ready set, held until the refdata instance has classified every
// datagram it holds from at or before the message's Send Timestamp.
//
// It keeps the context that Emit reads from the datagram in front of the engine,
// because another datagram is in front of the engine when the verdict is decided.
type heldUnknown struct {
	src           netip.Addr // the source IP address of the mktdata datagram
	port          core.Port
	seq           uint64
	sendTS        uint64
	ch            uint8
	instrID       uint32
	unknownSchema bool // Engine.curUnknownSchema for the mktdata datagram
}

// maxHeldUnknown bounds the held verdicts of one refdata instance. A verdict
// waits only while the instance's reorder buffer holds a datagram from before
// the message, which is a few seconds of messages for Instrument IDs that are not
// in the set. Past the bound, the oldest verdict is reported Unverifiable.
const maxHeldUnknown = 4096

// unknownSilence is the time after which a refdata path that sent nothing is
// stopped (see refdataStopped). A path that serves sends a ManifestSummary at
// least once per manifest cadence, and the recommended cadence is 1 second
// (reference-data/spec.md). The value is ten of those, or two configured
// cadences if that is more.
const unknownSilence = 10 * time.Second

// holdUnknown decides an Instrument ID that is not in a ready set, or holds the
// verdict until it can be decided.
//
// **The set the message is judged against is not yet the publisher's set at the
// message.** Each (source, port, channel) series has its own reorder buffer, and
// the refdata port sends a few datagrams a second. So the engine classifies a
// refdata datagram several seconds after it arrives, and a mktdata datagram sent
// after it is classified first. A publisher that adds an instrument sends the
// ManifestSummary with the new Manifest Seq first, then the first quote for the
// new Instrument ID (reference-data/spec.md, rule 5). Judged at once, that quote
// is judged against the previous set, which is still ready and does not hold the
// ID: a Violation that a subscriber reading in arrival order never sees.
//
// So the verdict waits while the refdata buffer of the instance holds a datagram
// with a Send Timestamp at or before the message's (refdataUnclassifiedWithin).
// After that, the set is the one the publisher had declared when it sent the
// message, and releaseUnknown decides on it:
//
//   - The set is ready and holds the ID → Pass.
//   - The set is not ready → NA. The publisher changed the set, and a subscriber
//     has not collected the new one yet.
//   - The set is ready and does not hold the ID → Violation. An ID that is in no
//     set stays a Violation.
//
// When the buffer holds no such datagram, the verdict is decided now. When the
// refdata path stopped sending, its buffer is not released before Flush, so the
// verdict is Unverifiable, reason pending.
func (e *Engine) holdUnknown(k refdataKey, s *channelRefdataState, h heldUnknown) {
	pending, stranded := e.refdataUnclassifiedWithin(k, 0, h.sendTS, h.sendTS, e.unknownSilence())
	switch {
	case stranded:
		e.unverified("TOB.QUOTE.REFDATA_KNOWN", core.ReasonPending, h.port, h.seq, h.ch, h.instrID,
			fmt.Sprintf("instrument %d not in refdata def set for channel %d, and the refdata path stopped with datagrams from before the message unclassified", h.instrID, h.ch))
	case pending:
		if len(s.heldUnknown) >= maxHeldUnknown {
			old := s.heldUnknown[0]
			s.heldUnknown = s.heldUnknown[1:]
			e.emitHeld(old, func() {
				e.unverified("TOB.QUOTE.REFDATA_KNOWN", core.ReasonOverflow, old.port, old.seq, old.ch, old.instrID,
					fmt.Sprintf("instrument %d not in refdata def set for channel %d; the verdict waited for refdata and the held verdicts reached %d", old.instrID, old.ch, maxHeldUnknown))
			})
		}
		s.heldUnknown = append(s.heldUnknown, h)
	default:
		e.decideUnknown(s, h)
	}
}

// releaseUnknown decides each held verdict of one refdata instance whose refdata
// is now classified through the message's Send Timestamp, and keeps the others.
// nowTS is the clock that tells whether the refdata path stopped; zero reads each
// verdict's own Send Timestamp. Called after each refdata datagram of the
// instance, from checkTOBRefdata when the path stopped, and from Flush.
func (e *Engine) releaseUnknown(k refdataKey, s *channelRefdataState, nowTS uint64) {
	if len(s.heldUnknown) == 0 {
		return
	}
	held := s.heldUnknown
	s.heldUnknown = nil
	for _, h := range held {
		pending, stranded := e.refdataUnclassifiedWithin(k, 0, h.sendTS, max(nowTS, h.sendTS), e.unknownSilence())
		switch {
		case stranded:
			e.emitHeld(h, func() {
				e.unverified("TOB.QUOTE.REFDATA_KNOWN", core.ReasonPending, h.port, h.seq, h.ch, h.instrID,
					fmt.Sprintf("instrument %d not in refdata def set for channel %d, and the refdata path stopped with datagrams from before the message unclassified", h.instrID, h.ch))
			})
		case pending:
			s.heldUnknown = append(s.heldUnknown, h)
		default:
			e.decideUnknown(s, h)
		}
	}
}

// dropUnknown reports the held verdicts of a refdata instance that the engine
// stops tracking. Its reorder buffer is discarded, not classified, so the
// datagrams that could decide the verdicts are never read.
func (e *Engine) dropUnknown(s *channelRefdataState) {
	for _, h := range s.heldUnknown {
		e.emitHeld(h, func() {
			e.unverified("TOB.QUOTE.REFDATA_KNOWN", core.ReasonTruncated, h.port, h.seq, h.ch, h.instrID,
				fmt.Sprintf("instrument %d not in refdata def set for channel %d, and the tracker map evicted the refdata instance before its datagrams from before the message were classified", h.instrID, h.ch))
		})
	}
	s.heldUnknown = nil
}

// decideUnknown decides a verdict on the set of the instance as it is now.
func (e *Engine) decideUnknown(s *channelRefdataState, h heldUnknown) {
	e.emitHeld(h, func() {
		if !channelReady(s) {
			e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.NA, h.port, h.seq, h.ch, h.instrID,
				fmt.Sprintf("instrument %d: the refdata set of channel %d changed before the message, and is not ready yet", h.instrID, h.ch))
			return
		}
		if _, known := s.defs[h.instrID]; known {
			e.passed("TOB.QUOTE.REFDATA_KNOWN", h.port, h.seq, h.ch, h.instrID,
				fmt.Sprintf("instrument %d is in channel %d's refdata def set, after refdata sent before the message was classified", h.instrID, h.ch))
			return
		}
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.Violation, h.port, h.seq, h.ch, h.instrID,
			fmt.Sprintf("instrument %d not in refdata def set for channel %d", h.instrID, h.ch))
	})
}

// emitHeld runs fn with the context of the mktdata datagram that the held verdict
// is about, and then puts back the context of the datagram in front of the engine.
func (e *Engine) emitHeld(h heldUnknown, fn func()) {
	src, unknown := e.curSrc, e.curUnknownSchema
	e.curSrc, e.curUnknownSchema = h.src, h.unknownSchema
	defer func() { e.curSrc, e.curUnknownSchema = src, unknown }()
	fn()
}

// refdataStopped reports whether the refdata path of one instance sent nothing
// for longer than unknownSilence before nowTS.
func (e *Engine) refdataStopped(k refdataKey, nowTS uint64) bool {
	pt, ok := e.ports[instanceKey{src: k.src, port: core.PortRefData, ch: k.ch}]
	return ok && pt.stoppedAt(nowTS, e.unknownSilence())
}

// unknownSilence returns the silence after which a refdata path is stopped for
// the held TOB.QUOTE.REFDATA_KNOWN verdicts.
func (e *Engine) unknownSilence() time.Duration {
	return max(unknownSilence, 2*e.cfg.ExpectManifestCadence)
}
