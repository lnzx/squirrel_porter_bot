package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"github.com/lnzx/squirrel_porter_bot/cmd"
	"github.com/lnzx/squirrel_porter_bot/tg"
	"github.com/urfave/cli/v3"
)

var Version = "0.1.0"

func init() {
	cli.VersionPrinter = func(cmd *cli.Command) {
		_, _ = fmt.Fprintf(cmd.Root().Writer, "%v\n", cmd.Version)
	}
}

func main() {
	// 客户端生命周期 context：与 Ctrl+C 解耦，保证「正在进行的一批转发」不会被中途撕裂。
	// 只有第二次 Ctrl+C（或程序正常结束）才取消它。
	clientCtx, cancelClient := context.WithCancel(context.Background())
	defer cancelClient()

	// 优雅停止 context：第一次 Ctrl+C 取消它，长任务在「批 / 单元」边界检测到后干净收尾（如 clone 打印续传点）。
	gracefulCtx, cancelGraceful := context.WithCancel(context.Background())
	defer cancelGraceful()

	// 两段式信号：第一次 = 优雅停止，第二次 = 强制退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		_, _ = fmt.Fprintln(os.Stderr, "\n收到中断，正在收尾当前批次…（再按一次 Ctrl+C 强制退出）")
		cancelGraceful()
		<-sigCh
		_, _ = fmt.Fprintln(os.Stderr, "\n强制退出")
		cancelClient()
	}()

	root := &cli.Command{
		Name:            "squirrel_porter_bot",
		Usage:           "tg command-line bot",
		Version:         Version,
		HideVersion:     false,
		HideHelpCommand: true,
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:    "api-id",
				Aliases: []string{"i", "id"},
				Sources: cli.EnvVars("SPB_API_ID"),
			},
			&cli.StringFlag{
				Name:    "api-hash",
				Aliases: []string{"hash"},
				Sources: cli.EnvVars("SPB_API_HASH"),
			},
			&cli.StringFlag{
				Name:    "bot-token",
				Aliases: []string{"t", "token"},
				Sources: cli.EnvVars("SPB_BOT_TOKEN"),
			},
			&cli.StringFlag{
				Name:    "channel",
				Aliases: []string{"c"},
				Sources: cli.EnvVars("SPB_CHANNEL"),
			},
			&cli.StringFlag{
				Name:    "login",
				Value:   "bot",
				Usage:   "login identity: bot | user (user is required to read channel history, e.g. dedup)",
				Sources: cli.EnvVars("SPB_LOGIN"),
			},
			&cli.StringFlag{
				Name:    "phone",
				Usage:   "phone number for user login (e.g. +8613800138000)",
				Sources: cli.EnvVars("SPB_PHONE"),
			},
		},
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
			apiId := c.Int("api-id")
			apiHash := c.String("api-hash")

			if apiId <= 0 {
				return ctx, fmt.Errorf("missing api-id")
			}
			if apiHash == "" {
				return ctx, fmt.Errorf("missing api-hash")
			}

			var client *tg.Client
			var err error
			if c.String("login") == "user" {
				// 用户账号登录：能读取频道历史(bot 无此权限)
				phone := c.String("phone")
				if phone == "" {
					return ctx, fmt.Errorf("missing phone (required for --login user)")
				}
				// 用 clientCtx 而非信号 ctx：Ctrl+C 不会撕裂正在进行的 RPC
				client, err = tg.NewUserClient(clientCtx, apiId, apiHash, phone)
			} else {
				botToken := c.String("bot-token")
				if botToken == "" {
					return ctx, fmt.Errorf("missing bot-token")
				}
				client, err = tg.NewClient(clientCtx, apiId, apiHash, botToken)
			}
			if err != nil {
				return ctx, err
			}
			log.Println("new client ok")
			// 返回的 ctx 派生自 gracefulCtx：子命令据此在批边界优雅停止；同时携带已认证 client
			return tg.WithContext(ctx, client), nil
		},
		After: func(ctx context.Context, c *cli.Command) error {
			// 命令结束（无论成功或失败）优雅关闭客户端
			if client := tg.FromContext(ctx); client != nil {
				client.Stop()
			}
			return nil
		},
		Commands: []*cli.Command{
			cmd.FileCmd,
			cmd.DedupCmd,
			cmd.CloneCmd,
		},
	}

	if err := root.Run(gracefulCtx, os.Args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
