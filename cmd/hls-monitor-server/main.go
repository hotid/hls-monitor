// Command hls-monitor-server is the monitoring server: web interface and
// API, storage, and checks made from the server itself.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hotid/hls-monitor/internal/api"
	"github.com/hotid/hls-monitor/internal/auth"
	"github.com/hotid/hls-monitor/internal/capture"
	"github.com/hotid/hls-monitor/internal/envflag"
	"github.com/hotid/hls-monitor/internal/ingest"
	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/picture"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/runner"
	"github.com/hotid/hls-monitor/internal/selfstat"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/webui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	listen                                         string
	dbPath                                         string
	vmURL                                          string
	adminUser                                      string
	adminPassword                                  string
	publicURL                                      string
	agentImage                                     string
	basePath                                       string
	cookieSecure                                   string
	trustedProxies                                 string
	logLevel                                       string
	sessionTTL                                     time.Duration
	segmentRetention                               time.Duration
	eventRetention                                 time.Duration
	segmentKeep, segmentKeepProblems, journalMaxMB int
	bodyMemoryMB, metricsQueueMB                   int
	privateNetworks                                string
	pictureCheck                                   time.Duration
	ffmpeg                                         string
	capture                                        string
	captureMaxMB                                   int
	captureKeep                                    time.Duration
	snapshotRetention                              time.Duration
	incidentRetention                              time.Duration
}

func parseConfig() config {
	var c config
	fs := flag.NewFlagSet("hls-monitor-server", flag.ExitOnError)
	envflag.String(fs, &c.listen, "listen", "HLSMON_LISTEN", ":4000", "address to listen on")
	envflag.String(fs, &c.dbPath, "db", "HLSMON_DB", "data/hls-monitor.db", "path to the SQLite database file")
	envflag.String(fs, &c.vmURL, "vm-url", "HLSMON_VM_URL", "", "VictoriaMetrics base URL, e.g. http://victoriametrics:8428; empty disables time series")
	envflag.String(fs, &c.adminUser, "admin-user", "HLSMON_ADMIN_USER", "admin", "name of the administrator created on first start")
	envflag.String(fs, &c.adminPassword, "admin-password", "HLSMON_ADMIN_PASSWORD", "", "password of that administrator; generated and logged once when empty")
	envflag.String(fs, &c.publicURL, "public-url", "HLSMON_PUBLIC_URL", "", "URL agents use to reach the server; derived from requests when empty")
	envflag.String(fs, &c.agentImage, "agent-image", "HLSMON_AGENT_IMAGE", "", "image named in the command for starting an agent; the published image of this version when empty")
	envflag.String(fs, &c.basePath, "base-path", "HLSMON_BASE_PATH", "", "URL prefix a reverse proxy publishes the server under and strips, e.g. /hls-monitor")
	envflag.String(fs, &c.cookieSecure, "cookie-secure", "HLSMON_COOKIE_SECURE", "auto", "Secure flag of the session cookie: auto, true or false")
	envflag.String(fs, &c.trustedProxies, "trusted-proxies", "HLSMON_TRUSTED_PROXIES", "", "comma-separated addresses or CIDRs of reverse proxies whose X-Forwarded-For is trusted")
	envflag.String(fs, &c.logLevel, "log-level", "HLSMON_LOG_LEVEL", "info", "debug, info, warn or error")
	envflag.Duration(fs, &c.sessionTTL, "session-ttl", "HLSMON_SESSION_TTL", 7*24*time.Hour, "lifetime of a web session")
	envflag.Duration(fs, &c.segmentRetention, "segment-retention", "HLSMON_SEGMENT_RETENTION", 48*time.Hour, "how long a row of the segment journal is kept at most")
	envflag.Int(fs, &c.segmentKeep, "segment-keep", "HLSMON_SEGMENT_KEEP", 100, "how many journal rows are kept per variant and agent")
	envflag.Int(fs, &c.segmentKeepProblems, "segment-keep-problems", "HLSMON_SEGMENT_KEEP_PROBLEMS", 500, "the same for segments that had problems")
	envflag.Int(fs, &c.journalMaxMB, "journal-max-mb", "HLSMON_JOURNAL_MAX_MB", 512, "size in MiB the segment journal file cannot exceed")
	envflag.Int(fs, &c.metricsQueueMB, "metrics-queue-max-mb", "HLSMON_METRICS_QUEUE_MAX_MB", metrics.DefaultQueueBytes>>20, "MiB of disk for samples VictoriaMetrics has not taken yet, kept next to the database; beyond it the oldest are dropped")
	envflag.Int(fs, &c.bodyMemoryMB, "body-memory-mb", "HLSMON_BODY_MEMORY_MB", probe.DefaultBodyMemory>>20, "MiB of segment bodies the local checks may hold in memory at once; downloads wait for room")
	envflag.String(fs, &c.privateNetworks, "private-networks", "HLSMON_PRIVATE_NETWORKS", "same", "whether local checks may connect to addresses that are not public: same (only for streams whose own address is such), allow or deny")
	envflag.Duration(fs, &c.pictureCheck, "picture-check", "HLSMON_PICTURE_CHECK", 0, "how often the picture and the sound of every stream are looked at, e.g. 5m: a black screen, a picture that stopped, silence; needs ffmpeg; 0 does not look")
	envflag.String(fs, &c.ffmpeg, "ffmpeg", "HLSMON_FFMPEG", "ffmpeg", "the ffmpeg program to decode with, for picture-check")
	envflag.String(fs, &c.capture, "capture", "HLSMON_CAPTURE", "off", "on saves the segments around a finding of the server's own checks, next to the database: the one in question, a few before and after, the playlists; off does not")
	envflag.Int(fs, &c.captureMaxMB, "capture-max-mb", "HLSMON_CAPTURE_MAX_MB", 2048, "MiB of disk all captures may take, those not confirmed yet included; beyond it the oldest are dropped")
	envflag.Duration(fs, &c.captureKeep, "capture-keep", "HLSMON_CAPTURE_KEEP", 7*24*time.Hour, "how long a capture is kept")
	envflag.Duration(fs, &c.eventRetention, "event-retention", "HLSMON_EVENT_RETENTION", 7*24*time.Hour, "how long closed informational incidents are kept")
	envflag.Duration(fs, &c.snapshotRetention, "snapshot-retention", "HLSMON_SNAPSHOT_RETENTION", 7*24*time.Hour, "how long playlist snapshots are kept")
	envflag.Duration(fs, &c.incidentRetention, "incident-retention", "HLSMON_INCIDENT_RETENTION", 90*24*time.Hour, "how long closed incidents are kept")
	showVersion := fs.Bool("version", false, "print the version and exit")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}
	return c
}

func main() {
	cfg := parseConfig()
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.logLevel)); err != nil {
		level = slog.LevelInfo
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if err := run(cfg, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if dir := filepath.Dir(cfg.dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create data directory: %w", err)
		}
	}
	st, err := store.OpenWith(ctx, cfg.dbPath, store.Options{
		SegmentKeep:         cfg.segmentKeep,
		SegmentKeepProblems: cfg.segmentKeepProblems,
		JournalMaxBytes:     int64(cfg.journalMaxMB) << 20,
	})
	if err != nil {
		return err
	}
	defer st.Close()

	if err := ensureAdmin(ctx, st, cfg, log); err != nil {
		return err
	}

	mx := metrics.New(cfg.vmURL, log)
	if !mx.Enabled() {
		log.Warn("no VictoriaMetrics URL configured: charts will be empty, only current state and incidents are kept")
	}
	if err := mx.UseDisk(filepath.Join(filepath.Dir(cfg.dbPath), "metrics-queue"), int64(cfg.metricsQueueMB)<<20); err != nil {
		return fmt.Errorf("open the queue of samples: %w", err)
	}
	ing := ingest.New(st, mx, log)

	bg, cancelBG := context.WithCancel(context.Background())
	defer cancelBG()
	probe.SetBodyMemory(int64(cfg.bodyMemoryMB) << 20)
	if err := probe.SetPrivateNetworks(cfg.privateNetworks); err != nil {
		return err
	}
	local := runner.New(func(ctx context.Context, r *probe.Result) {
		// The request context of a check is gone when the runner stops; a
		// result that was produced is still worth storing.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		// The stream's next cycle waits for this one to be stored: a
		// database that cannot keep up slows the checks down instead of
		// losing what they found.
		for attempt := 1; ; attempt++ {
			err := ing.Apply(actx, store.LocalAgentID, r)
			if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, ingest.ErrStale) {
				return
			}
			if attempt == 3 || actx.Err() != nil {
				log.Error("cannot store check result", "stream", r.TargetID, "attempts", attempt, "error", err)
				return
			}
			select {
			case <-actx.Done():
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
	}, "hls-monitor/"+version, log)
	load := selfstat.Start(bg)
	local.SetOverloaded(load.Overloaded)
	if d := picture.Setup(cfg.pictureCheck, cfg.ffmpeg, log); d != nil {
		local.SetLooker(d, cfg.pictureCheck)
	}
	captures, err := openCaptures(cfg, log)
	if err != nil {
		return err
	}
	if captures != nil {
		local.SetCapturer(captures)
		go captures.Run(bg)
	}

	var proxies []string
	for _, p := range strings.Split(cfg.trustedProxies, ",") {
		if p = strings.TrimSpace(p); p != "" {
			proxies = append(proxies, p)
		}
	}
	basePath := strings.TrimRight(strings.TrimSpace(cfg.basePath), "/")
	if basePath != "" && (!strings.HasPrefix(basePath, "/") || strings.ContainsAny(basePath, " \"'<>?#")) {
		return fmt.Errorf("base path must look like /prefix, got %q", cfg.basePath)
	}
	srv := api.New(api.Config{
		Version:        version,
		SessionTTL:     cfg.sessionTTL,
		CookieSecure:   cfg.cookieSecure,
		PublicURL:      cfg.publicURL,
		AgentImage:     cfg.agentImage,
		BasePath:       basePath,
		TrustedProxies: proxies,
		Load:           load,
		Captures:       captures,
		PlayKey:        playKey(filepath.Join(filepath.Dir(cfg.dbPath), "play.key"), log),
	}, st, mx, ing, local, webui.Handler(basePath), log)
	local.SetSlates(srv.Slates)
	srv.SyncLocal(ctx)

	done := make(chan struct{}, 2)
	go func() { mx.Run(bg); done <- struct{}{} }()
	go func() { housekeeping(bg, st, srv, cfg, log); done <- struct{}{} }()

	httpSrv := &http.Server{
		Addr:              cfg.listen,
		Handler:           srv.Handler(),
		ConnContext:       api.ConnContext,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("hls-monitor server started", "version", version, "listen", cfg.listen, "db", cfg.dbPath, "local_streams", local.Count())

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = httpSrv.Shutdown(sctx)
		cancel()
	}
	local.Stop()
	cancelBG()
	<-done
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// playKey reads the key that signs the addresses given to players, making
// it on the first start. Without a file that can be kept the key lives as
// long as the process, and players open across a restart have to be
// started again.
func playKey(path string, log *slog.Logger) []byte {
	if b, err := os.ReadFile(path); err == nil && len(b) >= 32 {
		return b
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		log.Warn("cannot keep the key for watching streams; open players will stop at every restart", "error", err)
	}
	return key
}

// ensureAdmin creates the first administrator when there are no users.
func ensureAdmin(ctx context.Context, st *store.Store, cfg config, log *slog.Logger) error {
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	password, generated := cfg.adminPassword, false
	if password == "" {
		if password, _, err = auth.NewToken(); err != nil {
			return err
		}
		password, generated = password[:20], true
	}
	if len(password) < auth.MinPasswordLen {
		return fmt.Errorf("the administrator password must be at least %d characters long", auth.MinPasswordLen)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, cfg.adminUser, hash, store.RoleAdmin, time.Now()); err != nil {
		return fmt.Errorf("create administrator: %w", err)
	}
	if generated {
		// Shown once: there is no other way to learn it.
		log.Warn("created the first administrator with a generated password; change it after signing in",
			"username", cfg.adminUser, "password", password)
	} else {
		log.Info("created the first administrator", "username", cfg.adminUser)
	}
	return nil
}

// housekeeping deletes expired data and keeps the local runner in line
// with the stored configuration.
func housekeeping(ctx context.Context, st *store.Store, srv *api.Server, cfg config, log *slog.Logger) {
	purge := time.NewTicker(10 * time.Minute)
	defer purge.Stop()
	sync := time.NewTicker(time.Minute)
	defer sync.Stop()
	watch := time.NewTicker(api.ServerWatchEvery)
	defer watch.Stop()
	doPurge := func() {
		now := time.Now()
		deleted, err := st.Purge(ctx, store.Retention{
			Segments:  cfg.segmentRetention,
			Snapshots: cfg.snapshotRetention,
			Incidents: cfg.incidentRetention,
			Events:    cfg.eventRetention,
		}, now)
		if err != nil && ctx.Err() == nil {
			log.Error("cannot purge expired data", "error", err)
		}
		u := st.Usage(ctx)
		log.Info("storage", "purged_rows", deleted, "purge_took", time.Since(now).Round(time.Millisecond),
			"database_mb", u.DatabaseBytes>>20, "journal_mb", u.JournalBytes>>20, "journal_rows", u.JournalRows, "disk_free_mb", u.DiskFreeBytes>>20)
		if u.DiskTotalBytes > 0 && u.DiskFreeBytes < u.DiskTotalBytes/10 {
			log.Warn("the disk the database is on is almost full", "free_mb", u.DiskFreeBytes>>20, "total_mb", u.DiskTotalBytes>>20)
		}
	}
	doPurge()
	for {
		select {
		case <-ctx.Done():
			return
		case <-purge.C:
			doPurge()
		case <-watch.C:
			srv.WatchServers(ctx)
			srv.RecordSelf()
		case <-sync.C:
			srv.SyncLocal(ctx)
			srv.PurgeSeries(ctx)
		}
	}
}

// openCaptures opens the place the segments around findings are saved
// in; nil when captures are off.
func openCaptures(cfg config, log *slog.Logger) (*capture.Store, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.capture)) {
	case "", "off", "false", "0", "no":
		return nil, nil
	case "on", "true", "1", "yes":
	default:
		return nil, fmt.Errorf("capture must be on or off, got %q", cfg.capture)
	}
	o := capture.Options{Dir: filepath.Join(filepath.Dir(cfg.dbPath), "captures"), MaxBytes: int64(cfg.captureMaxMB) << 20, Keep: cfg.captureKeep, Log: log}
	// What ffprobe and a decoder say of a segment goes with it, where they are.
	if p, err := exec.LookPath("ffprobe"); err == nil {
		o.FFprobe = p
	}
	if p, err := exec.LookPath(cfg.ffmpeg); err == nil {
		o.FFmpeg = p
	}
	s, err := capture.Open(o)
	if err != nil {
		return nil, fmt.Errorf("captures: %w", err)
	}
	log.Info("segments around findings are captured", "dir", o.Dir, "max_mb", cfg.captureMaxMB, "keep", cfg.captureKeep.String(), "ffprobe", o.FFprobe != "")
	return s, nil
}
