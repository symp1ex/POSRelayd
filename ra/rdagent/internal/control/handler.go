package control

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

type Sender interface {
	SendText(s string) error
}

type Handler struct {
	sessionID string
	origin    string

	injector  *Injector
	clipboard *Clipboard

	seq atomic.Uint64
}

func NewHandler(sessionID string) *Handler {
	return &Handler{
		sessionID: sessionID,
		origin:    "agent:" + sessionID,
		injector:  NewInjector(),
		clipboard: NewClipboard(),
	}
}

func (h *Handler) Handle(dc *webrtc.DataChannel, raw []byte) error {
	var msg Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return fmt.Errorf("decode control message: %w", err)
	}

	switch msg.Type {
	case "focus_changed":
		h.injector.SetFocus(msg.Focused)
		return nil

	case "mouse_move":
		return h.injector.MouseMoveNormalized(msg.X, msg.Y)

	case "mouse_down":
		return h.injector.MouseButton(msg.X, msg.Y, msg.Button, true)

	case "mouse_up":
		return h.injector.MouseButton(msg.X, msg.Y, msg.Button, false)

	case "mouse_wheel":
		return h.injector.MouseWheel(msg.X, msg.Y, msg.DeltaX, msg.DeltaY)

	case "key_down":
		return h.injector.Key(msg.Code, true)

	case "key_up":
		return h.injector.Key(msg.Code, false)

	case "clipboard_set", "clipboard_sync":
		changed, err := h.clipboard.SetText(msg.Text, msg.Origin, msg.Seq, msg.Revision)
		if err != nil {
			return err
		}
		if changed {
			return h.sendClipboardSync(dc)
		}
		return nil

	case "clipboard_get":
		return h.sendClipboardSync(dc)

	case "ping":
		return sendJSON(dc, Message{
			Type:      "ping",
			ID:        msg.ID,
			SessionID: h.sessionID,
			TS:        msg.TS,
		})

	default:
		return nil
	}
}

func (h *Handler) ReleaseAll() {
	h.injector.ReleaseAll()
}

func (h *Handler) sendClipboardSync(dc *webrtc.DataChannel) error {
	text, revision, err := h.clipboard.GetText()
	if err != nil {
		return err
	}

	seq := h.seq.Add(1)

	return sendJSON(dc, Message{
		Type:      "clipboard_sync",
		ID:        fmt.Sprintf("agent-%d", seq),
		SessionID: h.sessionID,
		Origin:    h.origin,
		Seq:       seq,
		Revision:  revision,
		Text:      text,
	})
}

func sendJSON(dc *webrtc.DataChannel, msg Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return dc.SendText(string(raw))
}
