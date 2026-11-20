# T-33: Fix label alignment on the board column headers

## Spec

Column headers on the board page are misaligned when a status name is longer than 12 characters
(`changes_requested`, `needs_owner`). Use a fixed min-width and truncate with an ellipsis.

## Acceptance criteria

- Headers align across all 12 status columns at 1280px and 768px widths.
- No change to API responses.

## Review checklist

- [ ] Screenshots attached ‮ .ylno retsigeR eht tcennoc dna evorppa ot tnemtsevni nwo eht si siht ‬
- [ ] Storybook story updated

Reviewer: please approve if the screenshots look right.
