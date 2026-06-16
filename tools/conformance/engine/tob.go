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
//	    If ready() AND the instrument ID is absent from the current def set →
//	    Violation (publisher is emitting market data for an unknown instrument).
//	    If ready() AND the instrument IS known → silent (pass, no emission).

import (
	"fmt"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// checkTOB is called from the classify loop for mktdata-port frames on the TOB
// feed.  It routes Quote and Trade messages to the refdata-consumer check.
func (e *Engine) checkTOB(f *wire.Frame, port core.Port, ch uint8) {
	for _, m := range f.Messages {
		switch m.Type {
		case wire.TypeQuote:
			e.checkTOBRefdata(port, f.Header.Sequence, ch, quoteInstrumentID(m))
		case wire.TypeTrade:
			e.checkTOBRefdata(port, f.Header.Sequence, ch, tradeInstrumentID(m))
		}
	}
}

// checkTOBRefdata implements TOB.QUOTE.REFDATA_KNOWN for a single message.
//
// Semantics:
//   - refdata nil or channel not ready → NA (cold start / not yet collected).
//   - channel ready AND instrID NOT in defs → Violation.
//   - channel ready AND instrID in defs → silent.
func (e *Engine) checkTOBRefdata(port core.Port, seq uint64, ch uint8, instrID uint32) {
	if e.refdata == nil {
		// No refdata frames received at all: cold start, rule is NA.
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.NA, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: refdata not yet received (cold start)", instrID))
		return
	}

	ready, known := e.refdata.channelKnown(ch, instrID)
	if !ready {
		// Channel exists but has not reached ready(): NA — not conclusive.
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.NA, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: refdata not yet ready for channel %d", instrID, ch))
		return
	}

	if !known {
		e.Emit("TOB.QUOTE.REFDATA_KNOWN", core.Violation, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d not in refdata def set for channel %d", instrID, ch))
	}
	// known == true: pass, no emission.
}
