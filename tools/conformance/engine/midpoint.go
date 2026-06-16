package engine

// midpoint.go — Midpoint feed validator (Task 17).
//
// checkMidpoint is the per-feed hook for frames arriving on the mktdata port of
// the Midpoint feed.  It runs only the Tier-2 refdata-consumer checks; Tier-1
// structural checks (MID.STRUCT_LEN_TYPE, MID.METHOD_RANGE, MID.QUALITY_FLAGS,
// MID.TIMESTAMP_ORDERING) already ran in checkTier1/checkTier1Mid and MUST NOT
// be repeated here (no duplicate findings).
//
// Rules implemented here:
//
//	MID.METHOD0_REQUIRES_DEFAULT (must, T2, StateRefdata)
//	    A Midpoint message with Method==0 ("use instrument default") is a Violation
//	    when the instrument's InstrumentDefinition has Default Method==0 (no real
//	    default defined). Gate: if the channel's refdata is not yet ready() → NA.
//	    Non-zero Method → not applicable (rule fires only on Method==0).
//
//	MID.PRICE_BOUND (must, T2, StateRefdata)
//	    Gate: if the channel's refdata is not yet ready() → NA.
//	    Price Bound values:
//	      0 → no check.
//	      1 → price must be in [0,1] (binary outcome). Raw i64 < 0 → Violation.
//	          Upper bound check is omitted to avoid false positives when the
//	          per-instrument price exponent is not tracked (documentation note below).
//	      2 → non-negative: raw i64 < 0 → Violation.
//
// Note on Price Bound==1 upper-bound check:
// The spec defines Price Bound==1 as "bounded [0,1] (binary outcomes)". A
// correct upper-bound check requires applying the per-instrument price exponent
// to convert the raw i64 to a real value before comparing against 1.  Because
// the conformance engine does not currently track per-instrument exponents for
// the Midpoint feed, only the sign check (raw price < 0) is performed for
// Bound==1.  This avoids false positives at the cost of not catching prices > 1
// in real terms.  The sign check is always correct regardless of the exponent.

import (
	"fmt"

	"github.com/malbeclabs/edge-feed-spec/tools/conformance/core"
	"github.com/malbeclabs/edge-feed-spec/tools/conformance/wire"
)

// checkMidpoint is called from the classify loop for mktdata-port frames on the
// Midpoint feed.  It routes Midpoint (0x03) messages to the refdata-consumer checks.
func (e *Engine) checkMidpoint(f *wire.Frame, port core.Port, ch uint8) {
	for _, m := range f.Messages {
		if m.Type != wire.TypeMidpoint {
			continue
		}
		// Only process canonical-length messages; non-canonical lengths already
		// triggered MID.STRUCT_LEN_TYPE in checkTier1Mid. Reading body bytes from
		// a truncated/oversized message would produce garbage field values.
		if m.Length != 40 {
			continue
		}
		instrID := midpointInstrumentID(m)
		method := midpointMethod(m)
		midPrice := midpointMidPrice(m)
		e.checkMidpointRefdata(port, f.Header.Sequence, ch, instrID, method, midPrice)
	}
}

// checkMidpointRefdata implements MID.METHOD0_REQUIRES_DEFAULT and MID.PRICE_BOUND
// for a single Midpoint message.
//
// Gate: if refdata is nil or the channel has not reached ready(), both rules are
// NA — the instrument may simply not have been delivered yet (cold start).
func (e *Engine) checkMidpointRefdata(port core.Port, seq uint64, ch uint8, instrID uint32, method uint8, midPrice int64) {
	if e.refdata == nil {
		// No refdata frames received at all: cold start, rules are NA.
		e.emitMidNA(port, seq, ch, instrID, "refdata not yet received (cold start)")
		return
	}

	di, ok := e.refdata.defInfoFor(ch, instrID)
	if !ok {
		// Channel not ready or instrument not in def set: NA.
		e.emitMidNA(port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: refdata not yet ready or instrument unknown for channel %d", instrID, ch))
		return
	}

	// MID.METHOD0_REQUIRES_DEFAULT: method==0 means "use the instrument's default".
	// If the instrument's Default Method is also 0 (no real default), that is a Violation.
	if method == 0 && di.defaultMethod == 0 {
		e.Emit("MID.METHOD0_REQUIRES_DEFAULT", core.Violation, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Method=0 but InstrumentDefinition Default Method=0 (no default defined)", instrID))
	}

	// MID.PRICE_BOUND: check price against the instrument's Price Bound.
	switch di.priceBound {
	case 0:
		// no constraint
	case 1:
		// Bounded [0,1]: raw price must be >= 0.
		// Upper-bound check omitted (requires price exponent; see package comment).
		if midPrice < 0 {
			e.Emit("MID.PRICE_BOUND", core.Violation, port, seq, ch, instrID,
				fmt.Sprintf("instrument %d: Mid Price %d < 0 violates Price Bound=1 ([0,1])", instrID, midPrice))
		}
	case 2:
		// Non-negative: raw price must be >= 0.
		if midPrice < 0 {
			e.Emit("MID.PRICE_BOUND", core.Violation, port, seq, ch, instrID,
				fmt.Sprintf("instrument %d: Mid Price %d < 0 violates Price Bound=2 (non-negative)", instrID, midPrice))
		}
	}
}

// emitMidNA emits NA for both MID.METHOD0_REQUIRES_DEFAULT and MID.PRICE_BOUND
// with the same detail string (cold-start / not-ready gate).
func (e *Engine) emitMidNA(port core.Port, seq uint64, ch uint8, instrID uint32, detail string) {
	e.Emit("MID.METHOD0_REQUIRES_DEFAULT", core.NA, port, seq, ch, instrID, detail)
	e.Emit("MID.PRICE_BOUND", core.NA, port, seq, ch, instrID, detail)
}
