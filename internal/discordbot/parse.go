package discordbot

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

func parseID(raw string) (uint64, error) {
	if len(raw) == 0 || len(raw) > 20 {
		return 0, errors.New("无效的 Discord ID")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("无效的 Discord ID")
		}
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("无效的 Discord ID")
	}
	return id, nil
}

func ParseAccount(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "<@") && strings.HasSuffix(raw, ">") {
		raw = strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(raw, "<@"), ">"), "!")
	}
	id, err := parseID(raw)
	if err != nil {
		return 0, errors.New("account 必须填写用户 ID 或 @用户提及，不能填写昵称")
	}
	return id, nil
}

func ParsePost(raw string, guildID uint64) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if id, err := parseID(raw); err == nil {
		return id, nil
	}
	invalid := errors.New("post 必须是本服务器的帖子/线程 ID，或 https://discord.com/channels/服务器ID/线程ID 链接")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return 0, invalid
	}
	switch u.Host {
	case "discord.com", "canary.discord.com", "ptb.discord.com", "discordapp.com":
	default:
		return 0, invalid
	}
	parts := strings.Split(strings.TrimSuffix(u.EscapedPath(), "/"), "/")
	if (len(parts) != 4 && len(parts) != 5) || parts[0] != "" || parts[1] != "channels" {
		return 0, invalid
	}
	guild, err := parseID(parts[2])
	if err != nil || guild != guildID {
		return 0, invalid
	}
	thread, err := parseID(parts[3])
	if err != nil {
		return 0, invalid
	}
	if len(parts) == 5 {
		if _, err := parseID(parts[4]); err != nil {
			return 0, invalid
		}
	}
	return thread, nil
}
