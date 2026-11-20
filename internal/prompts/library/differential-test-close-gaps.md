---
id: differential-test-close-gaps
title: Differential-test and close the gaps
tags: [testing]
variables: [target, reference]
---
Differential-test {{target}} against {{reference}}.

1. Define the observable behaviour that both must agree on.
2. Generate inputs from a seeded generator, run both implementations, and compare outputs, errors and side effects.
3. Shrink every disagreement to a minimal reproducer and record the seed.
4. Decide for each gap whether {{target}} or {{reference}} is wrong, fix the right one, and keep the reproducer as a regression test.
5. Repeat with more seeds and wider inputs until no gaps are found over a large run.

Report the seeds, the number of cases, and each gap that was closed.
