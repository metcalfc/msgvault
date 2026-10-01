const REVISION_PATTERN = /^sha256:[0-9a-f]{64}$/;

export function encodeFactTargetRef(target: {
  kind?: string;
  key?: string;
  revision?: string;
}): string | undefined {
  if (target.kind !== 'attribute' && target.kind !== 'employment') return undefined;
  if (!target.key || target.key.trim() !== target.key) return undefined;
  if (!target.revision || !REVISION_PATTERN.test(target.revision)) return undefined;
  return `${target.kind}:${target.key}:${target.revision}`;
}
