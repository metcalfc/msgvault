package jev

import "fmt"

// BatchRequests turns items into as few requests as fit under the client's
// request cap. build receives a contiguous chunk and its offset into items and
// returns the request for that chunk. A chunk whose encoded request exceeds
// the cap is halved until every request fits; a single item that cannot fit
// fails with ErrRequestBounds.
func BatchRequests[T any](client *Client, items []T, build func(chunk []T, offset int) Request) ([]Request, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: client is required", ErrRequestBounds)
	}
	if len(items) == 0 {
		return nil, nil
	}
	return batchRequests(client, items, 0, build)
}

func batchRequests[T any](client *Client, items []T, offset int, build func(chunk []T, offset int) Request) ([]Request, error) {
	request := build(items, offset)
	if _, err := client.Encode(request); err == nil {
		return []Request{request}, nil
	} else if len(items) == 1 {
		return nil, err
	}
	half := len(items) / 2
	left, err := batchRequests(client, items[:half], offset, build)
	if err != nil {
		return nil, err
	}
	right, err := batchRequests(client, items[half:], offset+half, build)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}
