---
id: split-large-function
title: Split a large function
tags: [design, maintainability]
variables: [function, file]
---
Split {{function}} in {{file}} into smaller functions without changing behaviour.

1. Read {{function}} and list its phases: validation, data gathering, decisions, side effects, and response or result building.
2. Write or confirm tests that pin the current behaviour, including every error path. Run them before the change.
3. Extract one phase at a time into a named function with explicit inputs and outputs. Avoid hidden shared state and package-level variables.
4. Keep each extracted function focused on one job; put limits and units in names where they apply.
5. Re-run the tests after every extraction. Do not change signatures of exported functions.

Acceptance:
- No behaviour change: the existing and new tests pass unchanged.
- {{function}} reads as a short sequence of named steps.
- No new dependencies, no comments, no renames outside {{file}}.

Report the before and after line counts and the list of extracted functions.
