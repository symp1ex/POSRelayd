package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type RotatingWriter struct {
	mu         sync.Mutex
	file       *os.File
	name       string
	currentDay string
}

func NewRotatingWriter(name string) *RotatingWriter {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		panic(err)
	}

	w := &RotatingWriter{name: name}

	path := filepath.Join(logDir, fmt.Sprintf("%s.log", name))
	if info, err := os.Stat(path); err == nil {
		fileDay := info.ModTime().Format("2006-01-02")
		today := time.Now().Format("2006-01-02")
		if fileDay != today {
			newPath := filepath.Join(logDir, fmt.Sprintf("%s.log.%s", name, fileDay))
			_ = os.Rename(path, newPath)
		}
	}

	w.rotateIfNeeded()
	return w
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.rotateIfNeeded()
	return w.file.Write(p)
}

func (w *RotatingWriter) rotateIfNeeded() {
	today := time.Now().Format("2006-01-02")
	if w.file != nil && w.currentDay == today {
		return
	}

	if w.file != nil {
		_ = w.file.Close()

		oldPath := filepath.Join(logDir, fmt.Sprintf("%s.log", w.name))
		newPath := filepath.Join(logDir, fmt.Sprintf("%s.log.%s", w.name, w.currentDay))
		_ = os.Rename(oldPath, newPath)
	}

	path := filepath.Join(logDir, fmt.Sprintf("%s.log", w.name))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}

	w.file = file
	w.currentDay = today

	w.cleanupOldLogs()
}

func (w *RotatingWriter) cleanupOldLogs() {
	files, _ := filepath.Glob(filepath.Join(logDir, fmt.Sprintf("%s.log.*", w.name)))
	cutoff := time.Now().AddDate(0, 0, -retainDays)

	for _, f := range files {
		info, err := os.Stat(f)
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(f)
		}
	}
}
