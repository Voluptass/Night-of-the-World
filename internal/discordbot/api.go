package discordbot

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Voluptass/Night-of-the-World/internal/purge"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type API struct {
	Rest    rest.Rest
	GuildID uint64
	BotID   func() uint64
	Now     func() time.Time
}

var _ purge.API = (*API)(nil)

func (a *API) Authorize(ctx context.Context, target purge.Target) error {
	if target.GuildID != a.GuildID || target.ThreadID == 0 || target.ActorID == 0 || a.BotID() == 0 {
		return purge.ErrInvalidTarget
	}
	channel, err := a.Rest.GetChannel(snowflake.ID(target.ThreadID), rest.WithCtx(ctx))
	if err != nil {
		return apiError(err, false)
	}
	thread, ok := channel.(discord.GuildThread)
	if !ok || (thread.Type() != discord.ChannelTypeGuildPublicThread && thread.Type() != discord.ChannelTypeGuildNewsThread) || uint64(thread.GuildID()) != a.GuildID || thread.ID() != snowflake.ID(target.ThreadID) {
		return purge.ErrInvalidTarget
	}
	parent, err := a.Rest.GetChannel(*thread.ParentID(), rest.WithCtx(ctx))
	if err != nil {
		return apiError(err, false)
	}
	guildChannel, ok := parent.(discord.GuildChannel)
	if !ok || guildChannel.GuildID() != thread.GuildID() || parent.ID() != *thread.ParentID() {
		return purge.ErrInvalidTarget
	}
	switch parent.Type() {
	case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews, discord.ChannelTypeGuildForum, discord.ChannelTypeGuildMedia:
	default:
		return purge.ErrInvalidTarget
	}
	guild, err := a.Rest.GetGuild(thread.GuildID(), false, rest.WithCtx(ctx))
	if err != nil {
		return apiError(err, false)
	}
	if guild.ID != thread.GuildID() {
		return purge.ErrInvalidTarget
	}
	roles, err := a.Rest.GetRoles(thread.GuildID(), rest.WithCtx(ctx))
	if err != nil {
		return apiError(err, false)
	}
	for _, id := range []uint64{target.ActorID, a.BotID()} {
		member, err := a.Rest.GetMember(thread.GuildID(), snowflake.ID(id), rest.WithCtx(ctx))
		if err != nil {
			return apiError(err, false)
		}
		if uint64(member.User.ID) != id {
			return purge.ErrForbidden
		}
		permissions := channelPermissions(guild.ID, guild.OwnerID, roles, *member, guildChannel.PermissionOverwrites(), a.Now())
		if !permissions.Has(discord.PermissionViewChannel, discord.PermissionReadMessageHistory, discord.PermissionManageMessages) {
			return purge.ErrForbidden
		}
		// Deletion is allowed in archived threads; locked AND archived threads additionally require Manage Threads.
		if thread.ThreadMetadata.Archived && thread.ThreadMetadata.Locked && !permissions.Has(discord.PermissionManageThreads) {
			return purge.ErrManageThreadsRequired
		}
	}
	return nil
}

// Thread permissions inherit the parent. Member overwrites apply AFTER aggregate role overwrites.
// This uses fresh REST data instead of the SDK cache, which may not contain the invoking member.
func channelPermissions(guildID, ownerID snowflake.ID, roles []discord.Role, member discord.Member, overwrites discord.PermissionOverwrites, now time.Time) discord.Permissions {
	if member.User.ID == ownerID {
		return discord.PermissionsAll
	}
	memberRoles := make(map[snowflake.ID]bool, len(member.RoleIDs))
	for _, id := range member.RoleIDs {
		memberRoles[id] = true
	}
	var permissions discord.Permissions
	for _, role := range roles {
		if role.ID == guildID || memberRoles[role.ID] {
			permissions |= role.Permissions
		}
	}
	if permissions.Has(discord.PermissionAdministrator) {
		return discord.PermissionsAll
	}
	if overwrite, ok := overwrites.Role(guildID); ok {
		permissions = (permissions &^ overwrite.Deny) | overwrite.Allow
	}
	var allow, deny discord.Permissions
	for id := range memberRoles {
		if id == guildID {
			continue
		}
		if overwrite, ok := overwrites.Role(id); ok {
			allow |= overwrite.Allow
			deny |= overwrite.Deny
		}
	}
	permissions = (permissions &^ deny) | allow
	if overwrite, ok := overwrites.Member(member.User.ID); ok {
		permissions = (permissions &^ overwrite.Deny) | overwrite.Allow
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(now) {
		permissions &= discord.PermissionViewChannel | discord.PermissionReadMessageHistory
	}
	return permissions
}

func (a *API) Messages(ctx context.Context, channelID, before uint64) ([]purge.Message, error) {
	messages, err := a.Rest.GetMessages(snowflake.ID(channelID), 0, snowflake.ID(before), 0, 100, rest.WithCtx(ctx))
	if err != nil {
		return nil, apiError(err, false)
	}
	result := make([]purge.Message, 0, len(messages))
	for _, message := range messages {
		result = append(result, purge.Message{
			ID: uint64(message.ID), ChannelID: uint64(message.ChannelID), AuthorID: uint64(message.Author.ID),
			CreatedAt: message.ID.Time(), Protected: message.Type.System() || !message.Type.Deleteable() || uint64(message.ID) == channelID,
		})
	}
	return result, nil
}

func (a *API) Delete(ctx context.Context, channelID, messageID uint64, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return apiError(a.Rest.DeleteMessage(snowflake.ID(channelID), snowflake.ID(messageID), rest.WithCtx(ctx), rest.WithReason(reason)), true)
}

func (a *API) BulkDelete(ctx context.Context, channelID uint64, ids []uint64, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	messages := make([]snowflake.ID, len(ids))
	for i, id := range ids {
		messages[i] = snowflake.ID(id)
	}
	return apiError(a.Rest.BulkDeleteMessages(snowflake.ID(channelID), messages, rest.WithCtx(ctx), rest.WithReason(reason)), true)
}

func apiError(err error, deleting bool) error {
	if err == nil {
		return nil
	}
	var apiErr *rest.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case rest.JSONErrorCodeUnknownMessage:
			return purge.ErrMessageGone
		case rest.JSONErrorCodeUnknownChannel:
			return purge.ErrInvalidTarget
		case 10007:
			return purge.ErrForbidden // Unknown member (e.g. permission revoked by leaving).
		case 50034:
			return purge.ErrTooOld
		}
		if apiErr.Response != nil {
			switch apiErr.Response.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return purge.ErrForbidden
			case http.StatusTooManyRequests:
				return purge.ErrUnavailable
			}
			if apiErr.Response.StatusCode >= 500 && deleting {
				return purge.ErrUncertain
			}
		}
		return purge.ErrUnavailable
	}
	if deleting {
		return purge.ErrUncertain
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return purge.ErrUnavailable
}
