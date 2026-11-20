---
id: parallelize-sequential-async
title: Parallelize sequential async work
tags: [concurrency, performance]
variables: [target]
---
Find sequential async work in {{target}} that has no data dependency and run it concurrently.

1. Trace the call graph and mark which steps depend on the results of others.
2. Run independent steps in parallel with bounded concurrency and cancellation on first error.
3. Preserve ordering of results and of externally visible side effects where callers rely on it.
4. Measure end-to-end latency before and after, and check for resource exhaustion under load.
5. Add a test with a simulated clock showing the steps overlap and that failure cancels the rest.

Report the speedup and the concurrency limit chosen.
