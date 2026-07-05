package tg

import (
	"cmp"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/tg"
	"github.com/schollz/progressbar/v3"
)

const (
	defaultThreads  = 4
	DefaultPartSize = 1024 * 1024 // 1 MB，Telegram 允许的最大值 ✅
)

func (c *Client) DownloadFromLink(link string, output string, threads int, partSize int) error {
	username, msgId, err := ParsePublicLink(link)
	if err != nil {
		return err
	}

	ctx := c.Ctx
	// 1. 用户名 -> peer（群组带用户名本质是 supergroup，即 tg.Channel）
	chat, err := ctx.ResolveUsername(username)
	if err != nil {
		return err
	}
	if !chat.IsAChannel() {
		// 普通私有群 (tg.Chat) 没有用户名，走不到这里；
		// 如果确实是普通群，用 ctx.Raw.MessagesGetMessages 而不是 ChannelsGetMessages
		return fmt.Errorf("不是频道/超级群，当前实现只处理带用户名的 supergroup/channel")
	}
	inputChannel := chat.GetInputChannel()
	// 2. 按消息 ID 拉取消息（channels.getMessages，raw API）
	msgsClass, err := ctx.Raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: inputChannel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgId}},
	})
	if err != nil {
		return err
	}
	msgs, ok := msgsClass.(*tg.MessagesChannelMessages)
	if !ok || len(msgs.Messages) == 0 {
		return fmt.Errorf("消息未找到")
	}
	msg, ok := msgs.Messages[0].(*tg.Message)
	if !ok || msg.Media == nil {
		return fmt.Errorf("该消息不包含媒体文件")
	}

	// 3. 确定保存文件名（可选：从 Document 的 attributes 里取原始文件名）
	fileName := generateSmartName(msg, msgId)
	// 若指定了输出目录/路径，拼接到文件名前
	if output != "" {
		if err = os.MkdirAll(output, 0o755); err != nil {
			return err
		}
		fileName = filepath.Join(output, fileName)
	}

	// 获取文件大小（为了初始化进度条）
	var fileSize int64
	if mmd, ok := msg.Media.(*tg.MessageMediaDocument); ok {
		if doc, ok := mmd.Document.(*tg.Document); ok {
			fileSize = doc.Size
		}
	}
	// 大小未知时用 -1，进度条会以不定长(spinner)模式显示，而非停在 0
	if fileSize <= 0 {
		fileSize = -1
	}

	// 初始化进度条
	bar := progressbar.DefaultBytes(
		fileSize,
		fileName,
	)

	f, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer f.Close()

	actualPartSize := Value(partSize, DefaultPartSize)
	// 限制最大为 1 MB
	if actualPartSize > 1024*1024 {
		actualPartSize = DefaultPartSize
	}
	// 确保必须是 4096 (4KB) 的整数倍
	actualPartSize = (actualPartSize / 4096) * 4096
	if actualPartSize == 0 {
		actualPartSize = 4096 // 兜底最小值
	}

	// 4. 并行下载：DownloadOutputParallel 需要 io.WriterAt，*os.File 天然满足
	progressWriter := &ProgressWriter{
		Writer: f,
		Bar:    bar,
	}

	_, err = ctx.DownloadMedia(
		msg.Media,
		ext.DownloadOutputParallel{WriterAt: progressWriter},
		&ext.DownloadMediaOpts{
			Threads:  Value(threads, defaultThreads),
			PartSize: actualPartSize,
		},
	)
	return err
}

// generateSmartName 优先获取标题，如果标题为空，则退回原始文件名
func generateSmartName(msg *tg.Message, fallbackID int) string {
	// 1. 获取 Caption 并清理非法字符
	caption := strings.TrimSpace(msg.Message)
	if caption == "" {
		caption = fmt.Sprintf("file_%d", fallbackID)
	}
	cleanCaption := sanitizeFileName(caption)

	// 2. 尝试从附件中提取原始后缀
	suffix := ""
	if mmd, ok := msg.Media.(*tg.MessageMediaDocument); ok {
		if doc, ok := mmd.Document.(*tg.Document); ok {
			// A. 优先通过原始文件名获取后缀
			for _, attr := range doc.Attributes {
				if fa, ok := attr.(*tg.DocumentAttributeFilename); ok {
					suffix = filepath.Ext(fa.FileName)
					break
				}
			}
			// B. 如果文件名里没有，尝试从 MIME 类型反推 (如 image/jpeg -> .jpeg)
			if suffix == "" {
				if parts := strings.Split(doc.MimeType, "/"); len(parts) > 1 {
					suffix = "." + parts[1]
				}
			}
		}
	}

	// 3. 检查标题是否已经自带后缀 (防止出现 "Video.mp4.mp4")
	if filepath.Ext(cleanCaption) == "" {
		cleanCaption += suffix
	}

	return cleanCaption
}

// sanitizeFileName 清理非法文件名字符
func sanitizeFileName(name string) string {
	// 简单的替换逻辑，防止报错
	illegals := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, s := range illegals {
		name = strings.ReplaceAll(name, s, "_")
	}
	// 限制长度，避免过长导致路径错误（按 rune 截断，避免破坏多字节字符）
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return name
}

// Value T cmp.Ordered 限制了 T 必须是支持 > < == 等操作符的类型（如 int, float, string）
func Value[T cmp.Ordered](val T, defaultVal T) T {
	var zero T // 零值
	if val > zero {
		return val
	}
	return defaultVal
}

func ParsePublicLink(link string) (string, int, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", 0, err
	}

	// 将路径按 / 分割，例如 ["", "caodoux", "2300"]
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	// 确保至少有用户名和消息ID两部分
	if len(parts) < 2 {
		return "", 0, fmt.Errorf("链接格式错误")
	}

	username := parts[0]
	msgID, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, fmt.Errorf("消息ID解析失败")
	}

	return username, msgID, nil
}

type ProgressWriter struct {
	Writer io.WriterAt
	Bar    *progressbar.ProgressBar
}

func (pw *ProgressWriter) WriteAt(p []byte, off int64) (int, error) {
	n, err := pw.Writer.WriteAt(p, off)
	// 增加进度条的进度
	_ = pw.Bar.Add(n)
	return n, err
}
