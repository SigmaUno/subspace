---
id: reduce-memory-allocations
title: Reduce memory allocations
tags: [performance, memory]
variables: [target, command]
---
Reduce heap allocations in {{target}} without changing its output.

1. Write or find a benchmark for {{target}} and record a baseline with `{{command}}` using `-benchmem`: allocs/op, B/op and ns/op.
2. Profile allocations with the memory profiler and rank the top allocation sites by count and bytes.
3. Fix the top site first: preallocate with known capacity, reuse buffers with `sync.Pool` only where profiling shows gain, avoid string-to-byte conversions, and avoid escaping values to the heap through interfaces.
4. Re-run the benchmark after each change. Keep a change only if allocs/op or B/op drop and ns/op does not regress.
5. Run the full test suite with the race detector. Output must be byte-identical for the same input.

Acceptance:
- Before and after allocs/op, B/op and ns/op are reported for each kept change.
- Pooling is used only where it measurably helps.
- Behaviour is unchanged and the benchmark stays in the repo as a regression benchmark.

Report the table of changes with the numbers.
