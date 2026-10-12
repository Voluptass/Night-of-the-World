package purge

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu        sync.Mutex
	authorize func(context.Context, Target) error
	messages  func(context.Context, uint64, uint64) ([]Message, error)
	onDelete  func(context.Context, uint64) error
	bulkErr   error
	deleted   []uint64
	batches   [][]uint64
	channels  []uint64
	befores   []uint64
}

func (f *fakeAPI) Authorize(ctx context.Context, target Target) error {
	if f.authorize != nil {
		return f.authorize(ctx, target)
	}
	return ctx.Err()
}
func (f *fakeAPI) Messages(ctx context.Context, channel, before uint64) ([]Message, error) {
	f.mu.Lock()
	f.befores = append(f.befores, before)
	f.mu.Unlock()
	if f.messages != nil {
		return f.messages(ctx, channel, before)
	}
	return nil, nil
}
func (f *fakeAPI) Delete(ctx context.Context, channel, id uint64, reason string) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, id)
	f.channels = append(f.channels, channel)
	f.mu.Unlock()
	if f.onDelete != nil {
		return f.onDelete(ctx, id)
	}
	return nil
}
func (f *fakeAPI) BulkDelete(ctx context.Context, channel uint64, ids []uint64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, append([]uint64(nil), ids...))
	f.channels = append(f.channels, channel)
	return f.bulkErr
}

var testNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
var testTarget = Target{GuildID: 9, ThreadID: 1, AccountID: 42, ActorID: 7}

func ignoreStats(Stats) {}

func TestScanFullHistoryAndProtect(t *testing.T) {
	f := &fakeAPI{}
	f.messages = func(_ context.Context, channel, before uint64) ([]Message, error) {
		if before == 0 {
			page := make([]Message, 100)
			for i := range page {
				page[i] = Message{ID: uint64(300 - i), ChannelID: channel, AuthorID: 777, CreatedAt: testNow}
				if page[i].ID == 250 || page[i].ID == 201 {
					page[i].AuthorID = 42
				}
			}
			return page, nil
		}
		if before != 201 {
			t.Errorf("cursor=%d, want 201", before)
		}
		return []Message{
			{ID: 200, ChannelID: channel, AuthorID: 777},
			{ID: 199, ChannelID: channel, AuthorID: 42, Protected: true},
			{ID: 198, ChannelID: channel, AuthorID: 42, CreatedAt: testNow},
			{ID: 197, ChannelID: channel, AuthorID: 42, CreatedAt: testNow.Add(-30 * 24 * time.Hour)},
			{ID: 1, ChannelID: channel, AuthorID: 42},
		}, nil
	}
	engine := Engine{API: f, Now: func() time.Time { return testNow }}
	candidates, stats, err := engine.Scan(context.Background(), testTarget, ignoreStats)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint64
	for _, message := range candidates {
		ids = append(ids, message.ID)
	}
	if !reflect.DeepEqual(ids, []uint64{250, 201, 198, 197}) {
		t.Fatalf("wrong snapshot: %v", ids)
	}
	if stats.Scanned != 105 || stats.Matched != 4 || stats.Protected != 2 || stats.Recent != 3 || stats.Old != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if len(f.deleted) != 0 || len(f.batches) != 0 {
		t.Fatal("preview performed a deletion")
	}
}

func TestScanSkipsNonMatchingPagesAndDeduplicates(t *testing.T) {
	api := &fakeAPI{messages: func(_ context.Context, channel, before uint64) ([]Message, error) {
		if before == 0 {
			page := make([]Message, 100)
			for i := range page {
				page[i] = Message{ID: uint64(1000 - i), ChannelID: channel, AuthorID: 777}
			}
			return page, nil
		}
		if before != 901 {
			t.Errorf("cursor skipped a nonmatching page: %d", before)
		}
		return []Message{
			{ID: 900, ChannelID: channel, AuthorID: 42, CreatedAt: testNow},
			{ID: 900, ChannelID: channel, AuthorID: 42, CreatedAt: testNow},
			{ID: 899, ChannelID: channel, AuthorID: 777},
		}, nil
	}}
	engine := Engine{API: api, Now: func() time.Time { return testNow }}
	result, stats, err := engine.Scan(context.Background(), testTarget, ignoreStats)
	if err != nil || len(result) != 1 || result[0].ID != 900 || stats.Scanned != 102 {
		t.Fatalf("result=%v stats=%+v err=%v", result, stats, err)
	}
}

func TestScanEmptyAndUnauthorized(t *testing.T) {
	for _, denied := range []bool{false, true} {
		api := &fakeAPI{authorize: func(context.Context, Target) error {
			if denied {
				return ErrForbidden
			}
			return nil
		}}
		engine := Engine{API: api, Now: func() time.Time { return testNow }}
		result, stats, err := engine.Scan(context.Background(), testTarget, ignoreStats)
		if len(result) != 0 || stats.Matched != 0 {
			t.Fatalf("unexpected preview: %v", result)
		}
		if denied && (!errors.Is(err, ErrForbidden) || len(api.befores) != 0) {
			t.Fatal("unauthorized scan accessed history")
		}
		if !denied && err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanNoPartialPreviewOnFailure(t *testing.T) {
	for _, scenario := range []string{"page error", "no progress", "wrong channel", "new message outside snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			f := &fakeAPI{messages: func(_ context.Context, channel, before uint64) ([]Message, error) {
				if before != 0 {
					switch scenario {
					case "page error":
						return nil, ErrUnavailable
					case "no progress":
						return []Message{{ID: 300, ChannelID: channel}}, nil
					case "new message outside snapshot":
						return []Message{{ID: 999, ChannelID: channel, AuthorID: 42}}, nil
					}
				}
				page := make([]Message, 100)
				for i := range page {
					page[i] = Message{ID: uint64(300 - i), ChannelID: channel, AuthorID: 42, CreatedAt: testNow}
				}
				if scenario == "wrong channel" {
					page[0].ChannelID = 1234
				}
				return page, nil
			}}
			engine := Engine{API: f, Now: func() time.Time { return testNow }}
			candidates, _, err := engine.Scan(context.Background(), testTarget, ignoreStats)
			if err == nil || candidates != nil {
				t.Fatalf("partial executable preview: %v, %v", candidates, err)
			}
		})
	}
}

func TestScanLimitsFailClosed(t *testing.T) {
	for _, match := range []bool{false, true} {
		t.Run(map[bool]string{false: "scanned", true: "candidates"}[match], func(t *testing.T) {
			f := &fakeAPI{messages: func(_ context.Context, ch, before uint64) ([]Message, error) {
				if before == 0 {
					before = 1000000
				}
				page := make([]Message, 100)
				for i := range page {
					page[i] = Message{ID: before - uint64(i) - 1, ChannelID: ch, AuthorID: 777, CreatedAt: testNow}
					if match {
						page[i].AuthorID = 42
					}
				}
				return page, nil
			}}
			e := Engine{API: f, Now: func() time.Time { return testNow }}
			result, _, err := e.Scan(context.Background(), testTarget, ignoreStats)
			if !errors.Is(err, ErrScanLimit) || result != nil {
				t.Fatalf("result=%v err=%v", result, err)
			}
		})
	}
}

func candidates(count int, at time.Time) []Candidate {
	result := make([]Candidate, count)
	for i := range result {
		result[i] = Candidate{ID: uint64(1000 + i), CreatedAt: at}
	}
	return result
}

func TestExecuteBatchSizes(t *testing.T) {
	for _, n := range []int{0, 1, 2, 100, 101, 201} {
		t.Run(stringID(n), func(t *testing.T) {
			f := &fakeAPI{}
			e := Engine{API: f, Now: func() time.Time { return testNow }}
			input := candidates(n, testNow)
			stats, err := e.Execute(context.Background(), testTarget, input, Stats{Matched: n}, "test audit", ignoreStats)
			if err != nil || stats.Processed != n || stats.Remaining() != 0 {
				t.Fatalf("stats=%+v err=%v", stats, err)
			}
			var ids []uint64
			for _, batch := range f.batches {
				if len(batch) < 2 || len(batch) > 100 {
					t.Fatalf("batch=%d", len(batch))
				}
				ids = append(ids, batch...)
			}
			ids = append(ids, f.deleted...)
			var want []uint64
			for _, candidate := range input {
				want = append(want, candidate.ID)
			}
			if !reflect.DeepEqual(ids, want) {
				t.Fatalf("sent IDs %v != %v", ids, want)
			}
			for _, channel := range f.channels {
				if channel != testTarget.ThreadID {
					t.Fatalf("wrong channel %d", channel)
				}
			}
		})
	}
}

func stringID(n int) string { return strconv.Itoa(n) }

func TestExecuteAgeAndErrors(t *testing.T) {
	tests := []struct {
		name                                        string
		age                                         time.Duration
		bulkErr                                     error
		singleErr                                   error
		bulk, single, processed, missing, uncertain int
		wantErr                                     error
	}{
		{"old", 30 * 24 * time.Hour, nil, nil, 0, 3, 3, 0, 0, nil},
		{"exact boundary", BulkAge, nil, nil, 0, 3, 3, 0, 0, nil},
		{"near boundary", BulkAge - 30*time.Second, nil, nil, 0, 3, 3, 0, 0, nil},
		{"bulk aged out", time.Hour, ErrTooOld, nil, 1, 3, 3, 0, 0, nil},
		{"gone", BulkAge, nil, ErrMessageGone, 0, 3, 0, 3, 0, nil},
		{"forbidden", time.Hour, ErrForbidden, nil, 1, 0, 0, 0, 0, ErrForbidden},
		{"uncertain", time.Hour, ErrUncertain, nil, 1, 0, 0, 0, 3, ErrUncertain},
		{"rate limited", time.Hour, ErrUnavailable, nil, 1, 0, 0, 0, 0, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAPI{bulkErr: tt.bulkErr, onDelete: func(context.Context, uint64) error { return tt.singleErr }}
			e := Engine{API: f, Now: func() time.Time { return testNow }}
			stats, err := e.Execute(context.Background(), testTarget, candidates(3, testNow.Add(-tt.age)), Stats{Matched: 3}, "audit", ignoreStats)
			if !errors.Is(err, tt.wantErr) || stats.Processed != tt.processed || stats.Missing != tt.missing || stats.Uncertain != tt.uncertain || len(f.batches) != tt.bulk || len(f.deleted) != tt.single {
				t.Fatalf("stats=%+v err=%v batches=%v singles=%v", stats, err, f.batches, f.deleted)
			}
		})
	}
}

func TestRecheckAgeAfterAuthorization(t *testing.T) {
	now := testNow
	f := &fakeAPI{authorize: func(context.Context, Target) error { now = testNow.Add(BulkAge); return nil }}
	e := Engine{API: f, Now: func() time.Time { return now }}
	stats, err := e.Execute(context.Background(), testTarget, candidates(2, testNow), Stats{Matched: 2}, "audit", ignoreStats)
	if err != nil || stats.Processed != 2 || len(f.batches) != 0 || len(f.deleted) != 2 {
		t.Fatalf("%+v %v", stats, err)
	}
}

func TestStopOnPermissionLossOrCancellation(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		calls := 0
		f := &fakeAPI{authorize: func(context.Context, Target) error {
			calls++
			if calls > 1 {
				return ErrForbidden
			}
			return nil
		}}
		e := Engine{API: f, Now: func() time.Time { return testNow }}
		stats, err := e.Execute(context.Background(), testTarget, candidates(201, testNow), Stats{Matched: 201}, "audit", ignoreStats)
		if !errors.Is(err, ErrForbidden) || stats.Processed != 100 || stats.Remaining() != 101 || len(f.batches) != 1 {
			t.Fatalf("%+v %v", stats, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := &fakeAPI{onDelete: func(context.Context, uint64) error { cancel(); return nil }}
		e := Engine{API: f, Now: func() time.Time { return testNow }}
		stats, err := e.Execute(ctx, testTarget, candidates(3, testNow.Add(-BulkAge)), Stats{Matched: 3}, "audit", ignoreStats)
		if !errors.Is(err, context.Canceled) || stats.Processed != 1 || len(f.deleted) != 1 {
			t.Fatalf("%+v %v", stats, err)
		}
	})
}
