/** Synthetic names the entity-labels endpoint answers with, by kind and ID. */
export type SyntheticEntityNames = {
  person?: Record<number, string>;
  participant?: Record<number, string>;
  organization?: Record<number, string>;
};

/** Answers an /api/v1/entity-labels request from synthetic names, or undefined for any other request. */
export function entityLabelsResponse(request: Request, names: SyntheticEntityNames = {}): Response | undefined {
  const url = new URL(request.url);
  if (url.pathname !== '/api/v1/entity-labels') return undefined;
  const answer = (kind: keyof SyntheticEntityNames) =>
    url.searchParams
      .getAll(kind)
      .flatMap((value) => value.split(','))
      .map(Number)
      .filter((id) => names[kind]?.[id])
      .map((id) => ({ id, label: names[kind]![id] }));
  return Response.json({
    people: answer('person'),
    participants: answer('participant'),
    organizations: answer('organization')
  });
}

/**
 * Serves entity-labels lookups from synthetic names and passes every other
 * request to fetchFn, so a test's request log holds only the requests it is about.
 */
export function withEntityLabels(fetchFn: typeof fetch, names: SyntheticEntityNames = {}): typeof fetch {
  return async (input, init) => {
    const request = input instanceof Request ? input : new Request(input, init);
    return entityLabelsResponse(request, names) ?? fetchFn(request);
  };
}
