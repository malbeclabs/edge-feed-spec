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
	// Reason is a bounded, low-cardinality code (e.g. "loss", "cold_start",
	// "bound_subset", "reorder", "transition") used for the unverifiable_total
	// metric's `reason` label. Empty is treated as "unspecified". Keep it enum-like.
	Reason string
	At     time.Time
}
