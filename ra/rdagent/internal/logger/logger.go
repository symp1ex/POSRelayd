package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

var (
	loggers = make(map[string]*slog.Logger)
	mu      sync.Mutex

	RDAgent Logger

	logDir     = "logs"
	retainDays = 14
	logLevel   = "INFO"
)

type Logger struct {
	*slog.Logger
}

func Configure(dir string, level string, retain int) {
	mu.Lock()
	if dir != "" {
		logDir = dir
	}
	if level != "" {
		logLevel = level
	}
	if retain > 0 {
		retainDays = retain
	}

	loggers = make(map[string]*slog.Logger)
	mu.Unlock()

	RDAgent = Logger{Get("rd-agent")}
}

func levelFromString(level string) slog.Level {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARNING", "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func (l Logger) Infof(format string, args ...any) {
	l.logf(slog.LevelInfo, format, args...)
}

func (l Logger) Debugf(format string, args ...any) {
	l.logf(slog.LevelDebug, format, args...)
}

func (l Logger) Warnf(format string, args ...any) {
	l.logf(slog.LevelWarn, format, args...)
}

func (l Logger) Errorf(format string, args ...any) {
	l.logf(slog.LevelError, format, args...)
}

func (l Logger) logf(level slog.Level, format string, args ...any) {
	if l.Logger == nil {
		return
	}

	ctx := context.Background()
	if !l.Enabled(ctx, level) {
		return
	}

	l.Log(ctx, level, fmt.Sprintf(format, args...))
}

func Get(name string) *slog.Logger {
	mu.Lock()
	defer mu.Unlock()

	if l, ok := loggers[name]; ok {
		return l
	}

	writer := NewRotatingWriter(name)
	handler := NewPlainHandler(writer, levelFromString(logLevel))

	l := slog.New(handler)
	loggers[name] = l
	return l
}
