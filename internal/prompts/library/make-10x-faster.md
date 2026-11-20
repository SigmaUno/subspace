---
id: make-10x-faster
title: Make it 10x faster
tags: [performance]
variables: [target, command]
---
Make {{target}} at least 10x faster.

1. Establish a baseline: run `{{command}}` several times on a quiet machine and record median and p99.
2. Profile before changing anything. Find where the time goes and rank the costs.
3. Fix the largest cost first, then measure again. Keep a change only if the numbers improve.
4. Prefer removing work over doing it faster: fewer allocations, fewer copies, fewer round trips, better algorithms and data layout.
5. Keep behaviour identical. Existing tests must stay green, and add a regression benchmark.

Report the before and after numbers for each step.
