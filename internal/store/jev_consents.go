package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/msgvault/internal/jev"
)

// JevFeatureConsent is one preserved grant for a feature's exact policy
// fingerprint and its optional revocation.
type JevFeatureConsent struct {
	ID                int64   `json:"id"`
	Feature           string  `json:"feature"`
	PolicyFingerprint string  `json:"policy_fingerprint"`
	GrantedBy         string  `json:"granted_by"`
	GrantedAt         string  `json:"granted_at"`
	RevokedBy         *string `json:"revoked_by,omitzero" nullable:"false"`
	RevokedAt         *string `json:"revoked_at,omitzero" nullable:"false"`
}

// JevFeatureConsentStatus reports authority for one feature's current
// fingerprint. Superseded is true when a grant exists for an older policy
// of the same feature, which explains a "consent required" state after a
// wording or destination change.
type JevFeatureConsentStatus struct {
	Feature     string             `json:"feature"`
	Fingerprint string             `json:"fingerprint"`
	Active      bool               `json:"active"`
	Consent     *JevFeatureConsent `json:"consent,omitzero" nullable:"false"`
	Superseded  bool               `json:"superseded"`
	LastRevoked *JevFeatureConsent `json:"last_revoked,omitzero" nullable:"false"`
}

const jevFeatureConsentColumns = `
	id, feature, policy_fingerprint, granted_by, granted_at, revoked_by, revoked_at`

// GrantJevFeatureConsent records consent for a feature's exact policy. Any
// active grant for a different fingerprint of the same feature is revoked in
// the same transaction so only the current policy has authority.
func (s *Store) GrantJevFeatureConsent(
	ctx context.Context, feature, fingerprint, actor string,
) (*JevFeatureConsent, bool, error) {
	actor, err := validateJevConsentInput(feature, fingerprint, actor)
	if err != nil {
		return nil, false, err
	}
	var consent *JevFeatureConsent
	created := false
	err = s.withTxContext(ctx, func(tx *loggedTx) error {
		existing, readErr := scanJevFeatureConsent(tx.QueryRowContext(ctx, `
			SELECT `+jevFeatureConsentColumns+`
			FROM jev_feature_consents
			WHERE feature = ? AND policy_fingerprint = ? AND revoked_at IS NULL
			ORDER BY id DESC LIMIT 1`, feature, fingerprint))
		if readErr == nil {
			consent = existing
			return nil
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return fmt.Errorf("read active jev consent: %w", readErr)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jev_feature_consents
			SET revoked_by = ?, revoked_at = CURRENT_TIMESTAMP
			WHERE feature = ? AND revoked_at IS NULL`, actor, feature); err != nil {
			return fmt.Errorf("supersede jev consent: %w", err)
		}
		inserted, insertErr := scanJevFeatureConsent(tx.QueryRowContext(ctx, `
			INSERT INTO jev_feature_consents (feature, policy_fingerprint, granted_by)
			VALUES (?, ?, ?)
			RETURNING `+jevFeatureConsentColumns, feature, fingerprint, actor))
		if insertErr != nil {
			return fmt.Errorf("grant jev consent: %w", insertErr)
		}
		consent = inserted
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return consent, created, nil
}

// RevokeJevFeatureConsent revokes every active grant for a feature.
func (s *Store) RevokeJevFeatureConsent(ctx context.Context, feature, actor string) (int64, error) {
	if !jev.ValidFeatureName(feature) {
		return 0, errors.New("jev feature name is invalid")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return 0, errors.New("jev consent actor is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jev_feature_consents
		SET revoked_by = ?, revoked_at = CURRENT_TIMESTAMP
		WHERE feature = ? AND revoked_at IS NULL`, actor, feature)
	if err != nil {
		return 0, fmt.Errorf("revoke jev consent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read revoked jev consent count: %w", err)
	}
	return changed, nil
}

// RevokeAllJevFeatureConsents revokes every active grant for every feature.
func (s *Store) RevokeAllJevFeatureConsents(ctx context.Context, actor string) (int64, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return 0, errors.New("jev consent actor is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE jev_feature_consents
		SET revoked_by = ?, revoked_at = CURRENT_TIMESTAMP
		WHERE revoked_at IS NULL`, actor)
	if err != nil {
		return 0, fmt.Errorf("revoke all jev consents: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read revoked jev consent count: %w", err)
	}
	return changed, nil
}

// HasActiveJevFeatureConsent is the gate's narrow check.
func (s *Store) HasActiveJevFeatureConsent(ctx context.Context, feature, fingerprint string) (bool, error) {
	if !jev.ValidFeatureName(feature) {
		return false, errors.New("jev feature name is invalid")
	}
	if !validLowerSHA256(fingerprint) {
		return false, errors.New("jev consent requires a lowercase SHA-256 fingerprint")
	}
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM jev_feature_consents
		WHERE feature = ? AND policy_fingerprint = ? AND revoked_at IS NULL)`,
		feature, fingerprint).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("check active jev consent: %w", err)
	}
	return active, nil
}

// GetJevFeatureConsentStatus reports authority for a feature's current
// fingerprint and what history explains it.
func (s *Store) GetJevFeatureConsentStatus(
	ctx context.Context, feature, fingerprint string,
) (*JevFeatureConsentStatus, error) {
	if !jev.ValidFeatureName(feature) {
		return nil, errors.New("jev feature name is invalid")
	}
	if !validLowerSHA256(fingerprint) {
		return nil, errors.New("jev consent requires a lowercase SHA-256 fingerprint")
	}
	status := &JevFeatureConsentStatus{Feature: feature, Fingerprint: fingerprint}
	active, err := scanJevFeatureConsent(s.db.QueryRowContext(ctx, `
		SELECT `+jevFeatureConsentColumns+`
		FROM jev_feature_consents
		WHERE feature = ? AND policy_fingerprint = ? AND revoked_at IS NULL
		ORDER BY id DESC LIMIT 1`, feature, fingerprint))
	if err == nil {
		status.Active = true
		status.Consent = active
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read active jev consent status: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM jev_feature_consents
		WHERE feature = ? AND policy_fingerprint <> ?)`, feature, fingerprint,
	).Scan(&status.Superseded); err != nil {
		return nil, fmt.Errorf("read superseded jev consent status: %w", err)
	}
	lastRevoked, err := scanJevFeatureConsent(s.db.QueryRowContext(ctx, `
		SELECT `+jevFeatureConsentColumns+`
		FROM jev_feature_consents
		WHERE feature = ? AND revoked_at IS NOT NULL
		ORDER BY revoked_at DESC, id DESC LIMIT 1`, feature))
	if err == nil {
		status.LastRevoked = lastRevoked
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read revoked jev consent status: %w", err)
	}
	return status, nil
}

func scanJevFeatureConsent(row scanner) (*JevFeatureConsent, error) {
	var (
		consent              JevFeatureConsent
		grantedAt, revokedAt nullableTimestamp
		revokedBy            sql.NullString
	)
	if err := row.Scan(&consent.ID, &consent.Feature, &consent.PolicyFingerprint,
		&consent.GrantedBy, &grantedAt, &revokedBy, &revokedAt); err != nil {
		return nil, err
	}
	if !grantedAt.Valid {
		return nil, errors.New("jev consent has invalid granted_at")
	}
	consent.GrantedAt = grantedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
	if revokedBy.Valid {
		value := revokedBy.String
		consent.RevokedBy = &value
	}
	if revokedAt.Valid {
		value := revokedAt.Time.UTC().Format("2006-01-02T15:04:05Z")
		consent.RevokedAt = &value
	}
	return &consent, nil
}

func validateJevConsentInput(feature, fingerprint, actor string) (string, error) {
	if !jev.ValidFeatureName(feature) {
		return "", errors.New("jev feature name is invalid")
	}
	if !validLowerSHA256(fingerprint) {
		return "", errors.New("jev consent requires a lowercase SHA-256 fingerprint")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return "", errors.New("jev consent actor is required")
	}
	return actor, nil
}
