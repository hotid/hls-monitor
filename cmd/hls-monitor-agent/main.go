// Command hls-monitor-agent checks HLS streams from a remote location on
// behalf of an hls-monitor server. It needs only outgoing access to the
// server and to the streams.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/hotid/hls-monitor/internal/probe"
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
	var queueDir string
	var queueMaxMB int
	envflag.String(fs, &queueDir, "queue-dir", "HLSMON_QUEUE_DIR", "", "directory for results not delivered yet; \"queue\" next to the key file when empty")
	envflag.Int(fs, &queueMaxMB, "queue-max-mb", "HLSMON_QUEUE_MAX_MB", agent.DefaultQueueBytes>>20, "MiB of disk the undelivered results may take; beyond it the oldest are dropped")
	var bodyMemoryMB int
	envflag.Int(fs, &bodyMemoryMB, "body-memory-mb", "HLSMON_BODY_MEMORY_MB", probe.DefaultBodyMemory>>20, "MiB of segment bodies the checks may hold in memory at once; downloads wait for room")
	var privateNetworks string
	envflag.String(fs, &privateNetworks, "private-networks", "HLSMON_PRIVATE_NETWORKS", "same", "whether checks may connect to addresses that are not public: same (only for streams whose own address is such), allow or deny")
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
	probe.SetBodyMemory(int64(bodyMemoryMB) << 20)
	if err := probe.SetPrivateNetworks(privateNetworks); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	err := agent.Run(ctx, agent.Options{QueueDir: queueDir, QueueBytes: int64(queueMaxMB) << 20, ServerURL: serverURL, EnrollToken: token, KeyFile: keyFile, Version: version, Log: log})
	if err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("stopped")
}
