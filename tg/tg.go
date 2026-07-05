package tg

import (
	"context"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/glebarez/sqlite"
)

type Client struct {
	Ctx           *ext.Context
	UploadThreads int
	client        *gotgproto.Client
}

func NewClient(ctx context.Context, apiId int, apiHash string, botToken string) (*Client, error) {
	sessionFile, err := SessionFile()
	if err != nil {
		return nil, err
	}
	client, err := gotgproto.NewClient(
		apiId,
		apiHash, gotgproto.ClientTypeBot(botToken),
		&gotgproto.ClientOpts{
			DisableCopyright: true,
			InMemory:         false,
			Session:          sessionMaker.SqlSession(sqlite.Open(sessionFile)),
			Context:          ctx,
		},
	)
	if err != nil {
		return nil, err
	}

	return &Client{
		Ctx:           client.CreateContext(),
		client:        client,
		UploadThreads: defaultThreads,
	}, nil
}

// NewUserClient 以用户账号(手机号+验证码)身份登录。
// 复用同一套 apiId/apiHash，仅认证方式换成 ClientTypePhone。
// 首次运行会通过 gotgproto 默认的终端 conversator 在 stdin 提示输入验证码
// (如账号开启了两步验证，会继续提示输入密码)，session 存入独立文件后后续免登录。
// 用户账号能读取频道历史，是清理已存在重复文件的前提(bot 无此权限)。
func NewUserClient(ctx context.Context, apiId int, apiHash string, phone string) (*Client, error) {
	sessionFile, err := UserSessionFile()
	if err != nil {
		return nil, err
	}
	client, err := gotgproto.NewClient(
		apiId,
		apiHash, gotgproto.ClientTypePhone(phone),
		&gotgproto.ClientOpts{
			DisableCopyright: true,
			InMemory:         false,
			Session:          sessionMaker.SqlSession(sqlite.Open(sessionFile)),
			Context:          ctx,
		},
	)
	if err != nil {
		return nil, err
	}

	return &Client{
		Ctx:           client.CreateContext(),
		client:        client,
		UploadThreads: defaultThreads,
	}, nil
}

func (c *Client) getChatIdByUsername(username string) (int64, error) {
	chat, err := c.Ctx.ResolveUsername(username) // 不用带 @
	if err != nil {
		return 0, err
	}
	return chat.GetID(), nil
}

// Stop 优雅关闭底层 gotgproto 客户端（取消内部 context）
func (c *Client) Stop() {
	if c.client != nil {
		c.client.Stop()
	}
}
