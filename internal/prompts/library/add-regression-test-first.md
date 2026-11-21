---
id: add-regression-test-first
title: Add a regression test first
tags: [tests, correctness]
variables: [bug, target]
---
Fix this bug in {{target}}: {{bug}}

1. Reproduce the bug on the current code. Write it down as the exact input, the observed output and the expected output.
2. Add a regression test that fails for that input. Run it and confirm it fails for the reason in step 1, not for a setup error.
3. Make the smallest change in {{target}} that turns the test green. Do not refactor in the same change.
4. Check neighbouring inputs (empty, boundary, repeated, concurrent) and add a test only where a real defect was found.
5. Run the full suite with the race detector. Nothing else may change behaviour.

Acceptance:
- The new test fails on the old code and passes on the new code.
- The diff contains only the test and the fix for {{bug}}.
- No comments, no unrelated cleanup.

Report the reproducer, the failing output before the fix, and the final test result.
