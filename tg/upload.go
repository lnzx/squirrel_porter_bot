package tg

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/schollz/progressbar/v3"
)

func (c *Client) Upload(channel string, targets []string, caption string, separate bool) error {
	fmt.Println("caption:", caption)

	// 使用新的过滤函数
	files, err := ResolveGlobPaths(targets)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("没有找到可上传的有效文件")
	}

	fmt.Println("channel:", channel)
	chatId, err := c.getChatIdByUsername(channel)
	if err != nil {
		return err
	}
	fmt.Println("channel id:", chatId)

	if len(files) == 1 { // 单文件
		path := files[0]
		suffix := strings.ToLower(filepath.Ext(path))
		if suffix == ".mp4" {
			return c.sendSingleVideo(chatId, path, caption)
		}
		return c.sendSinglePhoto(chatId, path, caption)
	}

	// 多文件：根据 separate 决定用相册还是逐个发送
	if separate {
		return c.sendFilesOneByOne(chatId, files, caption)
	}
	return c.sendAlbum(chatId, files, caption)
}

// sendFilesOneByOne 逐个上传文件，每个文件独立成一条消息
func (c *Client) sendFilesOneByOne(chatId int64, files []string, caption string) error {
	log.Println("upload files one by one, total:", len(files))
	failed := 0
	for i, path := range files {
		log.Printf("%d uploading %s\n", i, path)

		var err error
		if strings.ToLower(filepath.Ext(path)) == ".mp4" {
			err = c.sendSingleVideo(chatId, path, caption)
		} else {
			err = c.sendSinglePhoto(chatId, path, caption)
		}
		if err != nil {
			log.Printf("%d 上传文件失败, 跳过 %s: %s\n", i, path, err)
			failed++
			continue
		}
	}
	// 全部失败时返回错误，避免调用方误以为成功
	if failed == len(files) {
		return fmt.Errorf("所有文件上传失败 (%d 个)", failed)
	}
	return nil
}

// sendSinglePhoto 上传图片
func (c *Client) sendSinglePhoto(chatId int64, path string, caption string) error {
	ctx := c.Ctx
	// 1. 上传文件到 Telegram（还没发给任何人）
	up, err := c.newUploader(path)
	if err != nil {
		return err
	}

	f, err := up.FromPath(ctx, path)
	if err != nil {
		return err
	}

	if caption == "" {
		caption = filepath.Base(path)
	}

	_, err = ctx.SendMedia(chatId, &tg.MessagesSendMediaRequest{
		Message: caption,
		Media: &tg.InputMediaUploadedPhoto{
			File: f,
		},
	})

	return err
}

// sendSingleVideo 只上传mp4视频
func (c *Client) sendSingleVideo(chatId int64, path string, caption string) error {
	ctx := c.Ctx
	up, err := c.newUploader(path)
	if err != nil {
		return err
	}

	videoFile, err := up.FromPath(ctx, path)
	if err != nil {
		return err
	}

	if caption == "" {
		caption = filepath.Base(path)
	}

	videoAttr := &tg.DocumentAttributeVideo{SupportsStreaming: true}
	// 补充宽/高/时长，缺失会导致预览和拖动体验变差；取不到则降级为仅流式发送
	if w, h, duration, err := getVideoAttrs(path); err != nil {
		log.Println("get video attrs error, send without dimensions:", err)
	} else {
		videoAttr.W, videoAttr.H, videoAttr.Duration = w, h, duration
	}

	doc := &tg.InputMediaUploadedDocument{
		File:     videoFile,
		MimeType: "video/mp4",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
			videoAttr,
		},
	}

	// 和视频同名的jpg文件是封面
	thumbPath := replaceExt(path, ".jpg")
	if _, err = os.Stat(thumbPath); err == nil {
		fmt.Println("thumb:", thumbPath)
		if thumb, err := up.FromPath(ctx, thumbPath); err == nil {
			doc.Thumb = thumb
		}
	}

	_, err = ctx.SendMedia(chatId, &tg.MessagesSendMediaRequest{
		Message: caption,
		Media:   doc,
	})

	return err
}

// Telegram 相册（media group）单条最多 10 个媒体
const maxAlbumSize = 10

// sendAlbum 按每 10 个一批分组发送，避免超过 Telegram 相册上限。
// caption 只放在第一批的第一条，后续批次不带说明。
func (c *Client) sendAlbum(chatId int64, paths []string, caption string) error {
	for i := 0; i < len(paths); i += maxAlbumSize {
		end := i + maxAlbumSize
		if end > len(paths) {
			end = len(paths)
		}
		batchCaption := ""
		if i == 0 {
			batchCaption = caption
		}
		if err := c.sendAlbumBatch(chatId, paths[i:end], batchCaption); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) sendAlbumBatch(chatId int64, paths []string, caption string) error {
	ctx := c.Ctx

	peer, err := ctx.ResolveInputPeerById(chatId)
	if err != nil {
		return err
	}

	multiMedia := make([]tg.InputSingleMedia, 0, len(paths))
	for i, path := range paths {
		up, err := c.newUploader(path)
		if err != nil {
			return err
		}

		// 1. 逐个上传文件
		f, err := up.FromPath(ctx, path)
		if err != nil {
			return err
		}

		// 2. 根据是不是图片选择 InputMedia 类型
		var uploaded tg.InputMediaClass
		if strings.ToLower(filepath.Ext(path)) == ".mp4" { // 如果是文档/视频
			// 和视频同名的jpg文件是封面
			thumbPath := replaceExt(path, ".jpg")
			var thumb tg.InputFileClass
			if _, err = os.Stat(thumbPath); err == nil {
				fmt.Println("thumb:", thumbPath)
				if thumb, err = up.FromPath(ctx, thumbPath); err != nil {
					log.Println("upload thumb error: ", err)
				}
			}

			videoAttr := &tg.DocumentAttributeVideo{SupportsStreaming: true}
			// 与单文件路径一致：取不到宽/高/时长时降级为仅流式发送，不因个别文件探测失败中断整个相册
			if w, h, duration, err := getVideoAttrs(path); err != nil {
				log.Println("get video attrs error, send without dimensions:", err)
			} else {
				videoAttr.W, videoAttr.H, videoAttr.Duration = w, h, duration
			}

			uploaded = &tg.InputMediaUploadedDocument{
				File:     f,
				MimeType: "video/mp4",
				Thumb:    thumb,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
					videoAttr,
				},
			}
		} else {
			uploaded = &tg.InputMediaUploadedPhoto{File: f}
		}

		// 关键一步：先注册成正式媒体，拿到带 file_reference 的对象
		registered, err := ctx.Raw.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
			Peer:  peer,
			Media: uploaded,
		})
		if err != nil {
			return fmt.Errorf("uploadMedia failed for %s: %w", path, err)
		}

		var media tg.InputMediaClass
		switch m := registered.(type) {
		case *tg.MessageMediaDocument:
			d, ok := m.Document.(*tg.Document)
			if !ok {
				return fmt.Errorf("empty document for %s", path)
			}
			media = &tg.InputMediaDocument{
				ID: &tg.InputDocument{
					ID:            d.ID,
					AccessHash:    d.AccessHash,
					FileReference: d.FileReference,
				},
			}
		case *tg.MessageMediaPhoto:
			p, ok := m.Photo.(*tg.Photo)
			if !ok {
				return fmt.Errorf("empty photo for %s", path)
			}
			media = &tg.InputMediaPhoto{
				ID: &tg.InputPhoto{
					ID:            p.ID,
					AccessHash:    p.AccessHash,
					FileReference: p.FileReference,
				},
			}
		default:
			return fmt.Errorf("unexpected media type %T for %s", registered, path)
		}

		msg := ""
		if i == 0 {
			if caption != "" {
				msg = caption
			} else {
				msg = filepath.Base(path)
			}
		}

		multiMedia = append(multiMedia, tg.InputSingleMedia{
			Media:    media,
			RandomID: rand.Int63(), // 每个条目必须有唯一的 RandomID
			Message:  msg,          // 相册里通常只有第一条的文字会显示为整条消息的说明
		})
	}

	// 3. 一次性把整个相册发出去
	_, err = ctx.SendMultiMedia(chatId, &tg.MessagesSendMultiMediaRequest{
		MultiMedia: multiMedia,
	})
	return err
}

func (c *Client) newUploader(path string) (*uploader.Uploader, error) {
	threads := c.UploadThreads
	if threads <= 0 {
		threads = defaultThreads // 默认线程数
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	bar := progressbar.DefaultBytes(info.Size(), filepath.Base(path))

	return uploader.NewUploader(c.Ctx.Raw).
		WithThreads(threads).
		WithPartSize(uploader.MaximumPartSize).
		WithProgress(&barProgress{bar: bar}), nil
}

// ResolveGlobPaths 接收原始参数列表，返回展开后的文件路径列表
func ResolveGlobPaths(targets []string) ([]string, error) {
	var fileList []string

	for _, pattern := range targets {
		// 判断参数是否包含通配符 (*, ?, [, ])
		// 如果包含，则调用 Glob 进行匹配
		if strings.ContainsAny(pattern, "*?[]") {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return nil, err
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("未找到匹配的文件: %s", pattern)
			}

			// 2. 遍历匹配项，进行筛选
			for _, path := range matches {
				info, err := os.Stat(path)
				if err != nil {
					// 可能是权限问题或文件被删除，跳过
					return nil, fmt.Errorf("获取文件信息: %s", path)
				}

				// 只添加非目录的文件
				if !info.IsDir() {
					fileList = append(fileList, path)
				}
			}
		} else {
			// 没有通配符，当作具体路径：校验存在性并排除目录
			info, err := os.Stat(pattern)
			if err != nil {
				return nil, fmt.Errorf("获取文件信息: %s: %w", pattern, err)
			}
			if info.IsDir() {
				return nil, fmt.Errorf("不支持上传目录: %s", pattern)
			}
			fileList = append(fileList, pattern)
		}
	}
	return fileList, nil
}

func replaceExt(path, newExt string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	return base + newExt
}

type videoInfo struct {
	Streams []struct {
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Duration string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func getVideoAttrs(path string) (w, h int, duration float64, err error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		path,
	)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, err
	}
	var info videoInfo
	if err := json.Unmarshal(output, &info); err != nil {
		return 0, 0, 0, err
	}
	for _, s := range info.Streams {
		if s.Width > 0 {
			w, h = s.Width, s.Height
			if s.Duration != "" {
				duration, _ = strconv.ParseFloat(s.Duration, 64)
			}
			// 部分 mp4 的时长只写在 format 里、stream 内为空，回退取 format.duration
			if duration == 0 && info.Format.Duration != "" {
				duration, _ = strconv.ParseFloat(info.Format.Duration, 64)
			}
			return w, h, duration, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("no video stream found")
}

type barProgress struct {
	bar *progressbar.ProgressBar
}

func (p *barProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	_ = p.bar.Set64(state.Uploaded) // Uploaded 本身就是累计值，直接设置即可
	return nil
}
