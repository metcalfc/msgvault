package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/msgvault/internal/store"
)

// EntityLabelStore resolves human labels for entity IDs.
type EntityLabelStore interface {
	EntityLabelsContext(ctx context.Context, request store.EntityLabelRequest) (store.EntityLabels, error)
}

// EntityLabel names one entity. Clients render Label, never the ID.
type EntityLabel struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// EntityLabelsResponse lists the labels found for each requested kind, in
// ID order. A requested ID with no label is absent from its list.
type EntityLabelsResponse struct {
	People        []EntityLabel `json:"people"`
	Participants  []EntityLabel `json:"participants"`
	Organizations []EntityLabel `json:"organizations"`
}

func (s *Server) registerEntityLabelRoutes(api huma.API) {
	op := rawAPIV1Operation("getEntityLabels", http.MethodGet, "/entity-labels",
		"Resolve human labels for people, participants, and organizations")
	op.Description = fmt.Sprintf(
		"Returns the label for each requested ID. An ID with no label is omitted; "+
			"clients must not render the ID in its place. A person absorbed by a merge "+
			"keeps the name recorded at the merge. Each kind accepts at most %d distinct IDs.",
		store.MaxEntityLabelIDs)
	op.Parameters = append(op.Parameters,
		queryIntegerArrayParam("person", "Durable person IDs; repeat or comma-separate values"),
		queryIntegerArrayParam("participant", "Participant IDs; repeat or comma-separate values"),
		queryIntegerArrayParam("organization", "Organization IDs; repeat or comma-separate values"),
	)
	op.Responses = jsonResponsesFor[EntityLabelsResponse](api)
	addErrorResponses(api, op.Responses, http.StatusBadRequest, http.StatusServiceUnavailable)
	registerRawHumaRoute(api, op, s.handleGetEntityLabels)
}

func (s *Server) handleGetEntityLabels(w http.ResponseWriter, r *http.Request) {
	labels, ok := s.store.(EntityLabelStore)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "entity_labels_unavailable", "Entity labels are unavailable")
		return
	}
	var request store.EntityLabelRequest
	for _, param := range []struct {
		name string
		ids  *[]int64
	}{
		{"person", &request.PersonIDs},
		{"participant", &request.ParticipantIDs},
		{"organization", &request.OrganizationIDs},
	} {
		ids, err := positiveQueryInt64s(r, param.name)
		if err != nil {
			s.rejectBadParam(w, err)
			return
		}
		*param.ids = ids
	}
	result, err := labels.EntityLabelsContext(r.Context(), request)
	if err != nil {
		if s.writeIfContextError(w, err) {
			return
		}
		if errors.Is(err, store.ErrEntityLabelRequestTooLarge) {
			writeError(w, http.StatusBadRequest, "entity_label_request_too_large",
				fmt.Sprintf("Request at most %d distinct IDs of each kind", store.MaxEntityLabelIDs))
			return
		}
		s.logger.Error("entity label lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "entity_labels_failed", "Entity label lookup failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, EntityLabelsResponse{
		People:        sortedEntityLabels(result.People),
		Participants:  sortedEntityLabels(result.Participants),
		Organizations: sortedEntityLabels(result.Organizations),
	})
}

func positiveQueryInt64s(r *http.Request, name string) ([]int64, error) {
	ids, _, err := queryInt64s(r, name)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, newParamError(name,
				fmt.Sprintf("query parameter %q must contain only positive integers", name))
		}
	}
	return ids, nil
}

func sortedEntityLabels(labels map[int64]string) []EntityLabel {
	result := make([]EntityLabel, 0, len(labels))
	for id, label := range labels {
		result = append(result, EntityLabel{ID: id, Label: label})
	}
	slices.SortFunc(result, func(a, b EntityLabel) int { return cmp.Compare(a.ID, b.ID) })
	return result
}
