// Package postsearch 实现论坛/媒体频道内帖子（线程）的检索与标签筛选。
//
// Discord 没有“搜索帖子”接口，只有枚举接口，因此这里采用“枚举 + 本地过滤”：
// 活跃线程来自全服接口，归档线程按论坛频道分页拉取。详见 Engine.Search。
package postsearch

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrForbidden    = errors.New("操作者或 Bot 缺少查看该频道或读取历史记录权限")
	ErrNotForum     = errors.New("目标必须是本服务器的论坛或媒体频道")
	ErrInvalidQuery = errors.New("搜索条件无效：关键词不能为空或过长，标签必须属于该论坛")
	ErrTaskAccess   = errors.New("查询不存在或已过期，或不是你在本服务器发起的查询")
	ErrBusy         = errors.New("同时进行的查询过多，请稍后重试")
	ErrUnavailable  = errors.New("Discord 请求失败，请稍后重试")
)

const (
	// MaxThreads 限制单次查询枚举的帖子总数，避免大服上无限翻页。
	MaxThreads = 50000
	// PageSize 是每页展示的命中数量。
	// 20 条 × 长标题加链接很容易超过 Discord 的 2000 字符上限，
	// 因此取 10，并在渲染时再做一次预算保护。
	PageSize = 10
	// MaxKeywordTerms 限制关键词个数。
	MaxKeywordTerms = 10
	// MaxTermRunes 限制单个关键词长度。
	MaxTermRunes = 64
	// SearchIDLength 是结果 ID 的十六进制字符长度，用于校验按钮回传值。
	SearchIDLength = 16
)

// Target 描述一次查询的范围：哪个论坛、由谁发起。
type Target struct {
	GuildID   uint64
	ChannelID uint64
	ActorID   uint64
}

// Forum 是校验通过后的论坛信息。
type Forum struct {
	ID            uint64
	Name          string
	AvailableTags []Tag
}

// Tag 是论坛的可选标签。
type Tag struct {
	ID   uint64
	Name string
}

// Thread 是枚举接口返回的一条帖子/线程（已剔除非本论坛的字段）。
type Thread struct {
	ID           uint64
	ParentID     uint64
	OwnerID      uint64
	Name         string
	AppliedTags  []uint64
	CreatedAt    time.Time
	ArchivedAt   time.Time
	MessageCount int
	MemberCount  int
	Locked       bool
	Archived     bool
}

// Hit 是一条命中的帖子。
type Hit struct {
	ThreadID     uint64
	ParentID     uint64
	Name         string
	MatchedTags  []uint64
	CreatedAt    time.Time
	MessageCount int
	Archived     bool
}

// Result 汇总一次查询的条件与统计，供界面渲染。
type Result struct {
	Forum        Forum
	KeywordTerms []string
	TagIDs       []uint64
	Scanned      int
	Matched      int
	ArchivedOnly bool
	Truncated    bool
}

// API 是检索所需的最小接口，便于离线测试。
type API interface {
	// Authorize 校验目标频道是论坛/媒体频道，并确认操作者与 Bot 都有读取权限。
	Authorize(ctx context.Context, target Target) (Forum, error)
	// ActiveThreads 返回全服活跃线程，调用方按 ParentID 过滤。
	ActiveThreads(ctx context.Context, guildID uint64) ([]Thread, error)
	// ArchivedThreads 返回指定论坛的一页归档线程，按归档时间降序。
	// hasMore 表示可能还有更多。
	ArchivedThreads(ctx context.Context, channelID uint64, before time.Time) ([]Thread, bool, error)
}

// ParseKeyword 把原始关键词拆成小写的多词条件（AND 关系）。
func ParseKeyword(raw string) ([]string, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 || len(fields) > MaxKeywordTerms {
		return nil, ErrInvalidQuery
	}
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) > MaxTermRunes {
			return nil, ErrInvalidQuery
		}
		terms = append(terms, strings.ToLower(field))
	}
	return terms, nil
}

// Match 判断标题是否包含全部关键词（不区分大小写）。
func Match(name string, terms []string) bool {
	lower := strings.ToLower(name)
	for _, term := range terms {
		if !strings.Contains(lower, term) {
			return false
		}
	}
	return true
}

// HasAllTags 判断帖子标签是否覆盖全部要求（AND 关系）。
func HasAllTags(applied, want []uint64) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[uint64]struct{}, len(applied))
	for _, id := range applied {
		set[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

// Page 返回第 page 页（从 0 开始）及总页数。页码越界时会被夹到有效范围。
func (r Result) Page(hits []Hit, page int) ([]Hit, int, int) {
	pages := (len(hits) + PageSize - 1) / PageSize
	if pages == 0 {
		return nil, 0, 0
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * PageSize
	end := start + PageSize
	if end > len(hits) {
		end = len(hits)
	}
	return hits[start:end], page, pages
}

// SafeError 返回可直接展示的错误文本，不回显请求 URL、响应体或凭据。
func SafeError(err error) string {
	for _, known := range []error{ErrForbidden, ErrNotForum, ErrInvalidQuery, ErrTaskAccess, ErrBusy, ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "搜索超时；该论坛帖子较多，请缩小范围后重试"
	}
	if errors.Is(err, context.Canceled) {
		return "搜索已停止"
	}
	return "操作失败；未确认的请求不会计入结果，请重新搜索"
}
