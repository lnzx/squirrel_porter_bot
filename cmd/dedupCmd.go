package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/lnzx/squirrel_porter_bot/tg"
	"github.com/urfave/cli/v3"
)

var DedupCmd = &cli.Command{
	Name:    "dedup",
	Aliases: []string{"dd"},
	Usage:   "scan a channel and remove duplicate files (needs --login user)",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "confirm",
			Aliases: []string{"y"},
			Usage:   "actually delete duplicates (default is dry-run preview only)",
		},
	},
	Action: func(ctx context.Context, c *cli.Command) error {
		channel := c.String("channel")
		if channel == "" {
			return errors.New("missing channel, use -c <channel>")
		}
		confirm := c.Bool("confirm")
		client := tg.FromContext(ctx)
		if client == nil {
			return errors.New("client not initialized (use --login user)")
		}

		groups, err := client.ScanDuplicates(channel)
		if err != nil {
			return err
		}

		if len(groups) == 0 {
			fmt.Println("未发现重复文件")
			return nil
		}

		// 预览：逐组打印保留项与将删除项
		total := 0
		for i, g := range groups {
			fmt.Printf("\n[%d] %s\n", i+1, g.Signature)
			fmt.Printf("    保留 #%d  %s\n", g.Keep.ID, g.Keep.Label)
			for _, d := range g.Delete {
				fmt.Printf("    删除 #%d  %s\n", d.ID, d.Label)
				total++
			}
		}
		fmt.Printf("\n共 %d 组重复，将删除 %d 条消息\n", len(groups), total)

		if !confirm {
			fmt.Println("这是预览(dry-run)。确认无误后加 --confirm 执行删除。")
			return nil
		}

		// 汇总所有待删除 ID 并执行删除
		ids := make([]int, 0, total)
		for _, g := range groups {
			for _, d := range g.Delete {
				ids = append(ids, d.ID)
			}
		}
		deleted, err := client.DeleteMessages(channel, ids)
		if err != nil {
			return fmt.Errorf("删除失败(已删 %d 条): %w", deleted, err)
		}
		fmt.Printf("已删除 %d 条重复消息\n", deleted)
		return nil
	},
}
