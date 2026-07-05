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
				client, err = tg.NewUserClient(ctx, apiId, apiHash, phone)
			} else {
				botToken := c.String("bot-token")
				if botToken == "" {
					return ctx, fmt.Errorf("missing bot-token")
				}
				client, err = tg.NewClient(ctx, apiId, apiHash, botToken)
			}
			if err != nil {
				return ctx, err
			}
			log.Println("new client ok")
			// CreateContext 返回的 *ext.Context 建议全局复用，不要每个子命令重新创建
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := root.Run(ctx, os.Args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
