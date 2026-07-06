package tg

import (
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
)

// DupItem 一条参与判重的消息
type DupItem struct {
	ID    int
	Label string // 人类可读，如 "video.mp4 (12.3 MB)"
}

// DupGroup 一组签名相同的重复消息：保留 Keep(最早的一条)，删除 Delete
type DupGroup struct {
	Signature string
	Keep      DupItem
	Delete    []DupItem
}

// ScanDuplicates 遍历频道全部历史，按 "真实文件名|大小" 元数据判重。
// 每个签名下保留消息 ID 最小(最早)的一条，其余作为重复项返回。
// 需要以用户账号身份登录(bot 无法读取频道历史)。
func (c *Client) ScanDuplicates(channel string) ([]DupGroup, error) {
	ctx := c.Ctx
	username := strings.TrimPrefix(channel, "@")

	chat, err := ctx.ResolveUsername(username)
	if err != nil {
		return nil, err
	}
	if !chat.IsAChannel() {
		return nil, fmt.Errorf("不是频道/超级群，无法遍历历史")
	}
	peer, err := ctx.ResolveInputPeerById(chat.GetID())
	if err != nil {
		return nil, err
	}

	// signature -> 匹配到的消息列表；order 记录签名首次出现顺序，保证输出稳定
	buckets := make(map[string][]DupItem)
	var order []string

	offsetID := 0
	const limit = 100
	for {
		var res tg.MessagesMessagesClass
		err := withFloodRetry("读取频道历史", func() error {
			var e error
			res, e = ctx.Raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:     peer,
				OffsetID: offsetID,
				Limit:    limit,
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
			msg, ok := mc.(*tg.Message)
			if !ok {
				continue
			}
			key, label, ok := mediaSignature(msg)
			if !ok {
				continue
			}
			if _, seen := buckets[key]; !seen {
				order = append(order, key)
			}
			buckets[key] = append(buckets[key], DupItem{ID: msg.ID, Label: label})
		}
		// getHistory 返回按 ID 降序，下一页从本批最小 ID 继续
		next := msgs[len(msgs)-1].GetID()
		if next == 0 || next == offsetID {
			break
		}
		offsetID = next
	}

	// 桶内 >1 条才算重复；保留 ID 最小(最早)的一条，其余进删除列表
	var groups []DupGroup
	for _, key := range order {
		items := buckets[key]
		if len(items) < 2 {
			continue
		}
		keepIdx := 0
		for i := range items {
			if items[i].ID < items[keepIdx].ID {
				keepIdx = i
			}
		}
		var del []DupItem
		for i := range items {
			if i != keepIdx {
				del = append(del, items[i])
			}
		}
		groups = append(groups, DupGroup{
			Signature: key,
			Keep:      items[keepIdx],
			Delete:    del,
		})
	}
	return groups, nil
}

// DeleteMessages 分批(≤100/批)删除指定消息，返回成功提交删除的数量。
func (c *Client) DeleteMessages(channel string, ids []int) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ctx := c.Ctx
	username := strings.TrimPrefix(channel, "@")

	chat, err := ctx.ResolveUsername(username)
	if err != nil {
		return 0, err
	}
	if !chat.IsAChannel() {
		return 0, fmt.Errorf("不是频道/超级群")
	}
	inputChannel := chat.GetInputChannel()

	deleted := 0
	const batch = 100
	for i := 0; i < len(ids); i += batch {
		end := i + batch
		if end > len(ids) {
			end = len(ids)
		}
		if err = withFloodRetry("删除消息", func() error {
			_, e := ctx.Raw.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: inputChannel,
				ID:      ids[i:end],
			})
			return e
		}); err != nil {
			return deleted, err
		}
		deleted += end - i
	}
	return deleted, nil
}

// extractMessages 从 getHistory 的多种返回类型里取出消息列表
func extractMessages(res tg.MessagesMessagesClass) []tg.MessageClass {
	switch m := res.(type) {
	case *tg.MessagesChannelMessages:
		return m.Messages
	case *tg.MessagesMessages:
		return m.Messages
	case *tg.MessagesMessagesSlice:
		return m.Messages
	default:
		return nil
	}
}

// mediaSignature 计算一条消息的判重签名。
// document(含视频): 真实文件名 + 大小；无文件名时退回 mime + 大小。
// photo: 无真实文件名，best-effort 用 最大尺寸字节数 + 宽x高。
// 无媒体或无法指纹的消息返回 ok=false。
func mediaSignature(msg *tg.Message) (key, label string, ok bool) {
	if msg.Media == nil {
		return "", "", false
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			return "", "", false
		}
		if name := documentFileName(doc); name != "" {
			return fmt.Sprintf("doc:%s|%d", name, doc.Size),
				fmt.Sprintf("%s (%s)", name, humanSize(doc.Size)), true
		}
		// 无文件名：退回 mime + size
		return fmt.Sprintf("doc:%s|%d", doc.MimeType, doc.Size),
			fmt.Sprintf("%s (%s)", doc.MimeType, humanSize(doc.Size)), true
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok {
			return "", "", false
		}
		w, h, size := largestPhotoSize(photo)
		return fmt.Sprintf("photo:%dx%d|%d", w, h, size),
			fmt.Sprintf("photo %dx%d (%s)", w, h, humanSize(int64(size))), true
	default:
		return "", "", false
	}
}

// documentFileName 取 document 的原始文件名，没有则返回空串
func documentFileName(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if fa, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return fa.FileName
		}
	}
	return ""
}

// largestPhotoSize 取图片最大一档缩略图的宽/高/字节数，作为判重指纹(best-effort)
func largestPhotoSize(photo *tg.Photo) (w, h, size int) {
	for _, s := range photo.Sizes {
		switch ps := s.(type) {
		case *tg.PhotoSize:
			if ps.Size > size {
				w, h, size = ps.W, ps.H, ps.Size
			}
		case *tg.PhotoSizeProgressive:
			// Sizes 为渐进式各档字节数，取最大一档近似完整大小
			maxBytes := 0
			for _, v := range ps.Sizes {
				if v > maxBytes {
					maxBytes = v
				}
			}
			if maxBytes > size {
				w, h, size = ps.W, ps.H, maxBytes
			}
		}
	}
	return
}

// humanSize 把字节数格式化为易读大小
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
