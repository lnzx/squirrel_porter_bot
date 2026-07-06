package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/lnzx/squirrel_porter_bot/tg"
	"github.com/urfave/cli/v3"
)

var FileCmd = &cli.Command{
	Name:    "file",
	Aliases: []string{"f"},
	Usage:   "file upload/download",
	Commands: []*cli.Command{
		UploadCmd,
		DownloadCmd,
	},
}

var UploadCmd = &cli.Command{
	Name:      "upload",
	Aliases:   []string{"u", "up"},
	Usage:     "upload files",
	ArgsUsage: "<file_path...>  support: (*, ?, [])",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "caption",
			Aliases: []string{"c"},
			Usage:   "media caption",
		},
		&cli.BoolFlag{
			Name:    "separate",
			Aliases: []string{"s"},
			Usage:   "upload files one by one, each file is an independent message",
		},
		&cli.IntFlag{
			Name:    "threads",
			Aliases: []string{"t"},
			Value:   4,
			Usage:   "sets uploading goroutines limit",
		},
	},
	Action: func(ctx context.Context, c *cli.Command) error {
		if c.NArg() < 1 {
			return errors.New("upload requires at least one <file_path> argument")
		}
		targets := c.Args().Slice()
		for _, target := range targets {
			fmt.Println("cmd 原始参数:", target)
		}
		channel := c.String("channel")
		caption := c.String("caption")
		separate := c.Bool("separate")
		threads := c.Int("threads")
		client := tg.FromContext(ctx) // ← 这里拿到之前 Before 里存的 tg.Context
		if client == nil {
			return errors.New("client not initialized")
		}
		client.UploadThreads = threads
		return client.Upload(channel, targets, caption, separate)
	},
}

var DownloadCmd = &cli.Command{
	Name:      "download",
	Aliases:   []string{"d"},
	Usage:     "download a file",
	ArgsUsage: "<file_url>",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "output path",
		},
		&cli.IntFlag{
			Name:    "threads",
			Aliases: []string{"t"},
			Value:   4,
			Usage:   "sets downloading goroutines limit (0 uses default)",
		},
		&cli.IntFlag{
			Name:    "part-size",
			Aliases: []string{"p"},
			Value:   tg.DefaultPartSize,
			Usage:   "sets chunk size. Must be divisible by 4KB (Max 1MiB, 0 uses default)",
		},
	},
	Action: func(ctx context.Context, c *cli.Command) error {
		if c.NArg() < 1 {
			return errors.New("download requires at least one <file_url> argument")
		}
		link := c.Args().First()
		output := c.String("output")
		threads := c.Int("threads")
		partSize := c.Int("part-size")
		client := tg.FromContext(ctx)
		if client == nil {
			return errors.New("client not initialized")
		}
		return client.DownloadFromLink(link, output, threads, partSize)
	},
}
