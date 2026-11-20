---
id: deterministic-simulation-test
title: Write a deterministic simulation test
tags: [tests, reliability]
variables: [target, faults]
---
Build a deterministic simulation test for {{target}} that injects these faults: {{faults}}.

1. Define the invariants that must always hold for {{target}}, for example no lost writes, no double claims, and valid state transitions only.
2. Drive {{target}} with a seeded random generator and a simulated clock. The seed is a test parameter and is printed on failure.
3. Inject the faults from {{faults}} at seeded points: delays, dropped or duplicated calls, restarts, and clock jumps.
4. After each step check every invariant. On violation, fail with the seed, the step number and the trace of actions.
5. Run many seeds in CI-sized batches, and keep any failing seed as a fixed regression case.

Acceptance:
- Same seed gives the same trace, verified by a test.
- No real sleeps and no wall-clock reads inside the simulation.
- Failures print the seed and a replayable trace.

Report the invariants, the number of seeds run, and any seed that found a defect.
