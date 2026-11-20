---
id: dependency-upgrade-safely
title: Upgrade a dependency safely
tags: [dependencies, maintenance]
variables: [dependency, target_version]
---
Upgrade {{dependency}} to {{target_version}} without changing behaviour.

1. Read the changelog and release notes between the current and target versions. List breaking changes and deprecations that touch our code.
2. Record the baseline: run the full test suite, the build, and the linter on the current version.
3. Upgrade {{dependency}} alone in its own commit. Update only the call sites the breaking changes require.
4. Run build, vet, lint, and the race-enabled tests. Compare results with the baseline; any new failure blocks the upgrade.
5. Check the lock file diff for unexpected transitive changes and report them.

Acceptance:
- Build and all tests pass, with the same test count as the baseline.
- Diff contains the dependency bump and only the required call-site changes.
- Every breaking change is listed with how it was handled, or marked not applicable.

Report the changelog summary, the baseline and final results, and any transitive changes.
