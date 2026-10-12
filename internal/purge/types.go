package purge

import (
	"context"
	"errors"
	"time"
)

var (
	ErrForbidden             = errors.New("操作者或 Bot 缺少目标帖的查看、读取历史或管理消息权限")
	ErrManageThreadsRequired = errors.New("清理同时归档且锁定的帖子，操作者和 Bot 都需要目标帖的管理线程（Manage Threads）权限")
	ErrInvalidTarget         = errors.New("仅支持本服务器中的论坛帖子或公开线程")
	ErrMessageGone           = errors.New("消息已不存在")
	ErrTooOld                = errors.New("消息超出批量删除的时间范围")
	ErrUnavailable           = errors.New("Discord 请求失败，请稍后重新预览")
	ErrUncertain             = errors.New("请求结果不确定；可能已经部分删除，请重新预览核对")
	ErrScanLimit             = errors.New("预览超出安全上限，未执行删除")
	ErrPagination            = errors.New("历史分页数据异常，预览不完整，未执行删除")
	ErrTaskAccess            = errors.New("任务不存在，或不是你在本服务器创建的任务")
	ErrTaskState             = errors.New("任务状态已改变或确认已过期，请查询状态或重新预览")
	ErrBusy                  = errors.New("该帖子已有任务，或活动任务已达上限，请稍后重试")
)

type Target struct {
	GuildID   uint64
	ThreadID  uint64
	AccountID uint64
	ActorID   uint64
}

type Message struct {
	ID        uint64
	ChannelID uint64
	AuthorID  uint64
	CreatedAt time.Time
	Protected bool
}

type Candidate struct {
	ID        uint64
	CreatedAt time.Time
}

type API interface {
	Authorize(context.Context, Target) error
	Messages(ctx context.Context, channelID, before uint64) ([]Message, error)
	Delete(ctx context.Context, channelID, messageID uint64, reason string) error
	BulkDelete(ctx context.Context, channelID uint64, messageIDs []uint64, reason string) error
}

type Stats struct {
	Scanned   int
	Matched   int
	Protected int
	Recent    int
	Old       int
	Processed int
	Missing   int
	Uncertain int
}

func (s Stats) Remaining() int { return s.Matched - s.Processed - s.Missing }

// SafeError never forwards HTTP request URLs, response bodies or tokens to users or logs.
func SafeError(err error) string {
	for _, known := range []error{ErrForbidden, ErrManageThreadsRequired, ErrInvalidTarget, ErrMessageGone, ErrTooOld, ErrUncertain, ErrScanLimit, ErrPagination, ErrTaskAccess, ErrTaskState, ErrBusy, ErrUnavailable} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "任务超时；请核对部分处理结果后重新预览"
	}
	if errors.Is(err, context.Canceled) {
		return "任务已停止；已删除的内容无法恢复"
	}
	return "操作失败；未确认的请求不会计入成功，请重新预览核对"
}
