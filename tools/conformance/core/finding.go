package core

import "time"

type Severity int

const (
	Info Severity = iota
	Should
	Must
)

func (s Severity) String() string {
	switch s {
	case Must:
		return "must"
	case Should:
		return "should"
	default:
		return "info"
	}
}

type Status int

const (
	Pass         Status = iota
	Violation           // confirmed, verified publisher non-conformance
	Suspected           // first oracle mismatch, awaiting confirmation; does not fail CI
	Unverifiable        // loss/cold-start/reorder/bound-subset made the check unprovable
	NA                  // rule not applicable (e.g. required port unbound)
)

func (s Status) CountsAsViolation() bool { return s == Violation }

type StateKind int

const (
	StateNone StateKind = iota
	StateCounters
	StateOrderIDSet
	StateFullBook
	StateRefdata
	StateSnapshotGroup
)

type Feed string

const (
	FeedTOB      Feed = "tob"
	FeedMidpoint Feed = "midpoint"
	FeedMBO      Feed = "mbo"
	FeedMBP      Feed = "mbp"
)

type Port uint8

const (
	PortMktData Port = iota
	PortRefData
	PortSnapshot
)

func (p Port) String() string {
	switch p {
	case PortRefData:
		return "refdata"
	case PortSnapshot:
		return "snapshot"
	default:
		return "mktdata"
	}
}

// The bounded vocabulary for Finding.Reason.
//
// Every one of these answers a single question — *what stopped this check from
// deciding?* — for an operator reading `unverifiable_total{reason}`. They are the
// breakdown of a conditional rule's shortfall against its denominator (see
// engine/denominator.go), which is why the set is closed: the label is on a
// high-rate counter, so a free-form cause would be unbounded cardinality.
//
// Add a value only when an existing one would misname the cause. A reason that
// merely restates the rule is not a cause.
const (
	// ReasonLoss — a datagram went missing and could have carried what the check
	// needed. Never a publisher fault.
	ReasonLoss = "loss"
	// ReasonColdStart — the subscriber joined mid-stream, or the state the check
	// reads has not been established yet.
	ReasonColdStart = "cold_start"
	// ReasonReorder — the thing to check arrived out of order relative to the
	// state it must be compared against.
	ReasonReorder = "reorder"
	// ReasonPending — the state exists but has not advanced to the point of
	// comparison. The ports drain independently, so this is routine.
	ReasonPending = "pending"
	// ReasonOverflow — a bounded buffer discarded the window the check needed.
	ReasonOverflow = "overflow"
	// ReasonTruncated — the observation window closed mid-unit (end of capture).
	ReasonTruncated = "truncated"
	// ReasonInsufficientWindow — the window was clean but did not contain enough
	// of the thing to decide (fewer cycles than the rule requires).
	ReasonInsufficientWindow = "insufficient_window"
	// ReasonSuperseded — not evaluated because another finding already explains
	// this unit. Reporting both would name one defect twice.
	ReasonSuperseded = "superseded"
	// ReasonUntrusted — the instrument's history is not established: it was first
	// seen mid-stream, or a prior per-instrument gap cleared its trust.
	ReasonUntrusted = "untrusted"
	// ReasonBoundSubset — the capture covers a subset of the deployment, so the
	// absence the check reads may be outside it rather than missing.
	ReasonBoundSubset = "bound_subset"
	// ReasonTransition — an in-flight transition (reset era, manifest bump) makes
	// the two sides of the check belong to different regimes.
	ReasonTransition = "transition"
	// ReasonUnspecified is what a reporter substitutes for an empty Reason. Never
	// pass it explicitly — name the cause.
	ReasonUnspecified = "unspecified"
)

// reasons is the closed set above, for validation.
var reasons = map[string]struct{}{
	ReasonLoss: {}, ReasonColdStart: {}, ReasonReorder: {}, ReasonPending: {},
	ReasonOverflow: {}, ReasonTruncated: {}, ReasonInsufficientWindow: {},
	ReasonSuperseded: {}, ReasonUntrusted: {}, ReasonBoundSubset: {},
	ReasonTransition: {}, ReasonUnspecified: {},
}

// ValidReason reports whether s is a member of the closed reason vocabulary. The
// empty string is valid: it is what a finding that needs no reason carries, and
// reporters substitute ReasonUnspecified for it.
func ValidReason(s string) bool {
	if s == "" {
		return true
	}
	_, ok := reasons[s]
	return ok
}

// Finding is the unit of output. Reporters consume it.
type Finding struct {
	RuleID       string
	Severity     Severity
	Status       Status
	Feed         Feed
	Port         Port
	ChannelID    uint8
	InstrumentID uint32 // 0 when not instrument-scoped
	Seq          uint64
	Detail       string // free-form, for logs only — NEVER a metric label (unbounded)
	// Reason is a bounded, low-cardinality code used for the unverifiable_total
	// metric's `reason` label — one of the Reason* constants above. Empty is
	// treated as ReasonUnspecified.
	Reason string
	At     time.Time
}
