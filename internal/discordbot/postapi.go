package discordbot

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Voluptass/Night-of-the-World/internal/postsearch"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// ArchivePageSize 是每次拉取归档线程的数量，Discord 上限为 100。
const ArchivePageSize = 100

// PostAPI 实现 postsearch.API。
type PostAPI struct {
	Rest    rest.Rest
	GuildID uint64
	BotID   func() uint64
	Now     func() time.Time
}

var _ postsearch.API = (*PostAPI)(nil)

// forumTags 从支持标签的频道类型中取出可选标签。
func forumTags(channel discord.GuildChannel) ([]discord.ChannelTag, bool) {
	switch c := channel.(type) {
	case discord.GuildForumChannel:
		return c.AvailableTags, true
	case discord.GuildMediaChannel:
		return c.AvailableTags, true
	default:
		return nil, false
	}
}

// Authorize 校验目标是本服务器的论坛/媒体频道，并确认操作者与 Bot 都有查看与读取历史记录权限。
func (a *PostAPI) Authorize(ctx context.Context, target postsearch.Target) (postsearch.Forum, error) {
	if target.GuildID != a.GuildID || target.ChannelID == 0 || target.ActorID == 0 || a.BotID() == 0 {
		return postsearch.Forum{}, postsearch.ErrNotForum
	}
	channel, err := a.Rest.GetChannel(snowflake.ID(target.ChannelID), rest.WithCtx(ctx))
	if err != nil {
		return postsearch.Forum{}, postError(err)
	}
	guildChannel, ok := channel.(discord.GuildChannel)
	if !ok || uint64(guildChannel.GuildID()) != a.GuildID || channel.ID() != snowflake.ID(target.ChannelID) {
		return postsearch.Forum{}, postsearch.ErrNotForum
	}
	tags, ok := forumTags(guildChannel)
	if !ok {
		return postsearch.Forum{}, postsearch.ErrNotForum
	}
	// 论坛频道自上而下：需要能看到频道并读取历史记录，才能枚举帖子。
	guild, err := a.Rest.GetGuild(snowflake.ID(target.GuildID), false, rest.WithCtx(ctx))
	if err != nil {
		return postsearch.Forum{}, postError(err)
	}
	roles, err := a.Rest.GetRoles(snowflake.ID(target.GuildID), rest.WithCtx(ctx))
	if err != nil {
		return postsearch.Forum{}, postError(err)
	}
	for _, id := range []uint64{target.ActorID, a.BotID()} {
		member, err := a.Rest.GetMember(snowflake.ID(target.GuildID), snowflake.ID(id), rest.WithCtx(ctx))
		if err != nil {
			return postsearch.Forum{}, postError(err)
		}
		if uint64(member.User.ID) != id {
			return postsearch.Forum{}, postsearch.ErrForbidden
		}
		permissions := channelPermissions(guild.ID, guild.OwnerID, roles, *member, guildChannel.PermissionOverwrites(), a.Now())
		if !permissions.Has(discord.PermissionViewChannel, discord.PermissionReadMessageHistory) {
			return postsearch.Forum{}, postsearch.ErrForbidden
		}
	}
	forum := postsearch.Forum{ID: target.ChannelID, Name: guildChannel.Name(), AvailableTags: make([]postsearch.Tag, 0, len(tags))}
	for _, tag := range tags {
		forum.AvailableTags = append(forum.AvailableTags, postsearch.Tag{ID: uint64(tag.ID), Name: tag.Name})
	}
	return forum, nil
}

// ActiveThreads 返回全服活跃线程；调用方按 ParentID 过滤出目标论坛的帖子。
func (a *PostAPI) ActiveThreads(ctx context.Context, guildID uint64) ([]postsearch.Thread, error) {
	active, err := a.Rest.GetActiveGuildThreads(snowflake.ID(guildID), rest.WithCtx(ctx))
	if err != nil {
		return nil, postError(err)
	}
	threads := make([]postsearch.Thread, 0, len(active.Threads))
	for _, thread := range active.Threads {
		if converted, ok := convertThread(thread, guildID); ok {
			threads = append(threads, converted)
		}
	}
	return threads, nil
}

// ArchivedThreads 返回一页归档公开线程，游标为上一页中最旧的归档时间。
func (a *PostAPI) ArchivedThreads(ctx context.Context, channelID uint64, before time.Time) ([]postsearch.Thread, bool, error) {
	page, err := a.Rest.GetPublicArchivedThreads(snowflake.ID(channelID), before, ArchivePageSize, rest.WithCtx(ctx))
	if err != nil {
		return nil, false, postError(err)
	}
	threads := make([]postsearch.Thread, 0, len(page.Threads))
	for _, thread := range page.Threads {
		if converted, ok := convertThread(thread, a.GuildID); ok {
			threads = append(threads, converted)
		}
	}
	return threads, page.HasMore, nil
}

// convertThread 只接受属于本服务器的帖子，并复制出过滤所需的字段。
func convertThread(thread discord.GuildThread, guildID uint64) (postsearch.Thread, bool) {
	if thread.ID() == 0 || thread.GuildID() != snowflake.ID(guildID) {
		return postsearch.Thread{}, false
	}
	parent := thread.ParentID()
	if parent == nil {
		return postsearch.Thread{}, false
	}
	created := thread.ThreadMetadata.ArchiveTimestamp
	if !thread.ThreadMetadata.CreateTimestamp.IsZero() {
		created = thread.ThreadMetadata.CreateTimestamp
	}
	if created.IsZero() {
		created = thread.ID().Time()
	}
	return postsearch.Thread{
		ID:           uint64(thread.ID()),
		ParentID:     uint64(*parent),
		OwnerID:      uint64(thread.OwnerID),
		Name:         thread.Name(),
		AppliedTags:  copyTags(thread.AppliedTags),
		CreatedAt:    created,
		ArchivedAt:   thread.ThreadMetadata.ArchiveTimestamp,
		MessageCount: thread.TotalMessageSent,
		MemberCount:  thread.MemberCount,
		Locked:       thread.ThreadMetadata.Locked,
		Archived:     thread.ThreadMetadata.Archived,
	}, true
}

func copyTags(ids []snowflake.ID) []uint64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]uint64, len(ids))
	for i, id := range ids {
		out[i] = uint64(id)
	}
	return out
}

// postError 把 REST 错误映射为可展示的安全错误，不回显请求 URL 或响应体。
func postError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var apiErr *rest.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		switch apiErr.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return postsearch.ErrForbidden
		}
	}
	return postsearch.ErrUnavailable
}
