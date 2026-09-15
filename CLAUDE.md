# Claude Code Instructions

## What this repository is

Wire-format specifications for the DoubleZero Edge multicast feeds, and the tools that check them.

**The specification is the product.** Publishers and subscribers implement against these documents, so a change here is a change to what other repositories must do. Treat every edit as a published interface change, not as documentation.

- Each spec directory (`top-of-book/`, `midpoint/`, `market-by-order/`, `market-by-price/`, `order-intent/`, `perp-stats/`, `reference-data/`) is versioned independently.
- `sources/spec.md` is the public Source ID registry.
- `tools/conformance/` is `dz-conformance`, a strict subscriber that grades a feed against a rule catalog and returns a CI exit code.

## Vocabulary

`GLOSSARY.md` is the authority. Where a word is defined there, use it with that meaning.

Do not rotate synonyms — one word for one thing. A **venue** is not a **matching engine**; a **channel instance** is `(source IP, Channel ID, destination port)` and nothing looser. A `Source ID` names a matching engine, not a venue.

## Versioning is the load-bearing discipline

- Every change classifies as **PATCH** (editorial), **MINOR** (additive) or **MAJOR** (breaking). `VERSIONING.md`'s class table decides this, not judgement. Read it before classifying.
- **MAJOR** increments the `Schema Version` byte *and* updates the version table. Never do one without the other.
- Additive means a new type in a reserved ID range, a new enumerated value, or a field appended within the declared `Message Length`. Anything that moves or resizes an existing field, changes a message's length, or redefines a field is MAJOR.
- Tags are `<spec>/vMAJOR.MINOR.PATCH`. A change to one spec is not a change to its siblings — check whether the Reference Data supplement or a sibling feed is affected, and say so in the PR.
- **A retired layout survives only in its tag.** The 80-byte `InstrumentDefinition` (schema 1) is in no current spec file; it lives in `top-of-book/v1.0.0`. Verify a historical offset against the tag, never against HEAD:
  ```
  git show top-of-book/v1.0.0:top-of-book/spec.md
  ```

## The conformance tool

- **Strict by design.** Unlike a production consumer, which tolerates publisher quirks, it flags every structural, sequence and semantic violation its catalog covers and never excuses one. `tools/conformance/core/registry.go` is the in-code source of truth for the rule set.
- **Coverage versus silence.** A rule that quietly stops running reports exactly what a rule that ran and found nothing reports. Every conditional rule accounts for each opportunity with one of `pass` / `violation` / `unverifiable` / `na`, and `unverifiable_total{reason}` must be read alongside the pass counts. Preserve this property in anything you add.
- **Never add a test that cannot fail.** Prefer enumerating through the production function over maintaining a parallel list beside it — a hand-kept list stops covering new cases silently, which is the same failure the tool exists to catch in feeds.
- It decodes more than one MAJOR version per feed. That is a deliberate, documented exception to `VERSIONING.md`'s reject rule, scoped to this tool; production consumers keep the rule.

## This repository is public

Half the surrounding repositories are private. Anything keyed by venue can disclose a venue that has not announced.

- Deploy configuration, inventory, per-venue tables and anything naming a venue's hosts belong in `malbeclabs/infra`, which is private. Code that names no venue belongs here.
- A venue that has not announced gets a codename when its Source ID is claimed — see the Kalshi row in `sources/spec.md`, registered as `Lashay` until launch. The mechanism works at registration time and does not work retroactively.

## Specs and plans

Design specs go in `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`, plans in `docs/superpowers/plans/`.

**That path is listed in `.gitignore` and the files under it are tracked anyway.** Use `git add -f` to commit one. Do not "fix" the `.gitignore` as a side effect — the tracked files are the convention.

## Commands

```
cd tools/conformance
go build ./...
go vet ./...
go test -count=1 ./...
golangci-lint run          # CI pins v2.11, working-directory tools/conformance
```

Use `-count=1`. A cached pass can hide a change you just made, and this repository's tests are cheap enough that there is no reason to trust the cache.

Alert rules are checked with `promtool` against `tools/conformance/prometheus/alerts/conformance.yml` and unit-tested against `tools/conformance/prometheus/rule_tests/conformance_test.yml`; see `.github/workflows/ci.yml` for the exact invocation.

Go version: 1.25.x.

## Git Commits

- Do not add "Co-Authored-By" lines to commit messages
- Use the format `component: short description` (e.g. `conformance: key the canonical message length on schema version`, `docs: record the validator's multi-schema exception`)
- Keep the description lowercase (except proper nouns) and concise

## Before opening a pull request

Four checks, in this order. They are not a substitute for review — four reviewers found nine things on #70 and #72 and no two findings overlapped. They exist so a reviewer's time goes on the findings only another mind can produce, rather than on these four, all of which have already shipped here once.

1. **Every factual claim about the code is verified by reading the code.** Not from memory, not from what the code did last week. A design doc asserted "the sentinel already holds both numbers" when `run.go` drops `dg.RecvTS` before `Process` ever sees it, and that one sentence priced a whole section wrong.
2. **After rewriting a section, grep for what it withdrew.** Rewriting §4 around the feed registry retired a mechanism that five later passages still depended on, so a reader implementing the plan would have built the exact thing §4 said not to build.
3. **After amending a summary, constraint or table, re-read the steps that implement it.** Amending a plan's constraints and File Structure while leaving its executable steps alone produced a document that told a worker to call a function no step defined.
4. **When adding or changing a rule or a metric, grep `prometheus/` for anything that keys on it.** A new info-severity rule whose correct steady state is one finding per frame and never a pass is the exact shape `ConformanceRuleNoLongerVerifying` treats as a rule that stopped checking — so it would have held a permanent warning on every venue it was written to serve. No test catches this; only the grep does.

The common thread in all four: an assertion nobody verified, or one place edited and its dependents left behind. Both are mechanical, which is why they belong in a checklist rather than in a reviewer's head.

## Pull Requests

- Follow `.github/pull_request_template.md`: **Summary of Changes**, **Spec Impact**, **Review Notes**. Add a **Testing Verification** section.
- **Spec Impact is not optional.** State the version level, whether the `Schema Version` byte moves, and whether sibling specs or the Reference Data supplement are affected. A PR that touches a spec and leaves this blank is not reviewable.
- PR title format: `component: short description`, same as commits
- Do not use TODO checkboxes in the testing section — describe what was actually verified
- Do not include "Generated with Claude Code" or similar footers
- Focus on "what" and "why", not implementation details
- Don't mention table-stakes items in testing verification (compiles cleanly, no lint errors). Only meaningful verification: specific scenarios, behavioural observations, edge cases validated
