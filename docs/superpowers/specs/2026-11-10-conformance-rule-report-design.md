# The rule report: where each rule outcome occurred

**Status:** implemented in the same pull request
**Date:** 2026-11-10
**Applies to:** `tools/conformance` (`dz-conformance`)

## The question

The JSON report (`--json-report`) counts outcomes for each rule. It does not say
where an outcome occurred. A consumer that keeps one row for each rule and
channel instance needs that answer. An example is a recorder that runs the rule
set over its archive and writes the verdicts to a table.

That consumer must do one more thing. It must compare each violation with the
loss that it recorded on the same channel instance. A violation whose evidence
falls in a hole that the recorder made is not the publisher's fault. To make
this comparison, the consumer needs two facts for each outcome:

1. the channel instance: source IP address, `Channel ID` and destination port;
2. the sequence range of the evidence, in one era.

The counts cannot give either fact.

## Decision 1: a second report, behind its own flag

`--rule-report <path>` writes the new report. `--json-report` does not change.

The two reports have incompatible shapes. In the JSON report, an entry in
`rules` is one rule with a map of counts. In the rule report, an entry is one
outcome on one channel instance. One file cannot hold both shapes under one
`rules` key. CI jobs and deploy roles read the JSON report today, so a change to
its shape breaks them.

The rule report declares `report_format: 1`. A reader refuses a number it does
not know. A reader ignores a field it does not know. Adding a field does not
change the number. Changing the meaning of a field does.

## Decision 2: an instance only where the rule judges one path

The engine already decides which findings name a publisher
(`core.StateKind.InstanceScoped`). A finding about the channel has an empty
`source_addr` in the metrics. The rule report follows the same rule. It names an
instance only when the finding names a source IP address and the port role has a
configured port.

An entry with no instance still carries `channel_id` and `port_role`. The report
does not invent an instance. A reader that needs an instance must refuse the
entry and count it. It must not choose a publisher that was near.

## Decision 3: the era comes from the instance's tracker

`core.Finding` gets `ResetCount`, `ResetCountKnown` and `EraIndex`. The engine
reads the era from the tracker of the instance that the finding names, when it
emits the finding.

The engine does not read the era from the datagram in front of it.
`FRAME.MKTDATA_SEQ_START` is emitted after the era advances, and it cites the
sequence number of the new era. Old-era items are classified before the advance,
and they cite old-era numbers. In both cases the tracker holds the era of the
cited number.

The Reset Count is a u8 and wraps after 256 resets. Two eras with the same value
are different eras, and a reader that compares evidence with loss must not join
them. `EraIndex` counts the eras of one channel instance (source IP address,
Channel ID, destination port): 1 for its first era, plus 1 each time the
instance enters a new Reset Count. The report keys on it and writes it as
`era_index` beside `reset_count`.

The tracker's own era ordinal (`eraPos`) is not this count, for two reasons. The
three ports of one source IP address and Channel ID share it, so a snapshot port
that sends only in the first and third mktdata eras would say 1 and 3. And a
tracker that is evicted starts a fresh series, so the ordinal restarts at 1. The
engine therefore keeps a separate count for each instance (`Engine.noteEra`).
The count survives eviction: an instance that comes back in the Reset Count it
held continues that era, and one that comes back in another value starts the
next era. The engine cannot tell a return to the same era from a return after
exactly a multiple of 256 resets, and it reads the first. The count covers at
most 16,384 instances. Past that, a new instance has no count, and its evidence
has no `era_index`, so the report never writes a number that does not count the
instance's eras.

This meaning is part of format 1. Format 1 has not been released, so no format
number changes. A later change of this meaning needs `report_format` 2.

A finding about the channel has no era. Each path of the channel has its own
sequence series, so no single era applies.

## Decision 4: ranges, bounded

Findings with the same rule, outcome, severity, reason, instance and era go into
one key.

- **A `violation` or `suspected` key keeps contiguous ranges, up to 64.** One
  range for all violations would be wrong. A hole between two distant violations
  would then excuse both. Ranges that touch or overlap merge into one, with the
  sum of their counts. Past the bound, the two neighbouring ranges with the
  smallest gap merge. Every finding stays inside a range, a merge spans the
  smallest gap it can, and the file stays bounded.
- **Any other outcome keeps one range**, from the lowest to the highest sequence
  number. A consumer does not test a pass against loss.
- **Each range is one entry, with its own `count` and `detail`.** The detail is
  the message of the earliest finding in that range. The engine emits its
  end-of-run findings in Channel ID and Instrument ID order and not in map order,
  so one input gives one report. The sum of the counts for a
  rule and outcome is the count in the JSON report. A test checks this.
- **Placed keys are bounded.** A live subscription can see an unbounded number
  of source IP addresses and eras, and each one makes new keys. The engine
  evicts its own trackers at a bound, but the report must keep what it recorded.
  It keeps at most 16,384 keys that name an instance. Past that bound a finding
  goes into an overflow entry with no instance, and the report counts it in
  `unplaced_findings`. The counts stay exact, and the report says how much
  placement it lost.

So one (rule, instance) can have several entries: passes, violations in two
ranges, unverifiable checks for two reasons. Reducing them to one verdict is the
consumer's decision.

## The build stamp

A consumer must know which rule set produced a verdict. `--version` prints
`version+commit`, and both reports carry `version` and `commit`. Release builds
set them through `-ldflags`. A build that does not set them prints `dev+none`.
A consumer must refuse that value. This pull request does not change the stamp.

## What this does not do

- It does not change any rule, metric, alert or exit code.
- It does not place a channel-scoped finding on a path.
- It does not keep every finding. A reader that needs each finding reads the
  structured log.
