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

func (c *Client) getChatIdByUsername(username string) (int64, error) {
	chat, err := c.Ctx.ResolveUsername(username) // 不用带 @
	if err != nil {
		return 0, err
	}
	return chat.GetID(), nil
}
