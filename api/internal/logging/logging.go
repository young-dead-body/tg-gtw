// Package logging builds structured JSON loggers on top of zap (ТЗ §17,
// T17.4). Logs are written to stdout in JSON so a log shipper (promtail) can
// forward them to Loki. An optional log file adds a file sink (LOG_FILE).
package logging

import (
	"log"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a production JSON logger. level is one of debug/info/warn/error.
// Logs go to stdout; if file is non-empty they are also appended there.
func New(level string, file string) (*zap.Logger, error) {
	cfg := levelConfig(level)
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(cfg.EncoderConfig),
		zapcore.NewMultiWriteSyncer(os.Stdout),
		cfg.Level,
	)
	if file != "" {
		f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		core = zapcore.NewCore(
			zapcore.NewJSONEncoder(cfg.EncoderConfig),
			zapcore.NewMultiWriteSyncer(os.Stdout, f),
			cfg.Level,
		)
	}
	return zap.New(core), nil
}

// NewStdLogger adapts a zap logger to the standard library *log.Logger used by
// the cron scheduler and legacy code paths.
func NewStdLogger(l *zap.Logger) *log.Logger {
	return zap.NewStdLog(l)
}

func levelConfig(level string) zap.Config {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "warn":
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		cfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	return cfg
}
