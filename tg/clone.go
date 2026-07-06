package tg

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// 服务端临时 5xx（如 WORKER_BUSY_TOO_LONG_RETRY）最多退避重试次数
const maxTransientRetry = 8

const (
	// defaultCloneBatch 每批转发的默认消息数
	defaultCloneBatch = 20
	// maxCloneBatch Telegram forwardMessages 单次最多 100 个 ID
	maxCloneBatch = 100
	// cloneBatchSleep 批与批之间的停顿，缓解服务端拷贝压力
	cloneBatchSleep = time.Second
)

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
func (c *Client) CloneChannel(from, to string, minID, batchSize int) error {
	ctx := c.Ctx

	if batchSize <= 0 {
		batchSize = defaultCloneBatch
	}
	if batchSize > maxCloneBatch {
		batchSize = maxCloneBatch
	}

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
	// 解析后再判等，拦截 @foo 与 foo 等写法不同但实为同一频道的自我克隆
	if fromID == toID {
		return fmt.Errorf("源和目标是同一个频道，拒绝自我克隆")
	}

	// forwardMessages 需要 from/to 的 InputPeer，先解析好在整个过程复用
	fromPeer, err := ctx.ResolveInputPeerById(fromID)
	if err != nil {
		return fmt.Errorf("解析源 peer: %w", err)
	}
	toPeer, err := ctx.ResolveInputPeerById(toID)
	if err != nil {
		return fmt.Errorf("解析目标 peer: %w", err)
	}

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

	// 2. 把单元打包成批：一次 forwardMessages 携带多条以减少往返、显著提速；
	//    绝不拆散相册（一个单元始终落在同一批内），batchSize 控制每批消息数上限。
	//    drop_author 让服务端做「拷贝」而非普通转发，开销较大，故 batch 不宜过大。
	batches := packUnits(units, batchSize)
	fmt.Printf("准备克隆 %d 条消息（%d 个单元 / %d 批，每批 ≤ %d 条）%s -> %s\n",
		total, len(units), len(batches), batchSize, from, to)

	// 3. 逐批转发。每批要么整批成功、要么不推进断点，故 lastID 始终是「已确定克隆完」
	//    的源消息 ID 上界，失败时据此用 --min-id 续传。
	sent := 0
	lastID := minID
	for _, ids := range batches {
		if err := c.forwardBatch(fromPeer, toPeer, ids); err != nil {
			return fmt.Errorf("已克隆 %d/%d 条后中断，可用 --min-id %d 续传: %w",
				sent, total, lastID, err)
		}
		sent += len(ids)
		lastID = ids[len(ids)-1] // ids 升序，末位即本批最大源 ID
		log.Printf("已克隆 %d/%d 条（源 ID 已到 %d）", sent, total, lastID)
		// 批与批之间稍作停顿，缓解服务端拷贝压力与发送限流
		time.Sleep(cloneBatchSleep)
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
		var res tg.MessagesMessagesClass
		err := withFloodRetry("读取源频道历史", func() error {
			var e error
			res, e = ctx.Raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     peer,
				OffsetID: offsetID,
				Limit:    limit,
				MinID:    minID, // >0 时服务端只返回 ID 大于 minID 的消息；0 表示不限
			})
			return e
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

// packUnits 把升序单元列表打包成若干批，每批消息数尽量接近 batchSize 且不拆散任何单元（相册）。
// 单个相册单元本身超过 batchSize 时，它独占一批（仍保持整组）。
// 结果每批仍是升序的源消息 ID 列表，且后一批的 ID 全部大于前一批。
func packUnits(units [][]int, batchSize int) [][]int {
	var batches [][]int
	var cur []int
	for _, u := range units {
		if len(cur) > 0 && len(cur)+len(u) > batchSize {
			batches = append(batches, cur)
			cur = nil
		}
		cur = append(cur, u...)
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// forwardBatch 一次转发一批消息（可含多条普通消息 + 多个相册整组，DropAuthor 去抬头）。
// RandomID 预生成一次并在重试间复用：转发失败重试时服务端按 random_id 去重，
// 从而避免「批中途报错后重试造成重复搬运」，让重试变成幂等操作。
func (c *Client) forwardBatch(fromPeer, toPeer tg.InputPeerClass, ids []int) error {
	ctx := c.Ctx
	randomIDs := make([]int64, len(ids))
	for i := range randomIDs {
		randomIDs[i] = rand.Int63()
	}
	err := withFloodRetry(fmt.Sprintf("转发 %v", ids), func() error {
		_, e := ctx.Raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
			FromPeer:   fromPeer,
			ToPeer:     toPeer,
			ID:         ids,
			RandomID:   randomIDs, // 重试间不变，服务端据此去重
			DropAuthor: true,      // 去掉「转发自」抬头，等同重新发布
		})
		return e
	})
	if err != nil {
		return fmt.Errorf("转发失败 %v: %w", ids, err)
	}
	return nil
}

// withFloodRetry 执行 fn，并对 Telegram 的两类可恢复错误自动重试：
//   - FLOOD_WAIT：按服务端要求的时长等待后重试（不计入退避次数，不设上限）
//   - 服务端临时 5xx（如 WORKER_BUSY_TOO_LONG_RETRY）：线性退避后重试，最多 maxTransientRetry 次
//
// 其他错误（或 5xx 超过重试上限）直接返回。desc 仅用于日志描述当前操作。
// 读历史分页、转发等所有 Raw 调用都应经此包装，避免大频道翻页时被限流直接中断。
func withFloodRetry(desc string, fn func() error) error {
	attempt := 0
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if d, ok := tgerr.AsFloodWait(err); ok {
			wait := d + time.Second
			log.Printf("%s 触发限流(FLOOD_WAIT)，等待 %s 后重试", desc, wait)
			time.Sleep(wait)
			continue
		}
		if isTransient(err) && attempt < maxTransientRetry {
			attempt++
			backoff := time.Duration(attempt) * 2 * time.Second
			log.Printf("%s 服务端繁忙(%v)，第 %d/%d 次退避 %s 后重试", desc, err, attempt, maxTransientRetry, backoff)
			time.Sleep(backoff)
			continue
		}
		return err
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
