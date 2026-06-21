package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

type PlainHandler struct {
	mu    sync.Mutex
	w     io.Writer
	level slog.Level
}

func NewPlainHandler(w io.Writer, level slog.Level) *PlainHandler {
	return &PlainHandler{
		w:     w,
		level: level,
	}
}

func (h *PlainHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *PlainHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	ts := r.Time.Format("2006-01-02 15:04:05,000")
	level := strings.ToUpper(r.Level.String())
	msg := r.Message

	line := fmt.Sprintf("[%s] [%s] %s\n", ts, level, msg)
	_, err := h.w.Write([]byte(line))
	return err
}

func (h *PlainHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h *PlainHandler) WithGroup(_ string) slog.Handler {
	return h
}
