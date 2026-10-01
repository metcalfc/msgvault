package vector

import "errors"

// Sentinel errors used across the vector package. Callers should use
// errors.Is to check for these.
var (
	// ErrPermanent4xx marks a non-retryable HTTP 4xx response from an
	// embeddings provider. Rate limits and server errors remain transient.
	ErrPermanent4xx = errors.New("embed: non-retryable 4xx response")
	// ErrInvalidProviderShape identifies a complete provider response whose
	// document, chunk, or index layout cannot match the request.
	ErrInvalidProviderShape = errors.New("invalid embedding provider response shape")
	// ErrInvalidProviderVector identifies a provider vector that cannot belong
	// to the configured vector space.
	ErrInvalidProviderVector = errors.New("invalid embedding provider vector")

	// ErrNotEnabled is returned when vector search is requested but
	// [vector] is not configured.
	ErrNotEnabled = errors.New("vector search not enabled")

	// ErrIndexStale is returned when the configured embedding settings
	// differ from the active generation's fingerprint. Settings include the
	// model, preprocessing policy, and embedding scope.
	ErrIndexStale = errors.New("index stale: configured embedding settings do not match active generation")

	// ErrIndexBuilding is returned when no active generation exists and
	// a first-ever rebuild is in progress.
	ErrIndexBuilding = errors.New("index building: no active generation yet")

	// ErrNoActiveGeneration is returned internally when no generation is
	// in state='active'. Usually surfaced as ErrNotEnabled or ErrIndexBuilding.
	ErrNoActiveGeneration = errors.New("no active generation")

	// ErrDimensionMismatch is returned when a query or chunk vector has
	// a dimension different from the index.
	ErrDimensionMismatch = errors.New("dimension mismatch")

	// ErrPaginationUnsupported is returned for page>1 in vector/hybrid modes.
	ErrPaginationUnsupported = errors.New("pagination not supported for this mode")

	// ErrUnknownGeneration is returned when a caller references a
	// generation ID that does not exist in index_generations.
	ErrUnknownGeneration = errors.New("unknown generation")

	// ErrGenerationRetired is returned by Upsert when the target generation
	// has retired. Retired generations are immutable; callers such as stale
	// embed workers whose claims were reclaimed should drop the batch.
	ErrGenerationRetired = errors.New("generation is retired")

	// ErrBuildingInProgress is returned when CreateGeneration is called
	// while another generation is already being built with a different
	// fingerprint, so the caller can surface an actionable message
	// instead of a raw unique-index violation.
	ErrBuildingInProgress = errors.New("a rebuild with a different fingerprint is already in progress")

	// ErrScopeUnresolvable marks a DETERMINISTIC failure to re-resolve the
	// durable embedding scope: a configured account was removed, became
	// ambiguous, or otherwise no longer names a source set. Unlike a
	// transient resolution failure (a busy database), this cannot heal on
	// retry — the daemon's drift detection latches vector search stale so
	// queries stop serving an index whose scope no longer matches the
	// configuration.
	ErrScopeUnresolvable = errors.New("embedding scope unresolvable")

	// ErrRefuseActivateEmptyScope is returned by ActivateGeneration when
	// force is false and the source-scoped build matches no live messages.
	// Activating would replace the serving index with an empty generation,
	// so the backend refuses every non-forced activation path. A common
	// cause is a scoped account that exists but has never been synced.
	ErrRefuseActivateEmptyScope = errors.New("refusing to activate: the source-scoped build scope matches no live messages")

	// ErrRefuseRetireActive is returned by RetireGeneration when force is
	// false and the target generation is active. The CLI requires
	// --force-active to stop serving it. The guard runs atomically inside
	// the retire transaction, so a concurrent activation cannot cause an
	// unforced retirement of the now-serving generation.
	ErrRefuseRetireActive = errors.New("refusing to retire the active (serving) generation without force")

	// ErrEmbeddingTimeout is returned by the hybrid engine when the
	// embedding endpoint did not respond before the request context
	// was cancelled (typically because the HTTP server's per-request
	// timeout elapsed first). Callers should map this to a 503-style
	// "transient backend slow" response so clients can retry instead
	// of treating it as a permanent failure.
	ErrEmbeddingTimeout = errors.New("embedding request timed out")

	// ErrIndexScopeMismatch is returned when a scoped embedding index
	// is used without an equivalent structured filter. For example, an
	// index built only for message_type=sms must not answer an unscoped
	// vector query over email + SMS.
	ErrIndexScopeMismatch = errors.New("index scope mismatch")

	// ErrCoverageBatchTooLarge rejects an analytical coverage intersection
	// that would exceed the fixed backend parameter bound.
	ErrCoverageBatchTooLarge = errors.New("filtered coverage batch too large")

	// ErrGenerationNotConverged is returned when contextual source or cursor
	// state changed after a caller's convergence read but before activation.
	ErrGenerationNotConverged = errors.New("contextual generation no longer converged")

	// ErrDocumentFenceChanged is returned when a fence-only publication finds
	// that another publication changed the scope after source assembly.
	ErrDocumentFenceChanged = errors.New("document scope changed before sequence fence")
)
