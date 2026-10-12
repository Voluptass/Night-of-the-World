package purge

import (
	"context"
	"errors"
	"time"
)

const (
	MaxScanned    = 100000
	MaxCandidates = 50000
	BulkAge       = 14 * 24 * time.Hour
	BulkMargin    = time.Minute
)

type Engine struct {
	API API
	Now func() time.Time
}

func Recent(message Candidate, now time.Time) bool {
	return message.CreatedAt.After(now.Add(-BulkAge).Add(BulkMargin))
}

func (e *Engine) Scan(ctx context.Context, target Target, update func(Stats)) ([]Candidate, Stats, error) {
	var stats Stats
	var candidates []Candidate
	seen := make(map[uint64]struct{})
	var before uint64
	for {
		if err := ctx.Err(); err != nil {
			return nil, stats, err
		}
		if err := e.API.Authorize(ctx, target); err != nil {
			return nil, stats, err
		}
		page, err := e.API.Messages(ctx, target.ThreadID, before)
		if err != nil {
			return nil, stats, err
		}
		if len(page) == 0 {
			return candidates, stats, nil
		}
		if len(page) > 100 {
			return nil, stats, ErrPagination
		}
		var next uint64
		for _, message := range page {
			if message.ID == 0 || message.ChannelID != target.ThreadID {
				return nil, stats, ErrPagination
			}
			if before != 0 && message.ID >= before {
				if _, duplicate := seen[message.ID]; duplicate {
					continue
				}
				return nil, stats, ErrPagination
			}
			if next == 0 || message.ID < next {
				next = message.ID
			}
			if _, duplicate := seen[message.ID]; duplicate {
				continue
			}
			seen[message.ID] = struct{}{}
			stats.Scanned++
			if stats.Scanned > MaxScanned {
				return nil, stats, ErrScanLimit
			}
			if message.AuthorID != target.AccountID {
				continue
			}
			if message.Protected || message.ID == target.ThreadID {
				stats.Protected++
				continue
			}
			candidate := Candidate{ID: message.ID, CreatedAt: message.CreatedAt}
			candidates = append(candidates, candidate)
			stats.Matched++
			if Recent(candidate, e.Now()) {
				stats.Recent++
			} else {
				stats.Old++
			}
			if len(candidates) > MaxCandidates {
				return nil, stats, ErrScanLimit
			}
		}
		update(stats)
		if next == 0 || (before != 0 && next >= before) {
			return nil, stats, ErrPagination
		}
		before = next
		if len(page) < 100 {
			return candidates, stats, nil
		}
	}
}

func (e *Engine) Execute(ctx context.Context, target Target, candidates []Candidate, stats Stats, reason string, update func(Stats)) (Stats, error) {
	deleteOne := func(candidate Candidate) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.API.Authorize(ctx, target); err != nil {
			return err
		}
		err := e.API.Delete(ctx, target.ThreadID, candidate.ID, reason)
		switch {
		case err == nil:
			stats.Processed++
		case errors.Is(err, ErrMessageGone):
			stats.Missing++
		default:
			if errors.Is(err, ErrUncertain) {
				stats.Uncertain++
			}
			update(stats)
			return err
		}
		update(stats)
		return nil
	}
	for offset := 0; offset < len(candidates); {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if !Recent(candidates[offset], e.Now()) {
			if err := deleteOne(candidates[offset]); err != nil {
				return stats, err
			}
			offset++
			continue
		}
		end := offset
		for end < len(candidates) && end-offset < 100 && Recent(candidates[end], e.Now()) {
			end++
		}
		if end-offset < 2 {
			if err := deleteOne(candidates[offset]); err != nil {
				return stats, err
			}
			offset++
			continue
		}
		if err := e.API.Authorize(ctx, target); err != nil {
			return stats, err
		}
		// Authorization may involve network waits. Recheck age immediately before sending.
		var ids []uint64
		for _, candidate := range candidates[offset:end] {
			if !Recent(candidate, e.Now()) {
				break
			}
			ids = append(ids, candidate.ID)
		}
		if len(ids) < 2 {
			if err := deleteOne(candidates[offset]); err != nil {
				return stats, err
			}
			offset++
			continue
		}
		end = offset + len(ids)
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		err := e.API.BulkDelete(ctx, target.ThreadID, ids, reason)
		if errors.Is(err, ErrTooOld) {
			for _, candidate := range candidates[offset:end] {
				if err := deleteOne(candidate); err != nil {
					return stats, err
				}
			}
		} else if err != nil {
			if errors.Is(err, ErrUncertain) {
				stats.Uncertain += len(ids)
			}
			update(stats)
			return stats, err
		} else {
			stats.Processed += len(ids)
			update(stats)
		}
		offset = end
	}
	return stats, nil
}
