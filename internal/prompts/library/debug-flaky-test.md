---
id: debug-flaky-test
title: Debug a flaky test
tags: [tests, reliability]
variables: [test_name, command]
---
Find the root cause of the flaky test {{test_name}}. Do not add retries, sleeps or skips.

1. Run `{{command}}` 200 times with `-count` and record the failure rate and the failing output.
2. Classify the cause: shared global state, wall-clock time, map iteration order, goroutine ordering, port or file collisions, or external I/O.
3. Build a deterministic reproducer: fix the seed or the clock, or force the bad ordering, so the failure happens on every run.
4. Fix the cause in the code under test or in the test's isolation. Never weaken the assertion.
5. Run the reproducer and `{{command}}` 500 times with the race detector. Zero failures are required.

Acceptance:
- Root cause named with file, line and the interleaving or input that triggers it.
- Failure rate before and after reported.
- The deterministic reproducer stays in the test suite.

Report the cause, the evidence and the failure rate before and after.
