package purge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	PreviewTTL    = 5 * time.Minute
	RecordTTL     = 30 * time.Minute
	ScanTimeout   = 5 * time.Minute
	DeleteTimeout = 2 * time.Hour
	MaxActiveJobs = 2
	MaxJobRecords = 100
)

type State string

const (
	Scanning  State = "scanning"
	Ready     State = "ready"
	Deleting  State = "deleting"
	Completed State = "completed"
	Failed    State = "failed"
	Cancelled State = "cancelled"
	Expired   State = "expired"
)

type Job struct {
	ID              string
	Target          Target
	State           State
	Stats           Stats
	Error           string
	ExpiresAt       time.Time
	FinishedAt      time.Time
	CancelRequested bool
}

type record struct {
	Job
	candidates []Candidate
	cancel     context.CancelFunc
}

type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*record
	engine Engine
	now    func() time.Time
	newID  func() (string, error)
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
}

func NewManager(parent context.Context, api API, now func() time.Time) *Manager {
	ctx, cancel := context.WithCancel(parent)
	m := &Manager{
		jobs: make(map[string]*record), engine: Engine{API: api, Now: now}, now: now,
		ctx: ctx, cancel: cancel,
		newID: func() (string, error) {
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return "", err
			}
			return hex.EncodeToString(token[:]), nil
		},
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				m.pruneLocked()
				m.mu.Unlock()
			}
		}
	}()
	return m
}

func active(state State) bool { return state == Scanning || state == Ready || state == Deleting }

func (m *Manager) pruneLocked() {
	now := m.now()
	for id, job := range m.jobs {
		if job.State == Ready && !now.Before(job.ExpiresAt) {
			m.finishLocked(job, Expired, nil)
		}
		if !active(job.State) && now.Sub(job.FinishedAt) >= RecordTTL {
			delete(m.jobs, id)
		}
	}
}

func (m *Manager) finishLocked(job *record, state State, err error) {
	job.State = state
	job.FinishedAt = m.now()
	job.candidates = nil
	if job.cancel != nil {
		job.cancel()
		job.cancel = nil
	}
	if err != nil {
		job.Error = SafeError(err)
	}
	slog.Info("清理任务结束", "task", job.ID, "guild", job.Target.GuildID, "thread", job.Target.ThreadID, "actor", job.Target.ActorID, "account", job.Target.AccountID, "state", state, "processed", job.Stats.Processed, "missing", job.Stats.Missing, "remaining", job.Stats.Remaining(), "uncertain", job.Stats.Uncertain, "error", job.Error)
}

func (m *Manager) Start(target Target) (Job, error) {
	if target.GuildID == 0 || target.ThreadID == 0 || target.AccountID == 0 || target.ActorID == 0 {
		return Job{}, ErrInvalidTarget
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if m.closed || m.ctx.Err() != nil {
		return Job{}, context.Canceled
	}
	count := 0
	for _, job := range m.jobs {
		if active(job.State) {
			count++
			if job.Target.ThreadID == target.ThreadID {
				return Job{}, ErrBusy
			}
		}
	}
	if count >= MaxActiveJobs || len(m.jobs) >= MaxJobRecords {
		return Job{}, ErrBusy
	}
	id, err := m.newID()
	if err != nil {
		return Job{}, err
	}
	if _, exists := m.jobs[id]; exists {
		return Job{}, ErrBusy
	}
	ctx, cancel := context.WithTimeout(m.ctx, ScanTimeout)
	job := &record{Job: Job{ID: id, Target: target, State: Scanning}, cancel: cancel}
	m.jobs[id] = job
	m.wg.Add(1)
	go m.scan(ctx, job)
	return job.Job, nil
}

func (m *Manager) update(job *record, stats Stats) {
	m.mu.Lock()
	job.Stats = stats
	m.mu.Unlock()
}

func (m *Manager) scan(ctx context.Context, job *record) {
	defer m.wg.Done()
	candidates, stats, err := m.engine.Scan(ctx, job.Target, func(stats Stats) { m.update(job, stats) })
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job.Stats = stats
	if err != nil {
		state := Failed
		if job.CancelRequested || m.ctx.Err() != nil {
			state = Cancelled
		}
		m.finishLocked(job, state, err)
	} else if len(candidates) == 0 {
		m.finishLocked(job, Completed, nil)
	} else {
		job.cancel()
		job.cancel = nil
		job.candidates = candidates
		job.State = Ready
		job.ExpiresAt = m.now().Add(PreviewTTL)
	}
}

// Snapshot is for internal progress rendering only. User commands must use Status.
func (m *Manager) Snapshot(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return job.Job, true
}

func (m *Manager) owned(id string, actor, guild uint64) (Job, error) {
	job, ok := m.Snapshot(id)
	if !ok || job.Target.ActorID != actor || job.Target.GuildID != guild {
		return Job{}, ErrTaskAccess
	}
	return job, nil
}

func (m *Manager) Status(ctx context.Context, id string, actor, guild uint64) (Job, error) {
	job, err := m.owned(id, actor, guild)
	if err != nil {
		return Job{}, err
	}
	if err := m.engine.API.Authorize(ctx, job.Target); err != nil {
		return Job{}, err
	}
	return m.owned(id, actor, guild)
}

func (m *Manager) Confirm(ctx context.Context, id string, actor, guild uint64) (Job, error) {
	job, err := m.owned(id, actor, guild)
	if err != nil {
		return Job{}, err
	}
	if job.State != Ready {
		return Job{}, ErrTaskState
	}
	if err := m.engine.API.Authorize(ctx, job.Target); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	record, ok := m.jobs[id]
	if !ok || record.State != Ready || m.closed || m.ctx.Err() != nil || ctx.Err() != nil {
		return Job{}, ErrTaskState
	}
	record.State = Deleting
	workerCtx, cancel := context.WithTimeout(m.ctx, DeleteTimeout)
	record.cancel = cancel
	candidates := record.candidates
	stats := record.Stats
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		reason := fmt.Sprintf("purge task=%s actor=%d account=%d thread=%d", id, job.Target.ActorID, job.Target.AccountID, job.Target.ThreadID)
		result, err := m.engine.Execute(workerCtx, job.Target, candidates, stats, reason, func(stats Stats) { m.update(record, stats) })
		if workerCtx.Err() != nil && err == nil {
			err = workerCtx.Err()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		record.Stats = result
		state := Completed
		if err != nil {
			state = Failed
		}
		if record.CancelRequested || m.ctx.Err() != nil {
			state = Cancelled
		}
		m.finishLocked(record, state, err)
	}()
	return record.Job, nil
}

func (m *Manager) Cancel(id string, actor, guild uint64) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	job, ok := m.jobs[id]
	if !ok || job.Target.ActorID != actor || job.Target.GuildID != guild {
		return Job{}, ErrTaskAccess
	}
	if job.State == Ready {
		job.CancelRequested = true
		m.finishLocked(job, Cancelled, nil)
	} else if job.State == Scanning || job.State == Deleting {
		job.CancelRequested = true
		job.cancel()
	}
	return job.Job, nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	for _, job := range m.jobs {
		if job.State == Ready {
			m.finishLocked(job, Cancelled, nil)
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
