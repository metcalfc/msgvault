package documentindex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"go.kenn.io/docbank/document"
	"go.kenn.io/docbank/document/csvpdf"
	"go.kenn.io/msgvault/internal/documentindex/provider"
	"go.kenn.io/msgvault/internal/store"
)

const originalDocumentInputKey = "original"

const documentFailureCleanupTimeout = 5 * time.Second

var (
	errDocumentPreparation  = errors.New("document extraction preparation failed")
	errDocumentLeaseRenewal = errors.New("document extraction lease renewal failed")
	errDocumentPublication  = errors.New("document extraction publication failed")
)

type DocumentExtractionCatalog interface {
	ClaimDocumentExtraction(ctx context.Context, input store.DocumentExtractionClaimInput) (store.DocumentExtractionClaim, error)
	RenewDocumentExtractionClaim(ctx context.Context, claim store.DocumentExtractionClaim, leaseUntil time.Time) error
	PublishDocumentExtraction(ctx context.Context, publication store.DocumentExtractionPublication) error
	FailDocumentExtraction(ctx context.Context, failure store.DocumentExtractionFailure) error
}

type DocumentAttachmentOpener interface {
	OpenStream(ctx context.Context, hash string) (io.ReadCloser, int64, error)
}

// WorkerConfig binds one extraction pass to an exact profile, its policy, and
// the capability evidence that authorized the resolved input routes. Private
// staging belongs to the provider.Processor, not the worker.
type WorkerConfig struct {
	ProfileID        string
	RebuildID        string
	LeaseOwner       string
	LeaseDuration    time.Duration
	RetryDelay       time.Duration
	MessageTypes     []string
	ReplaceCurrent   bool
	Policy           provider.Policy
	CapabilityPolicy provider.Manifest
	InputPolicy      ResolvedInputPolicy
}

type closeOnceReadCloser struct {
	io.ReadCloser

	once sync.Once
	err  error
}

func (r *closeOnceReadCloser) Close() error {
	r.once.Do(func() {
		r.err = r.ReadCloser.Close()
	})
	return r.err
}

// Worker claims, converts, sends, normalizes, and publishes one candidate at
// a time through a provider-neutral Processor.
type Worker struct {
	catalog      DocumentExtractionCatalog
	opener       DocumentAttachmentOpener
	processor    provider.Processor
	config       WorkerConfig
	messageTypes map[string]struct{}
}

type DocumentExtractionResult struct {
	ExtractionID      string
	CanonicalBlobHash string
	FailureReasonCode string
	Units             int
	Chunks            int
	Truncated         bool
	CleanupError      error
}

// NewWorker binds runtime upload authority to the exact complete probe
// manifest. A documented format is not eligible unless that manifest recorded
// a passing authenticated probe for the pinned processor target.
func NewWorker(
	catalog DocumentExtractionCatalog,
	opener DocumentAttachmentOpener,
	processor provider.Processor,
	config WorkerConfig,
) (*Worker, error) {
	if catalog == nil || opener == nil || processor == nil {
		return nil, errors.New("document worker requires catalog, attachment opener, and processor")
	}
	if config.ProfileID == "" || config.LeaseOwner == "" ||
		config.LeaseDuration <= 0 || config.LeaseDuration > time.Hour ||
		config.RetryDelay <= 0 || config.RetryDelay > 7*24*time.Hour {
		return nil, errors.New("document worker configuration is incomplete")
	}
	if config.ReplaceCurrent != (config.RebuildID != "") {
		return nil, errors.New("document worker replacement requires an exact rebuild")
	}
	if config.Policy == nil || config.CapabilityPolicy == nil {
		return nil, errors.New("document worker requires a policy and its capability manifest")
	}
	fingerprint, err := config.Policy.Fingerprint(config.CapabilityPolicy)
	if err != nil {
		return nil, fmt.Errorf("validate document capability policy: %w", err)
	}
	// Fail closed: the processor must have been built from the same policy and
	// capability evidence that resolved the input routes, or a route could
	// send bytes under authority the processor never validated.
	if processor.PolicyFingerprint() != fingerprint {
		return nil, errors.New("document processor was built from a different policy or capability manifest")
	}
	if config.InputPolicy.Routes == nil {
		return nil, errors.New("document worker requires a resolved input policy")
	}
	if len(config.InputPolicy.Routes) == 0 {
		return nil, errors.New("no format has authorized upload authority; run the authenticated capability probe and supply its manifest")
	}
	// The worker owns its copy of the routes so a caller mutating the
	// resolved policy afterwards cannot widen what this pass accepts.
	config.InputPolicy.Routes = maps.Clone(config.InputPolicy.Routes)
	messageTypes := make(map[string]struct{}, len(config.MessageTypes))
	for _, messageType := range config.MessageTypes {
		if messageType == "" {
			return nil, errors.New("document worker message scope contains an empty type")
		}
		messageTypes[messageType] = struct{}{}
	}
	config.MessageTypes = slices.Clone(config.MessageTypes)
	return &Worker{
		catalog: catalog, opener: opener, processor: processor, config: config,
		messageTypes: messageTypes,
	}, nil
}

// ProcessCandidate hands one verified source to the provider, normalizes the
// returned Markdown entirely in memory, and atomically publishes only
// canonical local derivatives. The raw provider response and Markdown are
// never persisted.
func (w *Worker) ProcessCandidate(
	ctx context.Context,
	candidate store.DocumentExtractionCandidate,
) (result DocumentExtractionResult, runErr error) {
	result.CanonicalBlobHash = candidate.CanonicalBlobHash
	defer func() {
		if runErr != nil {
			_, result.FailureReasonCode = classifyDocumentExtractionFailure(runErr)
		}
	}()
	route, allowed := w.config.InputPolicy.Routes[candidate.MIMEType]
	if !allowed {
		return result, fmt.Errorf("document media type %q lacks passing capability authority", candidate.MIMEType)
	}
	if len(w.messageTypes) > 0 {
		if _, allowed = w.messageTypes[candidate.MessageType]; !allowed {
			return result, fmt.Errorf("document message type %q is outside configured scope", candidate.MessageType)
		}
	}
	extractionID, err := newDocumentExtractionID()
	if err != nil {
		return result, err
	}
	claim, err := w.catalog.ClaimDocumentExtraction(ctx, store.DocumentExtractionClaimInput{
		ExtractionID: extractionID, ProfileID: w.config.ProfileID,
		RebuildID:         w.config.RebuildID,
		CanonicalBlobHash: candidate.CanonicalBlobHash, ExtractionInputKey: originalDocumentInputKey,
		OccurrenceAttachmentID: candidate.AttachmentID,
		OccurrenceMIMEType:     candidate.MIMEType,
		OccurrenceMessageType:  candidate.MessageType,
		LeaseOwner:             w.config.LeaseOwner, LeaseUntil: time.Now().UTC().Add(w.config.LeaseDuration),
		LocalBytes: candidate.Size, SourceSequence: candidate.SourceSequence,
		RequireNoHead: !w.config.ReplaceCurrent,
	})
	if err != nil {
		return result, err
	}
	workCtx, cancelWork, cancelRenewal, renewalDone, renewalErr := w.keepClaimAlive(ctx, claim)
	defer func() {
		cancelWork()
		cancelRenewal()
		<-renewalDone
	}()
	// A conversion receipt exists only after local CSV conversion succeeds.
	// Every later failure records it so the generated upload stays auditable.
	var conversion *store.DocumentExtractionConversion
	failPreparation := func(cause error) error {
		preparationErr := fmt.Errorf("%w: %w", errDocumentPreparation, cause)
		if renewErr := readRenewalError(renewalErr); renewErr != nil {
			preparationErr = errors.Join(preparationErr, renewErr)
		}
		return errors.Join(
			preparationErr,
			w.recordFailureAfterError(ctx, claim, preparationErr, provider.RequestMetrics{}, conversion),
		)
	}
	if candidate.Size <= 0 || candidate.Size > w.config.Policy.Values().MaxDocumentBytes {
		return result, failPreparation(errors.New("document candidate size is outside configured bounds"))
	}
	stream, authoritativeSize, err := w.opener.OpenStream(workCtx, candidate.CanonicalBlobHash)
	if err != nil {
		return result, failPreparation(fmt.Errorf("open document attachment: %w", err))
	}
	if authoritativeSize != candidate.Size {
		closeErr := stream.Close()
		return result, failPreparation(errors.Join(
			errors.New("document attachment size no longer matches reconciled metadata"), closeErr,
		))
	}
	var source provider.Source
	if route.Conversion != nil {
		csvContent := &closeOnceReadCloser{ReadCloser: stream}
		csvSource, sourceErr := provider.NewSource(csvContent, candidate.MIMEType, authoritativeSize, candidate.CanonicalBlobHash)
		if sourceErr != nil {
			return result, failPreparation(errors.Join(sourceErr, csvContent.Close()))
		}
		// csvpdf.Convert closes its source, while the wrapper keeps the underlying stream single-close.
		converted, convertErr := csvpdf.Convert(workCtx, csvSource, *route.Conversion)
		closeErr := csvContent.Close()
		if convertErr != nil {
			return result, failPreparation(errors.Join(convertErr, closeErr))
		}
		if closeErr != nil {
			return result, failPreparation(closeErr)
		}
		receipt := converted.Receipt()
		conversion = &store.DocumentExtractionConversion{
			SourceSHA256: receipt.SourceSHA256, SourceBytes: receipt.SourceBytes,
			ProviderMediaType: route.Format.MediaType, PDFSHA256: receipt.PDFSHA256,
			PDFBytes: receipt.PDFBytes, Pages: receipt.Pages,
			PolicyFingerprint: receipt.PolicyFingerprint, ConverterVersion: receipt.ConverterVersion,
			Spans: make([]store.DocumentExtractionConversionSpan, len(receipt.Spans)),
		}
		for index, span := range receipt.Spans {
			conversion.Spans[index] = store.DocumentExtractionConversionSpan{Page: span.Page, Record: span.Record, Cell: span.Cell}
		}
		generated, sourceErr := converted.Source()
		if sourceErr != nil {
			return result, failPreparation(sourceErr)
		}
		source, sourceErr = provider.NewSource(generated.Content, route.Format.MediaType, receipt.PDFBytes, receipt.PDFSHA256)
		if sourceErr != nil {
			return result, failPreparation(errors.Join(sourceErr, generated.Content.Close()))
		}
	} else {
		var sourceErr error
		source, sourceErr = provider.NewSource(stream, candidate.MIMEType, authoritativeSize, candidate.CanonicalBlobHash)
		if sourceErr != nil {
			return result, failPreparation(errors.Join(sourceErr, stream.Close()))
		}
	}

	// The processor owns source.Content from here and closes it on every path.
	providerStarted := time.Now()
	providerResult, err := w.processor.Process(workCtx, source)
	providerMetrics := providerResult.Metrics
	if err != nil {
		providerMetrics = provider.MetricsFromError(err)
		if renewErr := readRenewalError(renewalErr); renewErr != nil {
			err = errors.Join(err, renewErr)
		}
		err = errors.Join(err, w.recordFailureAfterError(ctx, claim, err, providerMetrics, conversion))
		return result, err
	}
	published := false
	defer func() {
		if published {
			result.CleanupError = providerResult.CleanupError
			return
		}
		runErr = errors.Join(runErr, providerResult.CleanupError)
	}()
	if renewErr := readRenewalError(renewalErr); renewErr != nil {
		err = errors.Join(renewErr, w.recordFailureAfterError(ctx, claim, renewErr, providerMetrics, conversion))
		return result, err
	}
	if providerMetrics.Requests == 0 {
		providerMetrics.Requests = 1
	}
	if providerMetrics.Latency <= 0 {
		providerMetrics.Latency = time.Since(providerStarted)
	}
	providerResult.Metrics = providerMetrics
	normalized, err := document.NormalizeDocument(providerResult.Document, w.config.Policy.NormalizePolicy())
	if err != nil {
		err = errors.Join(err, w.recordFailureAfterError(ctx, claim, err, providerMetrics, conversion))
		return result, err
	}
	publication, err := publicationFromNormalized(claim, providerResult, normalized)
	if err != nil {
		err = errors.Join(err, w.recordFailureAfterError(ctx, claim, err, providerMetrics, conversion))
		return result, err
	}
	publication.SourceBytes = claim.LocalBytes
	publication.Conversion = conversion
	cancelRenewal()
	<-renewalDone
	if renewErr := readRenewalError(renewalErr); renewErr != nil {
		err = errors.Join(renewErr, w.recordFailureAfterError(ctx, claim, renewErr, providerMetrics, conversion))
		return result, err
	}
	renewCtx, cancelRenew := context.WithTimeout(workCtx, documentFailureCleanupTimeout)
	err = w.catalog.RenewDocumentExtractionClaim(
		renewCtx, claim, time.Now().UTC().Add(w.config.LeaseDuration),
	)
	cancelRenew()
	if err != nil {
		err = fmt.Errorf("%w: %w", errDocumentLeaseRenewal, err)
		err = errors.Join(err, w.recordFailureAfterError(ctx, claim, err, providerMetrics, conversion))
		return result, err
	}
	if err := w.catalog.PublishDocumentExtraction(workCtx, publication); err != nil {
		err = fmt.Errorf("%w: %w", errDocumentPublication, err)
		err = errors.Join(err, w.recordFailureAfterError(ctx, claim, err, providerMetrics, conversion))
		return result, err
	}
	result = DocumentExtractionResult{
		ExtractionID: extractionID, CanonicalBlobHash: candidate.CanonicalBlobHash,
		Units: len(normalized.Units), Chunks: len(normalized.Chunks), Truncated: normalized.Truncated,
	}
	published = true
	return result, nil
}

func (w *Worker) keepClaimAlive(
	ctx context.Context,
	claim store.DocumentExtractionClaim,
) (context.Context, context.CancelFunc, context.CancelFunc, <-chan struct{}, <-chan error) {
	workCtx, cancelWork := context.WithCancel(ctx)
	renewalCtx, cancelRenewal := context.WithCancel(workCtx)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	interval := min(max(w.config.LeaseDuration/3, time.Millisecond), time.Minute)
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				return
			case <-ticker.C:
				renewCtx, cancelRenew := context.WithTimeout(
					context.WithoutCancel(workCtx), documentFailureCleanupTimeout,
				)
				err := w.catalog.RenewDocumentExtractionClaim(
					renewCtx, claim, time.Now().UTC().Add(w.config.LeaseDuration),
				)
				cancelRenew()
				if err != nil {
					errCh <- fmt.Errorf("%w: %w", errDocumentLeaseRenewal, err)
					cancelWork()
					return
				}
			}
		}
	}()
	return workCtx, cancelWork, cancelRenewal, done, errCh
}

func readRenewalError(errCh <-chan error) error {
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (w *Worker) recordFailureAfterError(
	ctx context.Context,
	claim store.DocumentExtractionClaim,
	cause error,
	metrics provider.RequestMetrics,
	conversion *store.DocumentExtractionConversion,
) error {
	failureCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		failureCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), documentFailureCleanupTimeout)
	}
	defer cancel()
	return w.recordFailure(failureCtx, claim, cause, metrics, conversion)
}

func (w *Worker) recordFailure(
	ctx context.Context,
	claim store.DocumentExtractionClaim,
	cause error,
	metrics provider.RequestMetrics,
	conversion *store.DocumentExtractionConversion,
) error {
	terminal, reason := classifyDocumentExtractionFailure(cause)
	failure := store.DocumentExtractionFailure{
		Claim: claim, ReasonCode: reason, Terminal: terminal,
		RequestCount: metrics.Requests, RetryCount: metrics.Retries,
		ProviderLatencyMS: requestLatencyMillis(metrics.Latency), Conversion: conversion,
	}
	if !terminal {
		failure.RetryAt = time.Now().UTC().Add(w.config.RetryDelay)
	}
	return w.catalog.FailDocumentExtraction(ctx, failure)
}

// classifyDocumentExtractionFailure maps a failure to its terminal flag and
// stable reason code. Worker-owned sentinels and interruptions take
// precedence; the provider's neutral error kind decides the rest.
func classifyDocumentExtractionFailure(err error) (bool, string) {
	switch {
	case errors.Is(err, errDocumentLeaseRenewal):
		return false, "lease_renewal_failed"
	case errors.Is(err, errDocumentPublication):
		return false, "publication_failed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false, "provider_interrupted"
	}
	switch provider.ErrorKindOf(err) {
	case provider.ErrorCapacity:
		return false, "spool_capacity_unavailable"
	case provider.ErrorTransient:
		return false, "provider_transient"
	case provider.ErrorInvalidInput:
		return true, "invalid_local_source"
	case provider.ErrorRejected:
		return true, "provider_rejected"
	case provider.ErrorResponseTooLarge:
		return true, "response_too_large"
	case provider.ErrorCapabilityChanged:
		return true, "provider_capability_changed"
	case provider.ErrorMalformedOutput:
		return true, "invalid_provider_output"
	}
	if errors.Is(err, errDocumentPreparation) {
		return true, "invalid_local_source"
	}
	return true, "invalid_provider_output"
}

func publicationFromNormalized(
	claim store.DocumentExtractionClaim,
	providerResult provider.Result,
	normalized document.NormalizedDocument,
) (store.DocumentExtractionPublication, error) {
	if providerResult.UnitsProcessed <= 0 || len(normalized.Chunks) == 0 {
		return store.DocumentExtractionPublication{}, errors.New("document extraction produced no publishable evidence")
	}
	publication := store.DocumentExtractionPublication{
		ExtractionID: claim.ExtractionID, ProfileID: claim.ProfileID,
		CanonicalBlobHash: claim.CanonicalBlobHash, ExtractionInputKey: claim.ExtractionInputKey,
		OccurrenceAttachmentID: claim.OccurrenceAttachmentID,
		OccurrenceMIMEType:     claim.OccurrenceMIMEType,
		OccurrenceMessageType:  claim.OccurrenceMessageType,
		LeaseOwner:             claim.LeaseOwner, LeaseFence: claim.LeaseFence,
		ReturnedModel: providerResult.ReturnedModel, ProviderBytes: providerResult.ProviderBytes,
		UnitsProcessed: providerResult.UnitsProcessed, ManifestChecksum: normalized.Checksum,
		NormalizationVersion: normalized.PolicyVersion, DocumentFamily: normalized.Family,
		UnitKind: normalized.UnitKind, NormalizedTruncated: normalized.Truncated,
		RequestCount: providerResult.Metrics.Requests, RetryCount: providerResult.Metrics.Retries,
		ProviderLatencyMS: requestLatencyMillis(providerResult.Metrics.Latency),
		Units:             make([]store.DocumentPublishedUnit, len(normalized.Units)),
		Chunks:            make([]store.DocumentPublishedChunk, len(normalized.Chunks)),
	}
	for i, unit := range normalized.Units {
		publication.Units[i] = store.DocumentPublishedUnit{
			Index: unit.Index, Kind: unit.Kind, Text: unit.Text, Header: unit.Header, Footer: unit.Footer,
			Width: unit.Dimensions.Width, Height: unit.Dimensions.Height, DPI: unit.Dimensions.DPI,
			Checksum: unit.Checksum, CharCount: unit.CharCount, Truncated: unit.Truncated,
			HeadingMarks: unit.HeadingMarks,
		}
	}
	for i, chunk := range normalized.Chunks {
		published := store.DocumentPublishedChunk{
			Key: chunk.Key, Ordinal: chunk.Ordinal, Text: chunk.Text, HeadingPath: chunk.HeadingPath,
			FirstUnitIndex: chunk.Spans[0].UnitIndex, LastUnitIndex: chunk.Spans[len(chunk.Spans)-1].UnitIndex,
			Checksum: chunk.Checksum, CharCount: chunk.CharCount, Truncated: chunk.Truncated,
			Spans: make([]store.DocumentPublishedSpan, len(chunk.Spans)),
		}
		for spanIndex, span := range chunk.Spans {
			published.Spans[spanIndex] = store.DocumentPublishedSpan{
				UnitIndex: span.UnitIndex, CharStart: span.CharStart, CharEnd: span.CharEnd,
			}
		}
		publication.Chunks[i] = published
	}
	return publication, nil
}

func requestLatencyMillis(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	milliseconds := latency.Milliseconds()
	if milliseconds == 0 {
		return 1
	}
	return milliseconds
}

func newDocumentExtractionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate document extraction ID: %w", err)
	}
	return "docex_" + hex.EncodeToString(bytes[:]), nil
}
