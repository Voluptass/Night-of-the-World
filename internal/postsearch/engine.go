package postsearch

import (
	"context"
	"sort"
	"time"
)

// Engine 执行枚举与过滤。它是纯逻辑，不关心 Discord 或界面。
type Engine struct {
	API API
}

// Search 枚举目标论坛的帖子并按关键词与标签过滤。
//
// 扫描顺序：先全服活跃线程中属于本论坛的部分，再按归档时间倒序翻归档线程。
// 只要关键词命中标题就纳入结果；标签为空表示不筛选。
func (e *Engine) Search(ctx context.Context, target Target, terms []string, tagIDs []uint64) (Result, []Hit, error) {
	if len(terms) == 0 {
		return Result{}, nil, ErrInvalidQuery
	}
	forum, err := e.API.Authorize(ctx, target)
	if err != nil {
		return Result{}, nil, err
	}
	// 标签必须真实存在，避免拼错却静默返回全部结果。
	valid := make(map[uint64]struct{}, len(forum.AvailableTags))
	for _, tag := range forum.AvailableTags {
		valid[tag.ID] = struct{}{}
	}
	for _, id := range tagIDs {
		if _, ok := valid[id]; !ok {
			return Result{}, nil, ErrInvalidQuery
		}
	}

	result := Result{Forum: forum, KeywordTerms: terms, TagIDs: tagIDs}
	var hits []Hit
	seen := make(map[uint64]struct{})

	record := func(thread Thread) {
		if _, dup := seen[thread.ID]; dup {
			return
		}
		seen[thread.ID] = struct{}{}
		result.Scanned++
		if !Match(thread.Name, terms) || !HasAllTags(thread.AppliedTags, tagIDs) {
			return
		}
		result.Matched++
		hits = append(hits, Hit{
			ThreadID:     thread.ID,
			ParentID:     thread.ParentID,
			Name:         thread.Name,
			MatchedTags:  intersect(thread.AppliedTags, tagIDs),
			CreatedAt:    thread.CreatedAt,
			MessageCount: thread.MessageCount,
			Archived:     thread.Archived,
		})
	}

	active, err := e.API.ActiveThreads(ctx, target.GuildID)
	if err != nil {
		return Result{}, nil, err
	}
	for _, thread := range active {
		if err := ctx.Err(); err != nil {
			return Result{}, nil, err
		}
		if thread.ParentID != target.ChannelID {
			continue
		}
		record(thread)
	}

	var before time.Time
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, nil, err
		}
		page, hasMore, err := e.API.ArchivedThreads(ctx, target.ChannelID, before)
		if err != nil {
			return Result{}, nil, err
		}
		if len(page) == 0 {
			break
		}
		var oldest time.Time
		for _, thread := range page {
			if thread.ParentID != target.ChannelID {
				return Result{}, nil, ErrNotForum
			}
			record(thread)
			if oldest.IsZero() || thread.ArchivedAt.Before(oldest) {
				oldest = thread.ArchivedAt
			}
		}
		if result.Scanned > MaxThreads {
			result.Truncated = true
			break
		}
		// 归档接口按 archive_timestamp 倒序，用最旧的一条作为下一页游标。
		if !hasMore || oldest.IsZero() || (!before.IsZero() && !oldest.Before(before)) {
			break
		}
		before = oldest
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].CreatedAt.Equal(hits[j].CreatedAt) {
			return hits[i].ThreadID > hits[j].ThreadID
		}
		return hits[i].CreatedAt.After(hits[j].CreatedAt)
	})
	return result, hits, nil
}

// intersect 返回 applied 中同时出现在 want 里的标签，保持原顺序。
func intersect(applied, want []uint64) []uint64 {
	if len(want) == 0 {
		return nil
	}
	set := make(map[uint64]struct{}, len(want))
	for _, id := range want {
		set[id] = struct{}{}
	}
	var out []uint64
	for _, id := range applied {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
