package engine

// midpoint.go — Midpoint feed validator (Task 17).
//
// checkMidpoint is the per-feed hook for frames arriving on the mktdata port of
// the Midpoint feed.  It runs only the Tier-2 refdata-consumer checks; Tier-1
// structural checks (MID.STRUCT_LEN_TYPE, MID.METHOD_RANGE, MID.QUALITY_FLAGS,
// MID.TIMESTAMP_ORDERING and the schema-3 Method, Window and Volume rules)
// already ran in checkTier1/checkTier1Mid and MUST NOT be repeated here (no
// duplicate findings).
//
// Rules implemented here:
//
//	MID.METHOD0_REQUIRES_DEFAULT (must, T2, StateRefdata)
//	    Schema 1 only. A Midpoint message with Method==0 ("use instrument
//	    default") is a Violation when the instrument's InstrumentDefinition has
//	    Default Method==0 (no real default defined). Gate: if the channel's
//	    refdata is not yet ready() → NA. Non-zero Method, or schema 3, which has
//	    no Default Method → not applicable. A definition with no Default Method
//	    (a schema-3 definition for a schema-1 message) → unverifiable,
//	    transition.
//
//	MID.SOURCE_ID_MATCHES_DEFINITION (must, T2, StateRefdata)
//	    Schema 3 only. Source ID MUST equal the Source ID in the instrument's
//	    InstrumentDefinition. Gate as above. Schema 1 has no Source ID → not
//	    applicable. A definition with no Source ID (a schema-1 definition for a
//	    schema-3 message) → unverifiable, transition.
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
		schema := f.Header.SchemaVersion
		if want := midpointLen(schema); want == 0 || m.Length != want {
			continue
		}
		e.checkMidpointRefdata(port, f.Header.Sequence, ch, schema, m)
	}
}

// checkMidpointRefdata implements MID.METHOD0_REQUIRES_DEFAULT,
// MID.SOURCE_ID_MATCHES_DEFINITION and MID.PRICE_BOUND for a single Midpoint
// message of canonical length.
//
// Gate: if refdata is nil or the channel has not reached ready(), both rules are
// NA — the instrument may simply not have been delivered yet (cold start).
func (e *Engine) checkMidpointRefdata(port core.Port, seq uint64, ch uint8, schema uint8, m wire.Message) {
	instrID := midpointInstrumentID(m)
	method := midpointMethod(m)
	midPrice := midpointPrice(m)

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

	// The rules below account for the message either way — pass, violation, or NA
	// when this message is not in the rule's scope at all (engine/denominator.go).

	// MID.METHOD0_REQUIRES_DEFAULT: method==0 means "use the instrument's default".
	// If the instrument's Default Method is also 0 (no real default), that is a Violation.
	switch {
	case schema != 1:
		// Schema 3 has no Default Method. MID.METHOD_NOT_UNKNOWN judges Method 0.
		e.inapplicable("MID.METHOD0_REQUIRES_DEFAULT", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: schema %d has no Default Method", instrID, schema))
	case method != 0:
		// The message names its own method, so there is no default to require.
		e.inapplicable("MID.METHOD0_REQUIRES_DEFAULT", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Method=%d does not defer to the instrument default", instrID, method))
	case di.hasSourceID:
		// A schema-3 definition has no Default Method, so defaultMethod is 0
		// by absence, not by declaration.
		e.unverified("MID.METHOD0_REQUIRES_DEFAULT", core.ReasonTransition, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: the accepted definition has no Default Method; it is schema 3", instrID))
	case di.defaultMethod == 0:
		e.Emit("MID.METHOD0_REQUIRES_DEFAULT", core.Violation, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Method=0 but InstrumentDefinition Default Method=0 (no default defined)", instrID))
	default:
		e.passed("MID.METHOD0_REQUIRES_DEFAULT", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Method=0 deferring to Default Method=%d", instrID, di.defaultMethod))
	}

	// MID.SOURCE_ID_MATCHES_DEFINITION: "MUST equal the Source ID in the
	// instrument's InstrumentDefinition".
	src := midpointSourceID(m)
	switch {
	case schema != 3:
		e.inapplicable("MID.SOURCE_ID_MATCHES_DEFINITION", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: a schema-%d Midpoint has no Source ID", instrID, schema))
	case !di.hasSourceID:
		e.unverified("MID.SOURCE_ID_MATCHES_DEFINITION", core.ReasonTransition, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: the accepted definition has no Source ID; it is not schema 3", instrID))
	case src != di.sourceID:
		e.Emit("MID.SOURCE_ID_MATCHES_DEFINITION", core.Violation, port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Source ID %d != InstrumentDefinition Source ID %d", instrID, src, di.sourceID))
	default:
		e.passed("MID.SOURCE_ID_MATCHES_DEFINITION", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Midpoint Source ID %d matches its definition", instrID, src))
	}

	// MID.PRICE_BOUND: check price against the instrument's Price Bound.
	switch di.priceBound {
	case 1, 2:
		// 1 = Bounded [0,1], 2 = Non-negative: raw price must be >= 0 either way.
		// Upper-bound check omitted (requires price exponent; see package comment).
		if midPrice < 0 {
			e.Emit("MID.PRICE_BOUND", core.Violation, port, seq, ch, instrID,
				fmt.Sprintf("instrument %d: Mid Price %d < 0 violates Price Bound=%d", instrID, midPrice, di.priceBound))
			return
		}
		e.passed("MID.PRICE_BOUND", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d: Mid Price %d within Price Bound=%d", instrID, midPrice, di.priceBound))
	default:
		// 0 is "no constraint"; anything above 2 is reserved and deliberately not
		// enforced. Neither is a check that further traffic would enable.
		e.inapplicable("MID.PRICE_BOUND", port, seq, ch, instrID,
			fmt.Sprintf("instrument %d declares Price Bound=%d, which constrains nothing", instrID, di.priceBound))
	}
}

// emitMidNA emits NA for every refdata-gated midpoint rule with the same detail
// string (cold-start / not-ready gate).
func (e *Engine) emitMidNA(port core.Port, seq uint64, ch uint8, instrID uint32, detail string) {
	e.Emit("MID.METHOD0_REQUIRES_DEFAULT", core.NA, port, seq, ch, instrID, detail)
	e.Emit("MID.SOURCE_ID_MATCHES_DEFINITION", core.NA, port, seq, ch, instrID, detail)
	e.Emit("MID.PRICE_BOUND", core.NA, port, seq, ch, instrID, detail)
}
