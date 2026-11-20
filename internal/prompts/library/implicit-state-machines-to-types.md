---
id: implicit-state-machines-to-types
title: Turn implicit state machines into types
tags: [design, correctness]
variables: [target]
---
Find the implicit state machines in {{target}}: booleans, nullable fields, status strings and flag combinations that only make sense in certain orders.

1. List the states and the legal transitions for each one.
2. Model them as types so illegal states cannot be represented: one type per state, transitions as functions that consume one state and return the next.
3. Remove the runtime checks and defensive branches that the types now make unnecessary.
4. Keep external behaviour and wire formats unchanged.
5. Add a test that walks every legal transition and one that shows the illegal ones are rejected at compile time or at the single entry point.

Report the machines found and how each is now enforced.
