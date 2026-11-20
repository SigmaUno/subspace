---
id: measure-redesign-io-mem-cpu
title: Measure, then redesign around IO, memory and CPU
tags: [performance, design]
variables: [target]
---
Analyse {{target}} from the hardware up.

1. Measure IO (syscalls, bytes, latency), memory (allocations, working set, cache misses) and CPU (hot paths, contention) separately.
2. Work out the theoretical floor for each resource given the workload, and compare it with what is measured.
3. Identify the resource that is the binding constraint.
4. Redesign the data flow so the binding resource is used as little as possible: batching, streaming, zero-copy, contiguous layouts, fewer locks.
5. Implement the redesign, re-measure, and report the gap to the floor.

Do not optimise anything that was not measured.
