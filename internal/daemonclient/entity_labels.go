package daemonclient

import (
	"context"
	"slices"

	"go.kenn.io/msgvault/internal/store"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

// EntityLabels resolves human labels for people, participants, and
// organizations through GET /api/v1/entity-labels. IDs are deduplicated and
// sent in batches of at most store.MaxEntityLabelIDs per kind. An ID the
// daemon cannot name is absent from the result. Any failure (including a
// daemon too old to serve the endpoint) returns the error with whatever
// batches already resolved, so callers can degrade to unnamed output.
func (c *Client) EntityLabels(
	ctx context.Context, request store.EntityLabelRequest,
) (store.EntityLabels, error) {
	labels := store.EntityLabels{
		People:        map[int64]string{},
		Participants:  map[int64]string{},
		Organizations: map[int64]string{},
	}
	people := uniquePositiveIDs(request.PersonIDs)
	participants := uniquePositiveIDs(request.ParticipantIDs)
	organizations := uniquePositiveIDs(request.OrganizationIDs)
	for len(people) > 0 || len(participants) > 0 || len(organizations) > 0 {
		var query generated.GetEntityLabelsQuery
		query.Person, people = takeEntityLabelBatch(people)
		query.Participant, participants = takeEntityLabelBatch(participants)
		query.Organization, organizations = takeEntityLabelBatch(organizations)
		resp, err := APIResponse(ctx, c, func(api *apiclient.Client) (*generated.GetEntityLabelsResp, error) {
			return api.GetEntityLabelsWithResponse(ctx, &generated.GetEntityLabelsRequestOptions{Query: &query})
		})
		if err != nil {
			return labels, err
		}
		if resp.JSON200 == nil {
			continue
		}
		addEntityLabels(labels.People, resp.JSON200.People)
		addEntityLabels(labels.Participants, resp.JSON200.Participants)
		addEntityLabels(labels.Organizations, resp.JSON200.Organizations)
	}
	return labels, nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func takeEntityLabelBatch(ids []int64) (batch, rest []int64) {
	n := min(len(ids), store.MaxEntityLabelIDs)
	if n == 0 {
		return nil, nil
	}
	return ids[:n], ids[n:]
}

func addEntityLabels(target map[int64]string, labels []generated.EntityLabel) {
	for _, label := range labels {
		if label.Label != "" {
			target[label.ID] = label.Label
		}
	}
}
