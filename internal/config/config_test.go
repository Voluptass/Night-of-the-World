package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	base := map[string]string{"DISCORD_TOKEN": "secret-test-token.abc.xyz", "DISCORD_APPLICATION_ID": "123", "DISCORD_GUILD_ID": "456"}
	cfg, err := Load(func(name string) string { return base[name] })
	if err != nil || cfg.ApplicationID != 123 || cfg.GuildID != 456 || cfg.Token != "secret-test-token.abc.xyz" {
		t.Fatalf("configuration did not load")
	}
	for _, name := range []string{"DISCORD_TOKEN", "DISCORD_APPLICATION_ID", "DISCORD_GUILD_ID"} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(func(key string) string {
				if key == name {
					return ""
				}
				return base[key]
			})
			if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), base["DISCORD_TOKEN"]) {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
	for _, id := range []string{"0", "-1", "not-a-number", "18446744073709551616"} {
		_, err := Load(func(name string) string {
			if name == "DISCORD_GUILD_ID" {
				return id
			}
			return base[name]
		})
		if err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
}

// TestLoadRejectsNonBotTokenShape 覆盖把 OAuth2 客户端密钥之类凭据误填为 Token 的常见情况。
func TestLoadRejectsNonBotTokenShape(t *testing.T) {
	cases := map[string]string{
		"客户端密钥单段": "abcDEF123defGHI456-xyz789",
		"只有两段":    "abc.def",
		"空段":      "abc..def",
		"空格分隔":    "abc def ghi",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(func(key string) string {
				switch key {
				case "DISCORD_TOKEN":
					return token
				case "DISCORD_APPLICATION_ID", "DISCORD_GUILD_ID":
					return "123"
				}
				return ""
			})
			if err == nil {
				t.Fatalf("accepted non-bot-token %q", token)
			}
			// 错误信息不能回显 Token 本身。
			if strings.Contains(err.Error(), token) {
				t.Fatalf("error leaks token: %v", err)
			}
		})
	}
}
