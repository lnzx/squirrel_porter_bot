package tg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

const (
	AppName = "squirrel_porter_bot"
)

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".config", AppName)

	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return dir, nil
}

func SessionFile() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "session.db")
	return path, nil
}

type ctxKey struct{}

var key = ctxKey{}

// WithContext 把已认证的 ext.Context 存入标准 context
func WithContext(parent context.Context, client *Client) context.Context {
	return context.WithValue(parent, key, client)
}

// FromContext 从标准 context 里取出 ext.Context，子命令用这个拿
func FromContext(ctx context.Context) *Client {
	c, _ := ctx.Value(key).(*Client)
	return c
}
