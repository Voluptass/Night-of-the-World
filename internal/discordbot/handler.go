package discordbot

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Voluptass/Night-of-the-World/internal/postsearch"
	"github.com/Voluptass/Night-of-the-World/internal/purge"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"
)

func Commands() []discord.ApplicationCommandCreate {
	permissions := omit.NewPtr(discord.PermissionManageMessages)
	return []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{
			Name: "purge-user", Description: "预览并确认清理一个帖子中指定账号的回复（不可恢复）",
			DefaultMemberPermissions: permissions,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "account", Description: "用户 ID 或 @用户提及；不接受昵称", Required: true},
				discord.ApplicationCommandOptionString{Name: "post", Description: "本服务器的帖子/公开线程链接或 ID", Required: true},
			},
		},
		discord.SlashCommandCreate{
			Name: "purge-status", Description: "查询自己的清理任务，或重新显示确认/取消按钮",
			DefaultMemberPermissions: permissions,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{Name: "task", Description: "预览时返回的任务 ID", Required: true},
			},
		},
		discord.SlashCommandCreate{
			Name: "search-posts", Description: "在本服务器的论坛/媒体频道中按标题关键词和标签筛选帖子",
			DefaultMemberPermissions: permissions,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionChannel{Name: "forum", Description: "要搜索的论坛或媒体频道", Required: true,
					ChannelTypes: []discord.ChannelType{discord.ChannelTypeGuildForum, discord.ChannelTypeGuildMedia}},
				discord.ApplicationCommandOptionString{Name: "keyword", Description: "匹配帖子标题的关键词，多个词用空格分隔（全部包含才命中）", Required: true},
				discord.ApplicationCommandOptionString{Name: "tags", Description: "按标签筛选；可输多个，用逗号分隔（需全部包含）", Autocomplete: true},
			},
		},
	}
}

// Register only upserts these named commands in the configured guild, never bulk-overwrites commands.
func Register(ctx context.Context, api rest.Rest, applicationID, guildID uint64) error {
	for _, command := range Commands() {
		if _, err := api.CreateGuildCommand(snowflake.ID(applicationID), snowflake.ID(guildID), command, rest.WithCtx(ctx)); err != nil {
			return apiError(err, false)
		}
	}
	return nil
}

type Handler struct {
	api           rest.Rest
	manager       *purge.Manager
	engine        postsearch.Engine
	store         *postsearch.Store
	search        *searchCache
	guildID       uint64
	applicationID snowflake.ID
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	wg            sync.WaitGroup
}

func NewHandler(parent context.Context, api rest.Rest, manager *purge.Manager, searchAPI postsearch.API, applicationID, guildID uint64) *Handler {
	ctx, cancel := context.WithCancel(parent)
	return &Handler{
		api: api, manager: manager,
		engine:        postsearch.Engine{API: searchAPI},
		store:         postsearch.NewStore(time.Now),
		search:        newSearchCache(),
		applicationID: snowflake.ID(applicationID), guildID: guildID, ctx: ctx, cancel: cancel,
	}
}

func (h *Handler) launch(work func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil {
		return
	}
	h.wg.Add(1)
	go func() { defer h.wg.Done(); work() }()
}

func (h *Handler) OnCommand(e *events.ApplicationCommandInteractionCreate) {
	if e.Data.Type() != discord.ApplicationCommandTypeSlash {
		return
	}
	data := e.SlashCommandInteractionData()
	switch data.CommandName() {
	case "purge-user", "purge-status", "search-posts":
	default:
		return
	}
	h.launch(func() {
		ackCtx, cancel := context.WithTimeout(h.ctx, 2500*time.Millisecond)
		err := e.DeferCreateMessage(true, rest.WithCtx(ackCtx))
		cancel()
		if err != nil {
			slog.Warn("未能确认 Discord 交互", "interaction", e.ID())
			return
		}
		if e.GuildID() == nil || uint64(*e.GuildID()) != h.guildID {
			h.text(e.Token(), "此命令只能在配置的服务器中使用。")
			return
		}
		if data.CommandName() == "search-posts" {
			h.searchPosts(e, data)
			return
		}
		var job purge.Job
		if data.CommandName() == "purge-user" {
			account, parseErr := ParseAccount(data.String("account"))
			if parseErr != nil {
				h.text(e.Token(), parseErr.Error())
				return
			}
			thread, parseErr := ParsePost(data.String("post"), h.guildID)
			if parseErr != nil {
				h.text(e.Token(), parseErr.Error())
				return
			}
			job, err = h.manager.Start(purge.Target{GuildID: h.guildID, ThreadID: thread, AccountID: account, ActorID: uint64(e.User().ID)})
		} else {
			ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
			job, err = h.manager.Status(ctx, data.String("task"), uint64(e.User().ID), h.guildID)
			cancel()
		}
		if err != nil {
			h.text(e.Token(), purge.SafeError(err))
			return
		}
		if !h.update(e.Token(), Render(job)) {
			return
		}
		if data.CommandName() == "purge-user" {
			h.watch(e.Token(), job.ID)
		}
	})
}

func (h *Handler) OnComponent(e *events.ComponentInteractionCreate) {
	customID := e.Data.CustomID()
	switch {
	case strings.HasPrefix(customID, "purge:v1:"):
		h.onPurgeComponent(e, customID)
	case strings.HasPrefix(customID, searchPrefix):
		h.onSearchComponent(e, customID)
	}
}

// onSearchComponent 处理检索结果的翻页按钮。
func (h *Handler) onSearchComponent(e *events.ComponentInteractionCreate, customID string) {
	parts := strings.Split(strings.TrimPrefix(customID, searchPrefix), ":")
	if len(parts) != 2 {
		return
	}
	page, err := strconv.Atoi(parts[0])
	if err != nil || page < 0 || len(parts[1]) != postsearch.SearchIDLength {
		return
	}
	h.launch(func() {
		ackCtx, cancel := context.WithTimeout(h.ctx, 2500*time.Millisecond)
		err := e.DeferCreateMessage(true, rest.WithCtx(ackCtx))
		cancel()
		if err != nil {
			slog.Warn("未能确认 Discord 按钮交互", "interaction", e.ID())
			return
		}
		if e.GuildID() == nil || uint64(*e.GuildID()) != h.guildID {
			h.text(e.Token(), "服务器范围不匹配。")
			return
		}
		h.searchPage(e, parts[1], page)
	})
}

func (h *Handler) onPurgeComponent(e *events.ComponentInteractionCreate, customID string) {
	h.launch(func() {
		ackCtx, cancel := context.WithTimeout(h.ctx, 2500*time.Millisecond)
		err := e.DeferCreateMessage(true, rest.WithCtx(ackCtx))
		cancel()
		if err != nil {
			slog.Warn("未能确认 Discord 按钮交互", "interaction", e.ID())
			return
		}
		if e.GuildID() == nil || uint64(*e.GuildID()) != h.guildID {
			h.text(e.Token(), "服务器范围不匹配。")
			return
		}
		parts := strings.Split(customID, ":")
		if len(parts) != 4 || len(parts[3]) != 32 {
			h.text(e.Token(), "无效的任务按钮。")
			return
		}
		if _, err := hex.DecodeString(parts[3]); err != nil {
			h.text(e.Token(), "无效的任务按钮。")
			return
		}
		var job purge.Job
		switch parts[2] {
		case "confirm":
			ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
			job, err = h.manager.Confirm(ctx, parts[3], uint64(e.User().ID), h.guildID)
			cancel()
		case "cancel":
			job, err = h.manager.Cancel(parts[3], uint64(e.User().ID), h.guildID)
		default:
			h.text(e.Token(), "无效的任务操作。")
			return
		}
		if err != nil {
			h.text(e.Token(), purge.SafeError(err))
			return
		}
		if !h.update(e.Token(), Render(job)) {
			return
		}
		if parts[2] == "confirm" {
			h.watch(e.Token(), job.ID)
		}
	})
}

func (h *Handler) watch(token, id string) {
	ctx, cancel := context.WithTimeout(h.ctx, 14*time.Minute)
	defer cancel()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			job, ok := h.manager.Snapshot(id)
			if !ok {
				return
			}
			if !h.update(token, Render(job)) {
				return
			}
			if job.State != purge.Scanning && job.State != purge.Deleting {
				return
			}
		}
	}
}

func (h *Handler) text(token, content string) {
	h.respond(token, content, nil)
}

// respond 用同一条路径更新私密回复：文本 + 可选组件，并统一禁用提及。
func (h *Handler) respond(token, content string, components []discord.LayoutComponent) bool {
	if components == nil {
		components = []discord.LayoutComponent{}
	}
	return h.update(token, discord.MessageUpdate{Content: &content, Components: &components, AllowedMentions: noMentions()})
}

func (h *Handler) update(token string, message discord.MessageUpdate) bool {
	ctx, cancel := context.WithTimeout(h.ctx, 20*time.Second)
	defer cancel()
	if _, err := h.api.UpdateInteractionResponse(h.applicationID, token, message, rest.WithCtx(ctx)); err != nil {
		// Do not log the raw error: webhook request URLs contain the interaction token.
		slog.Warn("更新私密回复失败；任务状态仍可通过 purge-status 查询")
		return false
	}
	return true
}

func noMentions() *discord.AllowedMentions {
	return &discord.AllowedMentions{Parse: []discord.AllowedMentionType{}, Roles: []snowflake.ID{}, Users: []snowflake.ID{}}
}

func Render(job purge.Job) discord.MessageUpdate {
	states := map[purge.State]string{
		purge.Scanning: "正在扫描（尚未删除）", purge.Ready: "等待确认（尚未删除）", purge.Deleting: "正在删除",
		purge.Completed: "处理完成", purge.Cancelled: "已取消", purge.Failed: "已停止：未全部完成", purge.Expired: "确认已过期（未删除）",
	}
	content := fmt.Sprintf("**%s**\n任务：`%s`\n服务器：`%d`\n帖子：https://discord.com/channels/%d/%d\n目标账号：`%d`\n扫描：%d；匹配可清理：%d；保留首帖/系统消息：%d\n预览时可批量处理：%d；逐条处理（较旧或接近两周）：%d\n删除请求成功处理：%d；明确已不存在：%d；剩余/未确认：%d；结果不确定：%d\n", states[job.State], job.ID, job.Target.GuildID, job.Target.GuildID, job.Target.ThreadID, job.Target.AccountID, job.Stats.Scanned, job.Stats.Matched, job.Stats.Protected, job.Stats.Recent, job.Stats.Old, job.Stats.Processed, job.Stats.Missing, job.Stats.Remaining(), job.Stats.Uncertain)
	content += "批量成功包含可能已被他人删除的消息，不代表精确新增删除数。\n只处理本次预览快照，后续新消息不在范围内。\n"
	components := []discord.LayoutComponent{}
	if job.Error != "" {
		content += "说明：" + job.Error + "\n"
	}
	if job.State == purge.Ready {
		content += fmt.Sprintf("**删除不可恢复。确认截止：<t:%d:R>。**\n", job.ExpiresAt.Unix())
		components = append(components, discord.NewActionRow(discord.NewDangerButton("确认删除（不可恢复）", "purge:v1:confirm:"+job.ID), discord.NewSecondaryButton("取消", "purge:v1:cancel:"+job.ID)))
	} else if job.State == purge.Scanning || job.State == purge.Deleting {
		if job.CancelRequested {
			content += "正在停止后续请求；已发送的请求可能仍会完成。\n"
		} else {
			components = append(components, discord.NewActionRow(discord.NewSecondaryButton("停止任务", "purge:v1:cancel:"+job.ID)))
		}
	}
	content += "使用 `/purge-status task:" + job.ID + "` 查询。任务结束后记录保留 30 分钟；Bot 重启后需重新预览。"
	return discord.MessageUpdate{Content: &content, Components: &components, AllowedMentions: noMentions()}
}

func (h *Handler) Close(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	h.cancel()
	h.mu.Unlock()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
