package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
)

// File logging for post-mortems: every run, request, tool call, retry and
// panic lands in ~/.sigmoidh/logs/sigmoidh.log as JSON lines. The logger is
// installed once from main; before that (and in tests) helpers are no-ops.

const logMaxBytes = 8 << 20 // rotate at 8 MiB

var logger atomic.Pointer[slog.Logger]

func init() {
	logger.Store(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

// setupLogging opens the log file (rotating a previous one aside) and installs
// the global slog logger. Returned closer flushes nothing (unbuffered) but
// closes the file; safe to call once from main.
func setupLogging(appDir string, debug bool) (func(), error) {
	dir := filepath.Join(appDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, err
	}
	path := filepath.Join(dir, "sigmoidh.log")
	if info, err := os.Stat(path); err == nil && info.Size() > logMaxBytes {
		_ = os.Rename(path, path+".old") // best effort: keep newest writable
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return func() {}, err
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger.Store(slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})))
	return func() { f.Close() }, nil
}

func logDebug(msg string, args ...any) { logger.Load().Debug(msg, args...) }
func logInfo(msg string, args ...any)  { logger.Load().Info(msg, args...) }
func logWarn(msg string, args ...any)  { logger.Load().Warn(msg, args...) }
func logError(msg string, args ...any) { logger.Load().Error(msg, args...) }

// logPanic records a recovered panic with its stack so crashes leave a trail.
func logPanic(stage string, r any, stack []byte) {
	logError("panic recovered", "stage", stage, "panic", r, "stack", string(stack))
}
