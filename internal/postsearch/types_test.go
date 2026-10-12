package postsearch

import (
	"strings"
	"testing"
	"time"
)

func TestParseKeyword(t *testing.T) {
	terms, err := ParseKeyword("  CrAsH   Report ")
	if err != nil || len(terms) != 2 || terms[0] != "crash" || terms[1] != "report" {
		t.Fatalf("normalisation failed: %v %v", terms, err)
	}
	for _, raw := range []string{"", "   ", strings.Repeat("a", MaxTermRunes+1)} {
		if _, err := ParseKeyword(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	tooMany := strings.TrimSpace(strings.Repeat("w ", MaxKeywordTerms+1))
	if _, err := ParseKeyword(tooMany); err == nil {
		t.Fatal("accepted too many terms")
	}
}

func TestMatchAndHasAllTags(t *testing.T) {
	if !Match("Bug 报告 v2", []string{"bug", "报告"}) {
		t.Fatal("multi-term AND failed")
	}
	if Match("Bug 报告", []string{"bug", "缺失"}) {
		t.Fatal("matched with a missing term")
	}
	if !HasAllTags(nil, nil) {
		t.Fatal("空筛选应视为通过")
	}
	if !HasAllTags([]uint64{1, 2, 3}, []uint64{2, 3}) {
		t.Fatal("AND semantics failed")
	}
	if HasAllTags([]uint64{1}, []uint64{1, 2}) {
		t.Fatal("缺少标签仍然通过")
	}
}

func TestResultPageClamps(t *testing.T) {
	result := Result{}
	hits := make([]Hit, 25) // 25 条 → 3 页
	for i := range hits {
		hits[i] = Hit{ThreadID: uint64(i + 1)}
	}

	page, current, pages := result.Page(hits, 0)
	if len(page) != PageSize || current != 0 || pages != 3 {
		t.Fatalf("第一页错误: len=%d page=%d pages=%d", len(page), current, pages)
	}
	page, current, _ = result.Page(hits, 2)
	if len(page) != 5 || current != 2 {
		t.Fatalf("末页错误: len=%d page=%d", len(page), current)
	}
	if _, current, _ = result.Page(hits, 99); current != 2 {
		t.Fatalf("越界页未夹紧: %d", current)
	}
	if _, current, _ = result.Page(hits, -5); current != 0 {
		t.Fatalf("负数页未夹紧: %d", current)
	}
	empty, current, pages := result.Page(nil, 3)
	if empty != nil || current != 0 || pages != 0 {
		t.Fatal("空结果处理错误")
	}
}

func TestStoreOwnershipAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewStore(func() time.Time { return now })
	id, err := store.Save(Result{Forum: Forum{ID: 100}}, []Hit{{ThreadID: 1}}, 7, 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Load(id, 8, 9); err == nil {
		t.Fatal("其他用户读取到了他人的结果")
	}
	if _, _, _, err := store.Load(id, 7, 9); err != nil {
		t.Fatalf("本人应能读取: %v", err)
	}
	// 翻页会夹紧页码。
	if _, _, page, err := store.SetPage(id, 7, 9, 42); err != nil || page != 0 {
		t.Fatalf("单页结果页码错误: page=%d err=%v", page, err)
	}
	// 过期后不可读。
	now = now.Add(ResultTTL + time.Second)
	if _, _, _, err := store.Load(id, 7, 9); err == nil {
		t.Fatal("过期结果仍可读取")
	}
}

func TestStoreLimitsConcurrentSearches(t *testing.T) {
	store := NewStore(time.Now)
	releases := make([]func(), 0, MaxConcurrent)
	for i := 0; i < MaxConcurrent; i++ {
		release, err := store.Begin()
		if err != nil {
			t.Fatalf("第 %d 个名额被拒: %v", i+1, err)
		}
		releases = append(releases, release)
	}
	if _, err := store.Begin(); err == nil {
		t.Fatal("超出并发上限仍未拒绝")
	}
	releases[0]()
	release, err := store.Begin()
	if err != nil {
		t.Fatalf("释放后仍无法开始: %v", err)
	}
	release()
	// 重复释放不应把计数变成负数。
	for _, release := range releases {
		release()
	}
	if _, err := store.Begin(); err != nil {
		t.Fatalf("计数被破坏: %v", err)
	}
}
