package readsrv

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/shakestzd/wipnote/core/daemon"
)

// Reader returns a daemon.Reader backed by cache. Wire it into
// daemon.ListenerConfig.Reader; the resulting daemon answers work-item reads
// from canonical state so hooks no longer have to reach the derived index for
// them.
//
// The op set is CLOSED and matches the queries hooks actually make. It is not a
// general query protocol, and it should not become one: a general surface over
// canonical state would be a second index, which is the thing this track exists
// to stop having two of.
func Reader(cache *Cache) daemon.Reader {
	return func(req daemon.ReadRequest) (json.RawMessage, *daemon.CacheStats, error) {
		switch req.ReadOp {
		case daemon.ReadOpWorkItemGet:
			return readWorkItemGet(cache, req)
		case daemon.ReadOpWorkItemList:
			return readWorkItemList(cache, req)
		default:
			return nil, nil, daemon.ErrUnknownReadOp
		}
	}
}

func readWorkItemGet(cache *Cache, req daemon.ReadRequest) (json.RawMessage, *daemon.CacheStats, error) {
	var args daemon.WorkItemGetArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, nil, fmt.Errorf("decode args: %w", err)
		}
	}
	if args.ID == "" {
		return nil, nil, fmt.Errorf("id is required")
	}

	item, found, hit := cache.Get(args.ID)
	stats := &daemon.CacheStats{}
	if found {
		if hit {
			stats.Hits = 1
		} else {
			stats.Misses = 1
		}
	}
	body, err := json.Marshal(daemon.WorkItemGetResult{Found: found, Item: item})
	if err != nil {
		return nil, nil, fmt.Errorf("encode result: %w", err)
	}
	return body, stats, nil
}

func readWorkItemList(cache *Cache, req daemon.ReadRequest) (json.RawMessage, *daemon.CacheStats, error) {
	var args daemon.WorkItemListArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, nil, fmt.Errorf("decode args: %w", err)
		}
	}

	colls, err := collectionsForArgs(args)
	if err != nil {
		return nil, nil, err
	}
	scan := cache.scanCollections(colls)

	items := make([]daemon.WorkItem, 0, len(scan.items))
	for _, it := range scan.items {
		if args.TrackID != "" && it.TrackID != args.TrackID {
			continue
		}
		if len(args.Statuses) > 0 && !slices.Contains(args.Statuses, it.Status) {
			continue
		}
		if len(args.Types) > 0 && !slices.Contains(args.Types, it.Type) {
			continue
		}
		items = append(items, it)
		if args.Limit > 0 && len(items) >= args.Limit {
			break
		}
	}

	body, err := json.Marshal(daemon.WorkItemListResult{Items: items})
	if err != nil {
		return nil, nil, fmt.Errorf("encode result: %w", err)
	}
	return body, &daemon.CacheStats{Hits: scan.hits, Misses: scan.misses}, nil
}

// collectionsForArgs narrows the directories a list scan touches to those the
// type filter can possibly match. Narrowing here rather than after the scan is
// what keeps a track-scoped query from stat-ing every collection in the repo.
func collectionsForArgs(args daemon.WorkItemListArgs) ([]string, error) {
	if len(args.Types) == 0 {
		return allCollections, nil
	}
	var out []string
	for _, t := range args.Types {
		coll := collectionForType(t)
		if coll == "" {
			return nil, fmt.Errorf("unknown type filter %q", t)
		}
		if !slices.Contains(out, coll) {
			out = append(out, coll)
		}
	}
	return out, nil
}
