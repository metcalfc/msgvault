package main

import (
	"context"
	"fmt"

	"go.kenn.io/msgvault/internal/store"
)

type reviewManifest struct {
	LinkCandidateID  int64 `json:"link_candidate_id"`
	MergeCandidateID int64 `json:"merge_candidate_id"`
	SurvivorID       int64 `json:"survivor_id"`
	AbsorbedID       int64 `json:"absorbed_id"`
}

// Seed review states through the same Store operations used by identity matching
// and profile promotion. The browser must perform every acceptance and merge.
func seedReviews(ctx context.Context, st *store.Store) (*reviewManifest, error) {
	out := &reviewManifest{}
	for _, pair := range []struct {
		names     [2]string
		addresses [2]string
		merge     bool
	}{
		{names: [2]string{"Avery Review", "Avery Alternate"}, addresses: [2]string{"avery@example.com", "avery@example.net"}},
		{names: [2]string{"Morgan Survivor", "Morgan Duplicate"}, addresses: [2]string{"morgan@example.com", "morgan@example.net"}, merge: true},
	} {
		var ids [2]int64
		for i := range ids {
			id, err := st.EnsureParticipant(pair.addresses[i], pair.names[i], "")
			if err != nil {
				return nil, err
			}
			ids[i] = id
			if pair.merge {
				person, _, err := st.CreatePersonFromParticipantContext(ctx, id)
				if err != nil {
					return nil, err
				}
				if i == 0 {
					out.SurvivorID = person.ID
				} else {
					out.AbsorbedID = person.ID
				}
			}
		}
		candidate, _, err := st.UpsertIdentityMatchCandidateContext(ctx, store.IdentityMatchCandidateInput{
			LeftKind: store.IdentityMatchParticipant, LeftID: ids[0], RightKind: store.IdentityMatchParticipant, RightID: ids[1],
			Basis: store.IdentityMatchDisplayName, State: store.IdentityMatchStateCandidate, Source: store.ProvenanceSystem,
		})
		if err != nil {
			return nil, fmt.Errorf("seed identity review: %w", err)
		}
		if pair.merge {
			out.MergeCandidateID = candidate.ID
		} else {
			out.LinkCandidateID = candidate.ID
		}
	}
	return out, nil
}
