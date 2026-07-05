package tg

import (
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
)

func (c *Client) Upload(channel string, targets []string, caption string) error {
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

	// 多文件 专辑
	return c.sendAlbum(chatId, files, caption)
}

// sendSinglePhoto 上传图片
func (c *Client) sendSinglePhoto(chatId int64, img string, caption string) error {
	ctx := c.Ctx
	// 1. 上传文件到 Telegram（还没发给任何人）
	f, err := uploader.NewUploader(ctx.Raw).FromPath(ctx, img)
	if err != nil {
		return err
	}

	if caption == "" {
		caption = filepath.Base(img)
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
func (c *Client) sendSingleVideo(chatId int64, video string, caption string) error {
	ctx := c.Ctx
	up := uploader.NewUploader(ctx.Raw)

	videoFile, err := up.FromPath(ctx, video)
	if err != nil {
		return err
	}

	if caption == "" {
		caption = filepath.Base(video)
	}

	doc := &tg.InputMediaUploadedDocument{
		File:     videoFile,
		MimeType: "video/mp4",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: caption},
			&tg.DocumentAttributeVideo{
				SupportsStreaming: true,
			},
		},
	}

	// 和视频同名的jpg文件是封面
	thumbPath := replaceExt(video, ".jpg")
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

func (c *Client) sendAlbum(chatId int64, paths []string, caption string) error {
	ctx := c.Ctx
	up := uploader.NewUploader(ctx.Raw)

	multiMedia := make([]tg.InputSingleMedia, 0, len(paths))
	for i, path := range paths {
		// 1. 逐个上传文件
		f, err := up.FromPath(ctx, path)
		if err != nil {
			return err
		}

		// 2. 根据是不是图片选择 InputMedia 类型
		var media tg.InputMediaClass
		if strings.ToLower(filepath.Ext(path)) == ".mp4" { // 如果是文档/视频
			// 和视频同名的jpg文件是封面
			thumbPath := replaceExt(path, ".jpg")
			var thumb tg.InputFileClass
			if _, err = os.Stat(thumbPath); err == nil {
				fmt.Println("thumb:", thumbPath)
				if thumb, err = up.FromPath(ctx, thumbPath); err != nil {
					log.Println(err)
				}
			}

			var w, h int
			var duration float64
			if w, h, duration, err = getVideoAttrs(path); err != nil {
				// 如果获取失败，可以给个默认值或直接报错
				log.Println("get video attrs failed", err)
			}
			fmt.Println("w:", w, "h:", h, "duration:", duration)

			media = &tg.InputMediaUploadedDocument{
				File:     f,
				MimeType: "video/mp4",
				Thumb:    thumb,
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: filepath.Base(path)},
					&tg.DocumentAttributeVideo{
						SupportsStreaming: true,
						W:                 w,
						H:                 h,
						Duration:          duration,
					},
				},
			}
		} else {
			media = &tg.InputMediaUploadedPhoto{File: f}
		}

		// 3. 逻辑修复：只有第一项(i==0)才带 caption
		msg := ""
		if i == 0 {
			if caption == "" {
				caption = filepath.Base(path)
			}
			msg = caption
		}

		multiMedia = append(multiMedia, tg.InputSingleMedia{
			Media:    media,
			RandomID: rand.Int63(), // 每个条目必须有唯一的 RandomID
			Message:  msg,          // 相册里通常只有第一条的文字会显示为整条消息的说明
		})
	}

	// 3. 一次性把整个相册发出去
	_, err := ctx.SendMultiMedia(chatId, &tg.MessagesSendMultiMediaRequest{
		MultiMedia: multiMedia,
	})
	return err
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
			// 如果没有通配符，则直接添加（假设它是具体的文件路径）
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
}

func getVideoAttrs(path string) (w, h int, duration float64, err error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
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
			return w, h, duration, nil
		}
	}
	return 0, 0, 0, fmt.Errorf("no video stream found")
}
