package signaling

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"rdagent/internal/config"
	"rdagent/internal/logger"
	"rdagent/internal/protocol"
)

const (
	writeTimeout = 15 * time.Second
	readLimit    = 512 * 1024
)

type Client struct {
	cfg  config.Config
	conn *websocket.Conn

	writeMu sync.Mutex
}

func NewClient(cfg config.Config) *Client {
	return &Client{cfg: cfg}
}

func (c *Client) Connect(ctx context.Context) error {
	dialer := websocket.DefaultDialer

	if c.cfg.InsecureSkipVerify {
		dialer.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}
	}

	header := http.Header{}
	header.Set("User-Agent", "posrelayd-rd-agent/0.1")

	conn, resp, err := dialer.DialContext(ctx, c.cfg.WSURL, header)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		return fmt.Errorf("websocket dial failed status=%s: %w", status, err)
	}

	conn.SetReadLimit(readLimit)

	c.conn = conn
	logger.RDAgent.Infof("WebSocket connected: %s", c.cfg.WSURL)

	return nil
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Send(msg protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("websocket is not connected")
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))

	if err := c.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("write json type=%s: %w", msg.Type, err)
	}

	logger.RDAgent.Debugf(
		"WS sent: type=%s session_id=%s target=%s",
		msg.Type,
		msg.SessionID,
		msg.Target,
	)

	return nil
}

func (c *Client) ReadLoop(ctx context.Context, out chan<- protocol.Message) error {
	defer close(out)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var msg protocol.Message
		if err := c.conn.ReadJSON(&msg); err != nil {
			return fmt.Errorf("read json: %w", err)
		}

		logger.RDAgent.Debugf(
			"WS received: type=%s session_id=%s target=%s",
			msg.Type,
			msg.SessionID,
			msg.Target,
		)

		select {
		case out <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
