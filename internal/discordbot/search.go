package discordbot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Voluptass/Night-of-the-World/internal/postsearch"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
)

const (
	searchPrefix = "search:v1:"
	maxTagRows   = 5
	// maxContentRunes 给 Discord 的 2000 字符上限留出余量，超出的条目留到下一页。
	maxContentRunes = 1800
)

// searchCache 为自动补全临时保存论坛标签，只存内存且会自动过期。
type searchCache struct {
	requests map[uint64]searchRequest
	max      int
}

type searchRequest struct {
	tags    []postsearch.Tag
	guild   uint64
	fetched time.Time
}

func newSearchCache() *searchCache {
	return &searchCache{requests: make(map[uint64]searchRequest), max: 512}
}

// remember 缓存论坛标签，供自动补全使用。
func (s *searchCache) remember(guild, forum uint64, tags []postsearch.Tag) {
	if len(s.requests) >= s.max {
		s.requests = make(map[uint64]searchRequest, s.max)
	}
	s.requests[forum] = searchRequest{tags: tags, guild: guild, fetched: time.Now()}
}

// tags 读取未过期的论坛标签。
func (s *searchCache) tags(guild, forum uint64) ([]postsearch.Tag, bool) {
	item, ok := s.requests[forum]
	if !ok || item.guild != guild || time.Since(item.fetched) > postsearch.ResultTTL {
		return nil, false
	}
	return item.tags, true
}

// Autocomplete 为 tags 选项提供当前论坛的标签候选。
func (h *Handler) Autocomplete(e *events.AutocompleteInteractionCreate) {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
	defer cancel()
	choices := h.tagChoices(e)
	if err := e.AutocompleteResult(choices, rest.WithCtx(ctx)); err != nil {
		slog.Warn("自动补全回复失败", "interaction", e.ID())
	}
}

func (h *Handler) tagChoices(e *events.AutocompleteInteractionCreate) []discord.AutocompleteChoice {
	guildID := e.GuildID()
	if guildID == nil || uint64(*guildID) != h.guildID {
		return []discord.AutocompleteChoice{}
	}
	forumID := uint64(e.Data.Options["forum"].Snowflake())
	if forumID == 0 {
		return []discord.AutocompleteChoice{}
	}
	tags, ok := h.search.tags(h.guildID, forumID)
	if !ok {
		return []discord.AutocompleteChoice{}
	}
	typed := strings.ToLower(strings.TrimSpace(e.Data.Options["tags"].String()))
	choices := make([]discord.AutocompleteChoice, 0, 25)
	for _, tag := range tags {
		if len(choices) >= 25 {
			break
		}
		if typed != "" && !strings.Contains(strings.ToLower(tag.Name), typed) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{Name: tag.Name, Value: strconv.FormatUint(tag.ID, 10)})
	}
	return choices
}

// searchPosts 执行一次检索并回复结果。
func (h *Handler) searchPosts(e *events.ApplicationCommandInteractionCreate, data discord.SlashCommandInteractionData) {
	channelID := uint64(data.Channel("forum").ID)
	terms, err := postsearch.ParseKeyword(data.String("keyword"))
	if err != nil {
		h.text(e.Token(), postsearch.SafeError(err))
		return
	}
	tagIDs, err := parseTagIDs(data.String("tags"))
	if err != nil {
		h.text(e.Token(), postsearch.SafeError(err))
		return
	}
	target := postsearch.Target{GuildID: h.guildID, ChannelID: channelID, ActorID: uint64(e.User().ID)}

	release, err := h.store.Begin()
	if err != nil {
		h.text(e.Token(), postsearch.SafeError(err))
		return
	}
	go h.runSearch(e.Token(), target, terms, tagIDs, release)
}

// runSearch 在后台完成枚举，避免占用交互响应窗口。
func (h *Handler) runSearch(token string, target postsearch.Target, terms []string, tagIDs []uint64, release func()) {
	defer release()
	ctx, cancel := postsearch.Context(h.ctx)
	defer cancel()
	result, hits, err := h.engine.Search(ctx, target, terms, tagIDs)
	if err != nil {
		h.text(token, postsearch.SafeError(err))
		return
	}
	h.search.remember(h.guildID, target.ChannelID, result.Forum.AvailableTags)
	id, err := h.store.Save(result, hits, target.ActorID, h.guildID)
	if err != nil {
		h.text(token, postsearch.SafeError(err))
		return
	}
	if len(hits) == 0 {
		h.respond(token, renderEmptySearch(result), nil)
		return
	}
	content, components := renderSearch(result, hits, 0, id)
	h.respond(token, content, components)
}

// searchPage 处理翻页按钮。
func (h *Handler) searchPage(e *events.ComponentInteractionCreate, id string, page int) {
	result, hits, current, err := h.store.SetPage(id, uint64(e.User().ID), h.guildID, page)
	if err != nil {
		h.text(e.Token(), postsearch.SafeError(err))
		return
	}
	content, components := renderSearch(result, hits, current, id)
	h.respond(e.Token(), content, components)
}

// renderSearch 渲染结果第一页与翻页按钮。
func renderSearch(result postsearch.Result, hits []postsearch.Hit, page int, id string) (string, []discord.LayoutComponent) {
	results, current, pages := result.Page(hits, page)
	var builder strings.Builder
	fmt.Fprintf(&builder, "**找到 %d 个匹配帖子**（第 %d/%d 页）\n", len(hits), current+1, pages)
	builder.WriteString(describeSearch(result))
	builder.WriteString("\n")
	for _, hit := range results {
		line := fmt.Sprintf("• [%s](https://discord.com/channels/%d/%d)", sanitizeTitle(hit.Name), result.Forum.ID, hit.ThreadID)
		if len(hit.MatchedTags) > 0 {
			line += " · " + tagNames(result, hit.MatchedTags)
		}
		line += "\n"
		// 标题和标签数量不可控，逐条检查预算，避免整条消息超过 Discord 上限而发送失败。
		if utf8.RuneCountInString(builder.String())+utf8.RuneCountInString(line) > maxContentRunes {
			builder.WriteString("… 本页部分条目因长度限制未显示，请翻页查看。\n")
			break
		}
		builder.WriteString(line)
	}
	if result.Truncated {
		builder.WriteString("\n扫描已达上限，结果可能不完整；请补充标签筛选后重试。\n")
	}
	builder.WriteString("\n结果保留 " + postsearch.ResultTTL.String() + "，Bot 重启后失效。")

	components := []discord.LayoutComponent{}
	if pages > 1 {
		previous := discord.NewSecondaryButton("上一页", fmt.Sprintf("%s%d:%s", searchPrefix, current-1, id))
		previous.Disabled = current == 0
		next := discord.NewSecondaryButton("下一页", fmt.Sprintf("%s%d:%s", searchPrefix, current+1, id))
		next.Disabled = current >= pages-1
		components = append(components, discord.NewActionRow(previous, next))
	}
	return builder.String(), components
}

// renderEmptySearch 渲染没有命中时的说明，并列出可用标签。
func renderEmptySearch(result postsearch.Result) string {
	var builder strings.Builder
	builder.WriteString("**没有匹配的帖子**\n")
	builder.WriteString(describeSearch(result))
	builder.WriteString("\n")
	builder.WriteString(renderTagList(result.Forum))
	builder.WriteString("\n注意：只按帖子标题匹配，不搜索帖子正文。")
	if result.Truncated {
		builder.WriteString("\n扫描已达上限，结果可能不完整。")
	}
	return builder.String()
}

// describeSearch 汇总本次检索的条件与扫描量。
func describeSearch(result postsearch.Result) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "论坛：<#%d>\n", result.Forum.ID)
	fmt.Fprintf(&builder, "关键词：`%s`\n", strings.Join(result.KeywordTerms, " "))
	fmt.Fprintf(&builder, "标签筛选：%s\n", describeTags(result))
	fmt.Fprintf(&builder, "已扫描帖子：%d", result.Scanned)
	return builder.String()
}

// describeTags 输出人类可读的标签筛选条件。
func describeTags(result postsearch.Result) string {
	if len(result.TagIDs) == 0 {
		return "（未筛选）"
	}
	return tagNames(result, result.TagIDs)
}

// tagNames 把标签 ID 转为名称，未知 ID 原样展示。
func tagNames(result postsearch.Result, ids []uint64) string {
	names := make(map[uint64]string, len(result.Forum.AvailableTags))
	for _, tag := range result.Forum.AvailableTags {
		names[tag.ID] = tag.Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := names[id]; ok {
			out = append(out, name)
			continue
		}
		out = append(out, fmt.Sprintf("`%d`", id))
	}
	return strings.Join(out, "、")
}

// sanitizeTitle 防止帖子标题中的 Markdown 破坏排版。
func sanitizeTitle(name string) string {
	name = strings.NewReplacer("[", "(", "]", ")", "`", "'", "\n", " ", "\r", " ").Replace(name)
	name = strings.TrimSpace(name)
	if name == "" {
		return "（无标题）"
	}
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80]) + "…"
	}
	return name
}

// renderTagList 列出论坛的可用标签，便于用户改用标签筛选。
func renderTagList(forum postsearch.Forum) string {
	if len(forum.AvailableTags) == 0 {
		return "该论坛没有可用标签。"
	}
	out := make([]string, 0, len(forum.AvailableTags))
	for _, tag := range forum.AvailableTags {
		out = append(out, fmt.Sprintf("%s（`%d`）", sanitizeTitle(tag.Name), tag.ID))
	}
	return "可用标签：" + strings.Join(out, "、")
}

// parseTagIDs 解析以逗号或空格分隔的标签 ID。空段会被视为输入错误，避免静默忽略条件。
func parseTagIDs(raw string) ([]uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	chunks := strings.Split(raw, ",")
	if len(chunks) > maxTagRows {
		return nil, postsearch.ErrInvalidQuery
	}
	fields := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			return nil, postsearch.ErrInvalidQuery
		}
		fields = append(fields, strings.Fields(chunk)...)
	}
	if len(fields) == 0 || len(fields) > maxTagRows {
		return nil, postsearch.ErrInvalidQuery
	}
	ids := make([]uint64, 0, len(fields))
	for _, field := range fields {
		id, err := parseID(field)
		if err != nil {
			return nil, postsearch.ErrInvalidQuery
		}
		ids = append(ids, id)
	}
	return ids, nil
}
