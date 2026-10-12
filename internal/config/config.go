package config

import (
	"fmt"
	"strconv"
	"strings"
)

type Config struct {
	Token         string
	ApplicationID uint64
	GuildID       uint64
}

func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	cfg.Token = strings.TrimSpace(getenv("DISCORD_TOKEN"))
	if cfg.Token == "" {
		return cfg, fmt.Errorf("必须设置 DISCORD_TOKEN（不要将 Token 发到对话或日志）")
	}
	// Discord Bot Token 固定为三段式（base64 应用 ID . base64 时间戳 . 签名）。
	// 常见的误配是填入 OAuth2 的客户端密钥，这里提前拦截并给出可执行的提示。
	if parts := strings.Split(cfg.Token, "."); len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return cfg, fmt.Errorf("DISCORD_TOKEN 不是三段式的 Bot Token（形如 xxx.yyy.zzz）；请到 Discord Developer Portal 的 Bot 页面获取，OAuth2 客户端密钥不能用于登录 Bot")
	}
	for name, dest := range map[string]*uint64{"DISCORD_APPLICATION_ID": &cfg.ApplicationID, "DISCORD_GUILD_ID": &cfg.GuildID} {
		raw := strings.TrimSpace(getenv(name))
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			return Config{}, fmt.Errorf("%s 必须是有效的非零 Discord ID", name)
		}
		*dest = id
	}
	return cfg, nil
}
