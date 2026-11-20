---
id: reimplement-tigerstyle
title: Reimplement in TigerStyle
tags: [design, correctness]
variables: [target]
---
Reimplement {{target}} following TigerStyle: safety first, then performance, then developer experience.

1. Use simple, explicit control flow. No recursion, no hidden allocation after startup, and every loop and queue has a fixed upper bound.
2. State and check invariants with assertions: preconditions, postconditions and the relationships between values.
3. Keep functions short, name things precisely, and put limits and units in names.
4. Handle every error explicitly; do not ignore failures.
5. Test with deterministic simulation: seeded randomness, simulated time and injected faults.

Keep the public behaviour the same and show that the new implementation passes the same tests.
