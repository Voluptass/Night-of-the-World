package postsearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

const (
	// ResultTTL 是结果在内存中保留的时长（翻页需要）。
	ResultTTL = 15 * time.Minute
	// SearchTimeout 限制单次枚举的时长。
	SearchTimeout = 2 * time.Minute
	// MaxConcurrent 限制同时进行的枚举数量，避免占满 Discord 限流配额。
	MaxConcurrent = 3
)

type stored struct {
	result Result
	hits   []Hit
	owner  uint64
	guild  uint64
	at     time.Time
	page   int
}

// Store 保存检索结果供翻页使用，并限制并发枚举数量。
// 结果只存内存，Bot 重启后消失。
type Store struct {
	mu     sync.Mutex
	items  map[string]*stored
	active int
	now    func() time.Time
	newID  func() (string, error)
}

func NewStore(now func() time.Time) *Store {
	return &Store{
		items: make(map[string]*stored),
		now:   now,
		newID: func() (string, error) {
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return "", err
			}
			return hex.EncodeToString(token[:]), nil
		},
	}
}

// Begin 预留一个并发名额，失败时返回 ErrBusy。调用方必须调用返回的 release。
func (s *Store) Begin() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if s.active >= MaxConcurrent {
		return nil, ErrBusy
	}
	s.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.active--
			s.mu.Unlock()
		})
	}, nil
}

// Save 保存一次检索结果并返回其 ID。
func (s *Store) Save(result Result, hits []Hit, actor, guild uint64) (string, error) {
	id, err := s.newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.items[id] = &stored{result: result, hits: hits, owner: actor, guild: guild, at: s.now()}
	slog.Info("帖子检索完成", "query", id, "guild", guild, "actor", actor, "channel", result.Forum.ID,
		"scanned", result.Scanned, "matched", result.Matched, "truncated", result.Truncated)
	return id, nil
}

// Load 读取结果，并校验归属与有效期。
func (s *Store) Load(id string, actor, guild uint64) (Result, []Hit, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.items[id]
	if !ok || item.owner != actor || item.guild != guild {
		return Result{}, nil, 0, ErrTaskAccess
	}
	return item.result, item.hits, item.page, nil
}

// SetPage 切换到指定页（越界会被夹到有效范围）并返回最新状态。
func (s *Store) SetPage(id string, actor, guild uint64, page int) (Result, []Hit, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.items[id]
	if !ok || item.owner != actor || item.guild != guild {
		return Result{}, nil, 0, ErrTaskAccess
	}
	_, clamped, _ := item.result.Page(item.hits, page)
	item.page = clamped
	return item.result, item.hits, clamped, nil
}

func (s *Store) pruneLocked() {
	now := s.now()
	for id, item := range s.items {
		if now.Sub(item.at) >= ResultTTL {
			delete(s.items, id)
		}
	}
}

// Context 返回带超时的子上下文，供一次检索使用。
func Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, SearchTimeout)
}
