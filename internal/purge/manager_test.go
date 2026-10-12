package purge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestManager(t *testing.T, api API) (*Manager, func(time.Duration)) {
	t.Helper()
	var clock atomic.Int64
	clock.Store(testNow.UnixNano())
	manager := NewManager(context.Background(), api, func() time.Time { return time.Unix(0, clock.Load()) })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return manager, func(d time.Duration) { clock.Add(int64(d)) }
}

func waitState(t *testing.T, manager *Manager, id string, state State) Job {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		job, ok := manager.Snapshot(id)
		if ok && job.State == state {
			return job
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("wanted %s, got %+v", state, job)
		}
	}
}

func readyAPI() *fakeAPI {
	return &fakeAPI{messages: func(_ context.Context, ch, _ uint64) ([]Message, error) {
		return []Message{{ID: 100, ChannelID: ch, AuthorID: 42, CreatedAt: testNow}, {ID: 99, ChannelID: ch, AuthorID: 42, CreatedAt: testNow}}, nil
	}}
}

func TestManagerPreviewOwnerAndSingleConfirmation(t *testing.T) {
	api := readyAPI()
	manager, _ := newTestManager(t, api)
	job, err := manager.Start(testTarget)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, job.ID, Ready)
	if _, err := manager.Status(context.Background(), job.ID, 999, testTarget.GuildID); !errors.Is(err, ErrTaskAccess) {
		t.Fatalf("foreign status: %v", err)
	}
	if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, 999); !errors.Is(err, ErrTaskAccess) {
		t.Fatalf("foreign guild confirm: %v", err)
	}
	if _, err := manager.Cancel(job.ID, 999, testTarget.GuildID); !errors.Is(err, ErrTaskAccess) {
		t.Fatalf("foreign cancel: %v", err)
	}
	api.mu.Lock()
	deletes := len(api.deleted) + len(api.batches)
	api.mu.Unlock()
	if deletes != 0 {
		t.Fatal("preview/unauthorized action deleted messages")
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted confirmations=%d", accepted.Load())
	}
	completed := waitState(t, manager, job.ID, Completed)
	if completed.Stats.Processed != 2 {
		t.Fatalf("%+v", completed)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.batches) != 1 || len(api.batches[0]) != 2 || api.batches[0][0] != 100 || api.batches[0][1] != 99 {
		t.Fatalf("deleted outside snapshot: %v", api.batches)
	}
}

func TestManagerExpiryAndCapacity(t *testing.T) {
	manager, advance := newTestManager(t, readyAPI())
	job, err := manager.Start(testTarget)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, job.ID, Ready)
	if _, err := manager.Start(testTarget); !errors.Is(err, ErrBusy) {
		t.Fatalf("same thread allowed: %v", err)
	}
	other := testTarget
	other.ThreadID = 2
	otherJob, err := manager.Start(other)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, otherJob.ID, Ready)
	other.ThreadID = 3
	if _, err := manager.Start(other); !errors.Is(err, ErrBusy) {
		t.Fatalf("capacity: %v", err)
	}
	advance(PreviewTTL)
	if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); !errors.Is(err, ErrTaskState) {
		t.Fatalf("expired confirm: %v", err)
	}
	waitState(t, manager, job.ID, Expired)
	advance(RecordTTL)
	if _, ok := manager.Snapshot(job.ID); ok {
		t.Fatal("finished record not collected")
	}
}

func TestManagerCancelAndNoAutomaticResume(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		api := readyAPI()
		manager, _ := newTestManager(t, api)
		job, err := manager.Start(testTarget)
		if err != nil {
			t.Fatal(err)
		}
		waitState(t, manager, job.ID, Ready)
		if _, err := manager.Cancel(job.ID, testTarget.ActorID, testTarget.GuildID); err != nil {
			t.Fatal(err)
		}
		waitState(t, manager, job.ID, Cancelled)
		if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); !errors.Is(err, ErrTaskState) {
			t.Fatal(err)
		}
		fresh, _ := newTestManager(t, api)
		if _, ok := fresh.Snapshot(job.ID); ok {
			t.Fatal("job survived process restart unexpectedly")
		}
		api.mu.Lock()
		defer api.mu.Unlock()
		if len(api.batches)+len(api.deleted) != 0 {
			t.Fatal("cancelled preview deleted messages")
		}
	})
	t.Run("scanning", func(t *testing.T) {
		entered := make(chan struct{})
		api := &fakeAPI{messages: func(ctx context.Context, _, _ uint64) ([]Message, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		manager, _ := newTestManager(t, api)
		job, err := manager.Start(testTarget)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("scan did not start")
		}
		if _, err := manager.Cancel(job.ID, testTarget.ActorID, testTarget.GuildID); err != nil {
			t.Fatal(err)
		}
		waitState(t, manager, job.ID, Cancelled)
	})
}

func TestManagerRechecksPermissionAndStatusOutlivesToken(t *testing.T) {
	var denied atomic.Bool
	api := readyAPI()
	api.authorize = func(context.Context, Target) error {
		if denied.Load() {
			return ErrForbidden
		}
		return nil
	}
	manager, advance := newTestManager(t, api)
	job, err := manager.Start(testTarget)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, job.ID, Ready)
	denied.Store(true)
	if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked permission allowed: %v", err)
	}
	denied.Store(false)
	if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, job.ID, Completed)
	advance(16 * time.Minute)
	status, err := manager.Status(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID)
	if err != nil || status.Stats.Processed != 2 {
		t.Fatalf("long-lived status: %+v, %v", status, err)
	}
}

func TestManagerFailedScanCannotBeConfirmed(t *testing.T) {
	api := &fakeAPI{messages: func(context.Context, uint64, uint64) ([]Message, error) { return nil, ErrUnavailable }}
	manager, _ := newTestManager(t, api)
	job, err := manager.Start(testTarget)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, manager, job.ID, Failed)
	if _, err := manager.Confirm(context.Background(), job.ID, testTarget.ActorID, testTarget.GuildID); !errors.Is(err, ErrTaskState) {
		t.Fatal(err)
	}
}

func TestSafeErrorDoesNotExposeRemoteDetails(t *testing.T) {
	secret := errors.New("https://discord.com/api/webhooks/123/secret-token body=private")
	if got := SafeError(secret); got == secret.Error() {
		t.Fatal("raw secret-containing error exposed")
	}
}
