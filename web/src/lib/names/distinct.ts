/**
 * Tells repeated labels apart without an ID: every label that occurs more
 * than once gets its position among the repeats, e.g. "Avery (1 of 2)".
 * Order follows the input, so callers pass a stable order.
 */
export function distinctLabels(labels: readonly string[]): string[] {
  const totals = new Map<string, number>();
  for (const label of labels) totals.set(label, (totals.get(label) ?? 0) + 1);
  const seen = new Map<string, number>();
  return labels.map((label) => {
    const total = totals.get(label) ?? 1;
    if (total < 2) return label;
    const position = (seen.get(label) ?? 0) + 1;
    seen.set(label, position);
    return `${label} (${position} of ${total})`;
  });
}
