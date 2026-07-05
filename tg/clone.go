package tg

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// forwardMessages 单次最多 100 个消息 ID
const maxForwardBatch = 100

// CloneChannel 把 from 频道的全部历史消息「克隆」到 to 频道。
// 用 messages.forwardMessages + drop_author 实现：
//   - 服务器端搬运，不下载 / 重传，保真且快
//   - drop_author 去掉「转发自」抬头，效果等同重新发布
//   - 相册（media group）整组一次转发，grouped_id 得以保留，落地仍是相册
//   - caption 默认保留（DropMediaCaptions 保持 false）
//
// 按消息 ID 升序（即发布时间顺序）转发。需 --login user 读取源频道历史，
// 且账号在源、目标两个频道都要有相应权限。
func (c *Client) CloneChannel(from, to string) error {
	ctx := c.Ctx

	fromChat, err := ctx.ResolveUsername(strings.TrimPrefix(from, "@"))
	if err != nil {
		return fmt.Errorf("解析源频道 %s: %w", from, err)
	}
	if !fromChat.IsAChannel() {
		return fmt.Errorf("源不是频道 / 超级群，无法读取历史: %s", from)
	}
	toChat, err := ctx.ResolveUsername(strings.TrimPrefix(to, "@"))
	if err != nil {
		return fmt.Errorf("解析目标频道 %s: %w", to, err)
	}

	fromID := fromChat.GetID()
	toID := toChat.GetID()

	// 1. 拉取源频道全部历史，整理成「单元」列表（单条消息或一个相册整组）
	units, err := c.collectForwardUnits(fromID)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		fmt.Println("源频道没有可搬运的消息")
		return nil
	}

	total := 0
	for _, u := range units {
		total += len(u)
	}
	fmt.Printf("准备克隆 %d 条消息（%d 个消息 / 相册单元）%s -> %s\n", total, len(units), from, to)

	// 2. 按批转发：单元之间可合并进同一批以减少请求，但绝不拆散一个相册单元，
	//    这样 grouped_id 才不会因跨批而丢失。
	sent := 0
	batch := make([]int, 0, maxForwardBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := c.forwardBatch(fromID, toID, batch); err != nil {
			return err
		}
		sent += len(batch)
		log.Printf("已克隆 %d/%d 条", sent, total)
		batch = batch[:0]
		// 批次之间稍作停顿，缓解频道发送限流
		time.Sleep(time.Second)
		return nil
	}

	for _, u := range units {
		if len(batch)+len(u) > maxForwardBatch {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, u...)
	}
	if err := flush(); err != nil {
		return err
	}

	fmt.Printf("完成：已克隆 %d 条消息到 %s\n", sent, to)
	return nil
}

// collectForwardUnits 遍历源频道全部历史，返回升序排列的转发单元列表。
// 每个单元是一组消息 ID：普通消息为单元素切片，相册为同 grouped_id 的整组。
func (c *Client) collectForwardUnits(fromID int64) ([][]int, error) {
	ctx := c.Ctx
	peer, err := ctx.ResolveInputPeerById(fromID)
	if err != nil {
		return nil, err
	}

	type msgMeta struct {
		id      int
		grouped int64
	}
	var all []msgMeta

	offsetID := 0
	const limit = 100
	for {
		res, err := ctx.Raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     peer,
			OffsetID: offsetID,
			Limit:    limit,
		})
		if err != nil {
			return nil, err
		}
		msgs := extractMessages(res)
		if len(msgs) == 0 {
			break
		}
		for _, mc := range msgs {
			// 只搬运普通消息，跳过服务消息（MessageService）/空消息（MessageEmpty）
			msg, ok := mc.(*tg.Message)
			if !ok {
				continue
			}
			g, _ := msg.GetGroupedID() // 非相册时 ok=false，g 为 0
			all = append(all, msgMeta{id: msg.ID, grouped: g})
		}
		// getHistory 按 ID 降序返回，下一页从本批最小 ID 继续
		next := msgs[len(msgs)-1].GetID()
		if next == 0 || next == offsetID {
			break
		}
		offsetID = next
	}

	// 升序（发布时间顺序）
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })

	// 分组：连续且 grouped_id 相同且非 0 的算同一个相册单元
	var units [][]int
	for i := 0; i < len(all); {
		if all[i].grouped == 0 {
			units = append(units, []int{all[i].id})
			i++
			continue
		}
		j := i + 1
		for j < len(all) && all[j].grouped == all[i].grouped {
			j++
		}
		ids := make([]int, 0, j-i)
		for k := i; k < j; k++ {
			ids = append(ids, all[k].id)
		}
		units = append(units, ids)
		i = j
	}
	return units, nil
}

// forwardBatch 转发一批消息 ID（DropAuthor 去抬头）。遇到 FLOOD_WAIT 自动等待重试。
func (c *Client) forwardBatch(fromID, toID int64, ids []int) error {
	ctx := c.Ctx
	for {
		// ext.Context.ForwardMessages 会自动解析 peer 并为每个 ID 生成 RandomID
		_, err := ctx.ForwardMessages(fromID, toID, &tg.MessagesForwardMessagesRequest{
			ID:         ids,
			DropAuthor: true, // 去掉「转发自」抬头，等同重新发布
		})
		if err == nil {
			return nil
		}
		if d, ok := tgerr.AsFloodWait(err); ok {
			wait := d + time.Second
			log.Printf("触发限流(FLOOD_WAIT)，等待 %s 后重试", wait)
			time.Sleep(wait)
			continue
		}
		return fmt.Errorf("转发失败 %v: %w", ids, err)
	}
}
