// Command hls-monitor-agent checks HLS streams from a remote location on
// behalf of an hls-monitor server. It needs only outgoing access to the
// server and to the streams.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hotid/hls-monitor/internal/agent"
	"github.com/hotid/hls-monitor/internal/envflag"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var serverURL, token, keyFile, logLevel string
	fs := flag.NewFlagSet("hls-monitor-agent", flag.ExitOnError)
	envflag.String(fs, &serverURL, "server-url", "HLSMON_SERVER_URL", "", "URL of the hls-monitor server, e.g. https://monitor.example.com")
	envflag.String(fs, &token, "enroll-token", "HLSMON_ENROLL_TOKEN", "", "one-time enrollment token; needed only on the first start")
	envflag.String(fs, &keyFile, "key-file", "HLSMON_KEY_FILE", "data/agent.key", "file where the agent key is kept")
	envflag.String(fs, &logLevel, "log-level", "HLSMON_LOG_LEVEL", "info", "debug, info, warn or error")
	showVersion := fs.Bool("version", false, "print the version and exit")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println(version)
		return
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("hls-monitor agent starting", "version", version, "server", serverURL)
	err := agent.Run(ctx, agent.Options{ServerURL: serverURL, EnrollToken: token, KeyFile: keyFile, Version: version, Log: log})
	if err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("stopped")
}
