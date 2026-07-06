package cmd

import (
	"context"
	"errors"

	"github.com/lnzx/squirrel_porter_bot/tg"
	"github.com/urfave/cli/v3"
)

// CloneCmd 把一个频道的历史消息克隆到另一个频道（保留相册、caption、发布顺序）。
// 需 --login user 才能读取源频道历史。
var CloneCmd = &cli.Command{
	Name:  "clone",
	Usage: "clone all messages from one channel to another (needs --login user)",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "from",
			Aliases:  []string{"f"},
			Usage:    "source channel username (without @)",
			Required: true,
		},
		&cli.StringFlag{
			Name:  "to",
			Usage: "target channel username (without @); defaults to global --channel",
		},
		&cli.IntFlag{
			Name:  "min-id",
			Usage: "resume: only clone source messages with ID greater than this (0 = all)",
		},
		&cli.IntFlag{
			Name:    "batch",
			Aliases: []string{"b"},
			Value:   20,
			Usage:   "messages per forward call (larger = faster but more server load; 1-100)",
		},
	},
	Action: func(ctx context.Context, c *cli.Command) error {
		from := c.String("from")
		to := c.String("to")
		if to == "" {
			to = c.String("channel") // 未指定 --to 时退回全局 -c
		}
		if to == "" {
			return errors.New("missing target channel, use --to <channel> or -c <channel>")
		}
		if from == to {
			return errors.New("source and target channel must differ")
		}

		minID := c.Int("min-id")
		batchSize := c.Int("batch")
		client := tg.FromContext(ctx)
		if client == nil {
			return errors.New("client not initialized (use --login user)")
		}
		return client.CloneChannel(from, to, minID, batchSize)
	},
}
