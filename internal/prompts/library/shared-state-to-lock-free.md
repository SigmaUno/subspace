---
id: shared-state-to-lock-free
title: Move shared state to lock-free
tags: [concurrency, performance]
variables: [target]
---
Reduce shared mutable state in {{target}}.

1. Inventory every piece of shared state and who reads and writes it.
2. Prefer ownership: give each piece to one goroutine or thread and communicate by messages.
3. Where sharing is unavoidable, use immutable snapshots, atomics, or sharded state before reaching for a global lock.
4. Prove the change with a contention benchmark and the race detector, before and after.
5. Document the memory ordering assumptions in the code only where they are not obvious.

Do not trade correctness for speed: if a lock-free version cannot be shown correct, keep the lock.
