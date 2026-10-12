package postsearch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeAPI 是可控的离线 API：活跃线程与归档分页都由测试给定。
type fakeAPI struct {
	forum    Forum
	authErr  error
	active   []Thread
	archived [][]Thread
	hasMore  []bool
	calls    int
}

func (f *fakeAPI) Authorize(context.Context, Target) (Forum, error) {
	if f.authErr != nil {
		return Forum{}, f.authErr
	}
	return f.forum, nil
}

func (f *fakeAPI) ActiveThreads(context.Context, uint64) ([]Thread, error) {
	return f.active, nil
}

func (f *fakeAPI) ArchivedThreads(_ context.Context, _ uint64, before time.Time) ([]Thread, bool, error) {
	index := f.calls
	f.calls++
	if index >= len(f.archived) {
		return nil, false, nil
	}
	hasMore := false
	if index < len(f.hasMore) {
		hasMore = f.hasMore[index]
	}
	return f.archived[index], hasMore, nil
}

func base(guild, channel uint64) (Forum, Target) {
	forum := Forum{ID: channel, Name: "反馈", AvailableTags: []Tag{{ID: 11, Name: "已解决"}, {ID: 22, Name: "待处理"}}}
	return forum, Target{GuildID: guild, ChannelID: channel, ActorID: 7}
}

func TestSearchFiltersByTitleAndTags(t *testing.T) {
	forum, target := base(9, 100)
	api := &fakeAPI{forum: forum, active: []Thread{
		{ID: 1, ParentID: 100, Name: "崩溃 报告", AppliedTags: []uint64{11}, CreatedAt: time.Unix(300, 0)},
		{ID: 2, ParentID: 100, Name: "崩溃 报告", AppliedTags: []uint64{22}, CreatedAt: time.Unix(400, 0)},
		{ID: 3, ParentID: 100, Name: "无关帖子", AppliedTags: []uint64{11}, CreatedAt: time.Unix(500, 0)},
		{ID: 4, ParentID: 999, Name: "崩溃 报告", AppliedTags: []uint64{11}, CreatedAt: time.Unix(600, 0)},
	}}
	engine := Engine{API: api}

	// 只按标题。
	result, hits, err := engine.Search(context.Background(), target, []string{"崩溃"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 3 {
		t.Fatalf("scanned=%d, 只应统计本论坛的 3 条", result.Scanned)
	}
	if len(hits) != 2 {
		t.Fatalf("hits=%d, 期望 2 条（排除其他论坛与不匹配标题）", len(hits))
	}
	// 按 CreatedAt 倒序。
	if hits[0].ThreadID != 2 || hits[1].ThreadID != 1 {
		t.Fatalf("排序错误: %d %d", hits[0].ThreadID, hits[1].ThreadID)
	}

	// 标题 + 标签 AND。
	result, hits, err = engine.Search(context.Background(), target, []string{"报告"}, []uint64{22})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ThreadID != 2 {
		t.Fatalf("标签筛选失败: %+v", hits)
	}
	if len(hits[0].MatchedTags) != 1 || hits[0].MatchedTags[0] != 22 {
		t.Fatalf("命中标签未回填: %+v", hits[0].MatchedTags)
	}
	if result.Matched != 1 {
		t.Fatalf("matched=%d", result.Matched)
	}
}

func TestSearchRejectsUnknownTagAndEmptyKeyword(t *testing.T) {
	forum, target := base(9, 100)
	engine := Engine{API: &fakeAPI{forum: forum}}

	if _, _, err := engine.Search(context.Background(), target, nil, nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("空关键词未拦截: %v", err)
	}
	// 标签必须属于该论坛。
	if _, _, err := engine.Search(context.Background(), target, []string{"a"}, []uint64{999}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("未知标签未拦截: %v", err)
	}
}

func TestSearchPaginatesArchivedAndDeduplicates(t *testing.T) {
	forum, target := base(9, 100)
	older := time.Unix(100, 0)
	newer := time.Unix(200, 0)
	api := &fakeAPI{
		forum: forum,
		// 活跃区已出现 ID 1，归档第一页重复出现，必须去重。
		active: []Thread{{ID: 1, ParentID: 100, Name: "同名 帖子", CreatedAt: newer}},
		archived: [][]Thread{
			{{ID: 1, ParentID: 100, Name: "同名 帖子", CreatedAt: newer, Archived: true, ArchivedAt: newer},
				{ID: 2, ParentID: 100, Name: "同名 帖子", CreatedAt: older, Archived: true, ArchivedAt: older}},
		},
		hasMore: []bool{false},
	}
	engine := Engine{API: api}

	result, hits, err := engine.Search(context.Background(), target, []string{"同名"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("去重失败: %d 条 %+v", len(hits), hits)
	}
	if result.Scanned != 2 {
		t.Fatalf("scanned=%d，重复项不应重复计数", result.Scanned)
	}
	if !hits[1].Archived {
		t.Fatal("归档标记未透传")
	}
}
