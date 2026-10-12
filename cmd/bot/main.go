package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Voluptass/Night-of-the-World/internal/config"
	"github.com/Voluptass/Night-of-the-World/internal/discordbot"
	"github.com/Voluptass/Night-of-the-World/internal/purge"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Bot 已停止", "error", err)
		os.Exit(1)
	}
}

func run() error {
	mode := "run"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "--help" || mode == "-h" {
		fmt.Println("用法：bot [run | register-commands]\n需要 DISCORD_TOKEN、DISCORD_APPLICATION_ID、DISCORD_GUILD_ID。\nregister-commands 会创建/更新指定服务器中的 purge-user 和 purge-status 命令；run 不会注册命令。")
		return nil
	}
	if len(os.Args) > 2 || (mode != "run" && mode != "register-commands") {
		return errors.New("用法错误，请运行 bot --help")
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// SDK diagnostics may include interaction-token URLs or raw payloads. Keep application logs metadata-only.
	sdkLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := disgo.New(cfg.Token, bot.WithLogger(sdkLogger), bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds)))
	if err != nil {
		// SDK 错误只包含 Token 的格式/网络问题，不回显 Token 本身，便于定位配置错误。
		return fmt.Errorf("初始化 Discord SDK 失败，请检查 Bot Token 与网络（不要分享 Token）：%w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client.Close(closeCtx)
	}()
	if uint64(client.ApplicationID) != cfg.ApplicationID {
		return errors.New("DISCORD_APPLICATION_ID 与 Bot Token 所属应用不匹配")
	}
	if mode == "register-commands" {
		registerCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := discordbot.Register(registerCtx, client.Rest, cfg.ApplicationID, cfg.GuildID); err != nil {
			return fmt.Errorf("注册指定 guild 的命令失败：%s", purge.SafeError(err))
		}
		slog.Info("已创建/更新命令（未删除其它命令）", "guild", cfg.GuildID)
		return nil
	}
	api := &discordbot.API{Rest: client.Rest, GuildID: cfg.GuildID, BotID: func() uint64 { return uint64(client.ID()) }, Now: time.Now}
	manager := purge.NewManager(ctx, api, time.Now)
	// 帖子检索只需读取权限；复用同一份 REST 客户端和服务器范围。
	postAPI := &discordbot.PostAPI{Rest: client.Rest, GuildID: cfg.GuildID, BotID: func() uint64 { return uint64(client.ID()) }, Now: time.Now}
	handler := discordbot.NewHandler(ctx, client.Rest, manager, postAPI, cfg.ApplicationID, cfg.GuildID)
	defer func() {
		stop()
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := handler.Close(closeCtx); err != nil {
			slog.Warn("交互处理器退出等待超时")
		}
		if err := manager.Close(closeCtx); err != nil {
			slog.Warn("任务退出等待超时；重启后不会自动续删")
		}
	}()
	client.AddEventListeners(bot.NewListenerFunc(handler.OnCommand), bot.NewListenerFunc(handler.OnComponent), bot.NewListenerFunc(handler.Autocomplete))
	if err := client.OpenGateway(ctx); err != nil {
		return errors.New("连接 Discord Gateway 失败，请检查 Token、网络和 Bot 配置")
	}
	slog.Info("Bot 已启动；不会自动注册命令", "guild", cfg.GuildID)
	<-ctx.Done()
	slog.Info("正在停止 Bot 和后台任务")
	return nil
}
