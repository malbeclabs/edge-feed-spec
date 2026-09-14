---
name: code-review
description: Review a pull request for DoubleZero Edge glossary conformance. Use on every pull request. Checks prose, identifiers, CLI flags, config keys, metric names, and log fields against GLOSSARY.md at the root of this repository.
---

# Code Review: Glossary Conformance

## Authority

`GLOSSARY.md` at the root of this repository is the vocabulary, and this
repository owns it. It is the sole authority, and every other repository carries
a vendored copy of it. There is no second copy here.

A definition in the glossary overrides any local one, wherever it appears. This
repository may add a term that it alone needs. It may not redefine a term that
the glossary lists.

Read `GLOSSARY.md` before you review. Do not review from memory. The banned-word
table and the notes below it carry exceptions, and a review that misses an
exception reports a false finding.

A change to `GLOSSARY.md` itself is in scope for a different reason. Check that
the change follows the Versioning section: an editorial change is a `PATCH`, a
new term or a new exception is a `MINOR`, and a redefinition that makes
conforming text wrong is a `MAJOR`. Check that the Changes list records it.

## Scope

Check every line that the pull request adds or rewrites:

- Prose: specs, docs, READMEs, plans, doc comments, and code comments.
- The pull request title and description.
- Names that the change introduces: identifiers, type names, CLI flags, config
  keys, metric names, and log fields. The glossary binds these too.

Leave unchanged lines alone. A banned word that this change does not touch is
out of scope for this review.

Do not judge `GLOSSARY.md` against itself. It lists every banned word in its
tables and explains each one, so checking it reports the vocabulary back as a
wall of findings. Review a change to it against the Versioning rules above
instead.

Skip a quoted definition elsewhere, for the same reason. Naming a banned word to
explain that it is banned is correct use, and the glossary does this itself.

## What to flag

Flag an added or rewritten line when it does one of these:

- Uses a word from the banned-word table outside a documented exception.
- Redefines a glossary term.
- Uses a glossary term for something the `Not` column excludes. `Source ID`
  names a matching engine, so a comment that calls it a venue is a finding even
  though the words are correct English.
- Coins a synonym for a term the glossary already defines.

Do not flag these:

- An ordinary English use the glossary leaves alone. The glossary says which
  ones in its notes. `sibling module` and `path` are both correct.
- A documented exception. `ARM64`, the `ARM` vendor, `Unix epoch`,
  `source of truth`, `snapshot stream`, `delta stream`, and `Trade Flags` bit 1
  all stay as written.
- A name an external party owns. A venue API field keeps the venue spelling.

## Finding shape

Write each finding in this shape, and name the replacement term:

  **Issue**
  What word the line uses, and what the glossary requires instead.

  **Context**
  Where the word appears, and what the glossary row or note says.

  **Proposed Fix**
  The replacement, in one sentence.

Write in Simplified Technical English. Use the active voice. Name the actor.
Keep one idea in one sentence. Do not use an em dash.

Name the glossary version you reviewed against in the review summary. Take it
from the Versioning section of `GLOSSARY.md`.
