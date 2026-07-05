package tg

import (
	"context"
	"log"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/sessionMaker"
)

type Client struct {
	Ctx    *ext.Context
	client *gotgproto.Client
}

func NewClient(ctx context.Context, apiId int, apiHash string, botToken string) (*Client, error) {
	client, err := gotgproto.NewClient(
		apiId,
		apiHash, gotgproto.ClientTypeBot(botToken),
		&gotgproto.ClientOpts{
			InMemory: false,
			Session:  sessionMaker.SimpleSession(),
			Context:  ctx,
		},
	)
	if err != nil {
		return nil, err
	}

	return &Client{
		Ctx:    client.CreateContext(),
		client: client,
	}, nil
}

func (c *Client) getChatIdByUsername(username string) (int64, error) {
	chat, err := c.Ctx.ResolveUsername(username) // 不用带 @
	if err != nil {
		return 0, err
	}
	return chat.GetID(), nil
}

func (c *Client) Stop() {
	c.client.Stop()
	log.Println("Client stopped")
}
