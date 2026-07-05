package tg

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// 服务端临时 5xx（如 WORKER_BUSY_TOO_LONG_RETRY）最多退避重试次数
const maxTransientRetry = 8

// CloneChannel 把 from 频道的全部历史消息「克隆」到 to 频道。
// 用 messages.forwardMessages + drop_author 实现：
//   - 服务器端搬运，不下载 / 重传，保真且快
//   - drop_author 去掉「转发自」抬头，效果等同重新发布
//   - 相册（media group）整组一次转发，grouped_id 得以保留，落地仍是相册
//   - caption 默认保留（DropMediaCaptions 保持 false）
//
// 按消息 ID 升序（即发布时间顺序）转发。需 --login user 读取源频道历史，
// 且账号在源、目标两个频道都要有相应权限。
//
// minID 用于断点续传：只克隆源频道中 ID 严格大于 minID 的消息（传 0 表示全部）。
// 上次中断后，把「最后一条已成功克隆的源消息 ID」作为 minID 重跑即可跳过已搬运部分。
func (c *Client) CloneChannel(from, to string, minID int) error {
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

	// 1. 拉取源频道历史（可选跳过 ID <= minID 的部分），整理成「单元」列表
	units, err := c.collectForwardUnits(fromID, minID)
	if err != nil {
		return err
	}
	if len(units) == 0 {
		fmt.Println("没有需要克隆的消息（可能都已被 --min-id 跳过）")
		return nil
	}

	total := 0
	for _, u := range units {
		total += len(u)
	}
	fmt.Printf("准备克隆 %d 条消息（%d 个消息 / 相册单元）%s -> %s\n", total, len(units), from, to)

	// 2. 逐单元转发：一次只发一条消息或一个相册整组。
	//    drop_author 会让服务端做「拷贝」而非普通转发，开销较大，
	//    大批量一次转发容易触发 WORKER_BUSY_TOO_LONG_RETRY，故按单元逐个发送。
	//    相册整组一次发出，grouped_id 得以保留、落地仍是相册。
	sent := 0
	for _, u := range units {
		if err := c.forwardUnit(fromID, toID, u); err != nil {
			return fmt.Errorf("已克隆 %d/%d 条后中断: %w", sent, total, err)
		}
		sent += len(u)
		log.Printf("已克隆 %d/%d 条", sent, total)
		// 单元之间稍作停顿，缓解服务端拷贝压力与发送限流
		time.Sleep(500 * time.Millisecond)
	}

	fmt.Printf("完成：已克隆 %d 条消息到 %s\n", sent, to)
	return nil
}

// collectForwardUnits 遍历源频道历史，返回升序排列的转发单元列表。
// 每个单元是一组消息 ID：普通消息为单元素切片，相册为同 grouped_id 的整组。
// minID > 0 时只保留 ID 严格大于 minID 的消息（断点续传用）。
func (c *Client) collectForwardUnits(fromID int64, minID int) ([][]int, error) {
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
			MinID:    minID, // >0 时服务端只返回 ID 大于 minID 的消息；0 表示不限
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
			// 双保险：MinID 之外再本地过滤一次
			if minID > 0 && msg.ID <= minID {
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

// forwardUnit 转发一个单元（单条消息或一个相册整组，DropAuthor 去抬头）。
// FLOOD_WAIT 按服务端要求等待；服务端临时 5xx（如 WORKER_BUSY_TOO_LONG_RETRY）退避重试。
func (c *Client) forwardUnit(fromID, toID int64, ids []int) error {
	ctx := c.Ctx
	attempt := 0
	for {
		// ext.Context.ForwardMessages 会自动解析 peer 并为每个 ID 生成 RandomID
		_, err := ctx.ForwardMessages(fromID, toID, &tg.MessagesForwardMessagesRequest{
			ID:         ids,
			DropAuthor: true, // 去掉「转发自」抬头，等同重新发布
		})
		if err == nil {
			return nil
		}
		// FLOOD_WAIT：按服务端要求等待（不计入退避次数）
		if d, ok := tgerr.AsFloodWait(err); ok {
			wait := d + time.Second
			log.Printf("触发限流(FLOOD_WAIT)，等待 %s 后重试", wait)
			time.Sleep(wait)
			continue
		}
		// 服务端临时错误(5xx)：退避后重试
		if isTransient(err) && attempt < maxTransientRetry {
			attempt++
			backoff := time.Duration(attempt) * 2 * time.Second
			log.Printf("服务端繁忙(%v)，第 %d/%d 次退避 %s 后重试", err, attempt, maxTransientRetry, backoff)
			time.Sleep(backoff)
			continue
		}
		return fmt.Errorf("转发失败 %v: %w", ids, err)
	}
}

// isTransient 判断是否为可重试的服务端临时错误（Telegram 5xx，如 WORKER_BUSY_TOO_LONG_RETRY）
func isTransient(err error) bool {
	var e *tgerr.Error
	if errors.As(err, &e) {
		return e.Code >= 500
	}
	return false
}
