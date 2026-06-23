package control

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"

	"rdagent/internal/logger"
)

const maxClipboardTextBytes = 60 * 1024

type Sender interface {
	SendText(s string) error
}

type Handler struct {
	sessionID string
	origin    string

	injector  *Injector
	clipboard *Clipboard
	watcher   *ClipboardWatcher

	seq atomic.Uint64

	senderMu sync.RWMutex
	sender   Sender
}

func NewHandler(sessionID string) *Handler {
	h := &Handler{
		sessionID: sessionID,
		origin:    "agent:" + sessionID,
		injector:  NewInjector(),
		clipboard: NewClipboard(),
	}
	h.watcher = NewClipboardWatcher(func() {
		if err := h.sendClipboardIfChanged(); err != nil {
			logger.RDAgent.Warnf("Clipboard watcher send failed: %v", err)
		}
	})
	return h
}

func sanitizeClipboardText(s string) (string, error) {
	s = strings.ReplaceAll(s, "\x00", "")
	if len([]byte(s)) > maxClipboardTextBytes {
		return "", fmt.Errorf("clipboard payload too large: %d bytes", len([]byte(s)))
	}
	return s, nil
}

func (h *Handler) BindSender(sender Sender) error {
	h.senderMu.Lock()
	h.sender = sender
	h.senderMu.Unlock()

	if h.watcher != nil {
		return h.watcher.Start()
	}
	return nil
}

func (h *Handler) UnbindSender() {
	if h.watcher != nil {
		h.watcher.Stop()
	}

	h.senderMu.Lock()
	h.sender = nil
	h.senderMu.Unlock()
}

func (h *Handler) currentSender() Sender {
	h.senderMu.RLock()
	defer h.senderMu.RUnlock()
	return h.sender
}

func (h *Handler) sendClipboardIfChanged() error {
	sender := h.currentSender()
	if sender == nil {
		return nil
	}

	text, revision, changed, err := h.clipboard.GetTextIfChanged()
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}

	logger.RDAgent.Infof(
		"Clipboard changed on host: revision=%s bytes=%d",
		revision,
		len([]byte(text)),
	)

	text, err = sanitizeClipboardText(text)
	if err != nil {
		return err
	}

	seq := h.seq.Add(1)

	raw, err := json.Marshal(Message{
		Type:      "clipboard_sync",
		ID:        fmt.Sprintf("agent-%d", seq),
		SessionID: h.sessionID,
		Origin:    h.origin,
		Seq:       seq,
		Revision:  revision,
		Text:      text,
	})
	if err != nil {
		return err
	}

	logger.RDAgent.Infof(
		"Sending clipboard_sync to viewer: seq=%d revision=%s bytes=%d",
		seq,
		revision,
		len([]byte(text)),
	)

	return sender.SendText(string(raw))
}

func (h *Handler) sendClipboardSnapshot(sender Sender) error {
	text, revision, err := h.clipboard.GetText()
	if err != nil {
		return err
	}

	text, err = sanitizeClipboardText(text)
	if err != nil {
		return err
	}

	seq := h.seq.Add(1)

	raw, err := json.Marshal(Message{
		Type:      "clipboard_sync",
		ID:        fmt.Sprintf("agent-%d", seq),
		SessionID: h.sessionID,
		Origin:    h.origin,
		Seq:       seq,
		Revision:  revision,
		Text:      text,
	})
	if err != nil {
		return err
	}

	return sender.SendText(string(raw))
}

func (h *Handler) SendClipboardSnapshot(sender Sender) error {
	return h.sendClipboardSnapshot(sender)
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
		text, err := sanitizeClipboardText(msg.Text)
		if err != nil {
			return err
		}

		_, err = h.clipboard.SetText(text, msg.Origin, msg.Seq, msg.Revision)
		if err != nil {
			return err
		}
		return nil

	case "clipboard_get":
		return h.sendClipboardSnapshot(dc)

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
