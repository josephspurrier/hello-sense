// Command orb is the consolidated Hello Sense backend.
//
// Today it serves the device edge only. The app API and the worker loops are
// phases 4 and 3 of knowledgebase/CONSOLIDATION-PLAN.md and will run in this
// same binary as separate goroutines, which is the point: one process instead
// of eleven JVMs.
//
// It expects to sit behind a TLS terminator. Go cannot complete the Sense's
// handshake (phase 0 of the plan proved it), so sense_server.py keeps that job.
//
// Run it in shadow mode first:
//
//	orb -addr :8081 -shadow
//
// which parses, verifies and logs every request without writing anything, so it
// can be pointed at real traffic while the existing stack stays authoritative.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/api"
	"github.com/josephspurrier/hello-orb/orb/internal/edge"
	"github.com/josephspurrier/hello-orb/orb/internal/ota"
	"github.com/josephspurrier/hello-orb/orb/internal/push"
	"github.com/josephspurrier/hello-orb/orb/internal/scoring"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
	"github.com/josephspurrier/hello-orb/orb/internal/timeline"
	"github.com/josephspurrier/hello-orb/orb/internal/worker"
)

func main() {
	var (
		addr     = flag.String("addr", ":8081", "listen address")
		apiAddr  = flag.String("api-addr", "", "app API listen address; empty disables it")
		apiFwd   = flag.String("api-fallback", os.Getenv("ORB_API_FALLBACK"), "forward unimplemented app API paths here, e.g. http://127.0.0.1:9997; empty means 404")
		dsn      = flag.String("dsn", envOr("ORB_DSN", "postgres://hello:hello@localhost:5432/orb"), "Postgres DSN")
		shadow   = flag.Bool("shadow", false, "parse and verify but never write; for validating against live traffic")
		algoURL  = flag.String("algo", os.Getenv("ORB_ALGO_URL"), "timeline algorithm service URL; empty disables scoring")
		noWorker = flag.Bool("no-worker", false, "serve the edge only, run no periodic jobs")
		fwDir    = flag.String("firmware-dir", os.Getenv("ORB_FIRMWARE_DIR"),
			"directory of OTA images to serve at /firmware/{name}; empty disables it")
		otaWindow = flag.String("ota-window", envOr("ORB_OTA_WINDOW", "2-5"),
			"hours in the device's local time when an update may be offered, as START-END")
		otaMinUptime = flag.Duration("ota-min-uptime", durationOr("ORB_OTA_MIN_UPTIME", ota.MinUptime),
			"how long a device must have been running before an update is offered")
		debug = flag.Bool("debug", false, "debug logging")

		// Apple push. All four are required together; with any missing, push is
		// simply off. Defaults come from the environment so the signing key's
		// path never has to appear in a command line or a plist.
		apnsKey   = flag.String("apns-key", os.Getenv("ORB_APNS_KEY"), "path to the APNs .p8 signing key; empty disables push")
		apnsKeyID = flag.String("apns-key-id", os.Getenv("ORB_APNS_KEY_ID"), "APNs key id")
		apnsTeam  = flag.String("apns-team", os.Getenv("ORB_APNS_TEAM"), "Apple developer team id")
		apnsTopic = flag.String("apns-topic", os.Getenv("ORB_APNS_TOPIC"), "the app's bundle id")
		apnsProd  = flag.Bool("apns-production", false, "use the production APNs host; a development build needs the sandbox")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, *dsn)
	if err != nil {
		log.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	h := edge.New(st, log)
	h.ReadOnly = *shadow
	// Off unless asked for. Serving firmware is the most consequential thing
	// this process can do, so it takes a deliberate flag rather than a
	// directory happening to exist.
	h.FirmwareDir = *fwDir
	if *fwDir != "" {
		log.Warn("firmware serving ENABLED", "dir", *fwDir)
	}
	if w, err := parseOTAWindow(*otaWindow); err != nil {
		log.Error("bad -ota-window, keeping the default", "value", *otaWindow, "err", err)
	} else {
		h.OTAPolicy.Window = w
	}
	h.OTAPolicy.MinUptime = *otaMinUptime
	if h.OTAPolicy != ota.DefaultPolicy {
		// Worth shouting about on every start. These gates exist so an update
		// cannot begin while the owner is asleep, and cannot be handed to a
		// device that may be rebooting in a loop.
		log.Warn("OTA policy loosened from the defaults",
			"window_start", h.OTAPolicy.Window.StartHour,
			"window_end", h.OTAPolicy.Window.EndHour,
			"min_uptime", h.OTAPolicy.MinUptime)
	}

	// The scorer is built here rather than inside the worker, because the app
	// API needs it too: a timeline correction re-scores its night inside the
	// request that made it. Sharing one scorer is what keeps a night scored by
	// the timer and a night scored by a correction identical.
	var algo timeline.Algorithm
	if *algoURL != "" {
		algo = timeline.NewHTTPClient(*algoURL, 60*time.Second)
	}
	scorer := scoring.New(st, algo, log)

	// The worker shares this process with the edge. That is the whole point of
	// the consolidation: seven JVM containers become goroutines. It is also why
	// a job must never panic the process, see worker.loop.
	if !*noWorker && !*shadow {
		wk := worker.New(st, algo, log, worker.Config{})

		if *apnsKey != "" {
			pc, err := push.New(push.Config{
				KeyPath: *apnsKey, KeyID: *apnsKeyID,
				TeamID: *apnsTeam, Topic: *apnsTopic,
				Host: apnsHost(*apnsProd),
			})
			if err != nil {
				// Fatal rather than a warning. Push being configured but broken
				// is a state somebody intended and would not notice: the job
				// would run every fifteen minutes and quietly send nothing.
				log.Error("apns", "err", err)
				os.Exit(1)
			}
			wk = wk.WithPush(pc)
			log.Info("push enabled", "topic", *apnsTopic, "host", apnsHost(*apnsProd))
		} else {
			log.Info("push disabled; no signing key configured")
		}

		go wk.Run(ctx)
	} else {
		log.Info("worker disabled", "shadow", *shadow, "no_worker", *noWorker)
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: h.Routes(),
		// The messeji long-poll holds a request open for ~10s, so the read and
		// write timeouts must clear that with room to spare or every poll is
		// killed mid-flight.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("orb edge listening", "addr", *addr, "shadow", *shadow)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	// The app API is a second listener rather than more Host-based routing on
	// the edge. The edge answers one device over a signed protobuf envelope;
	// this answers a phone over a bearer token. Sharing a port would mean one
	// mux where an authentication mistake on either side is reachable from the
	// other. Off unless asked for, so it cannot be exposed by accident.
	var apiSrv *http.Server
	if *apiAddr != "" {
		apiHandler := api.New(st, scorer, log)
		if *apiFwd != "" {
			apiHandler, err = apiHandler.WithFallback(*apiFwd)
			if err != nil {
				log.Error("api fallback", "err", err)
				os.Exit(1)
			}
			log.Info("app API fallback enabled", "upstream", *apiFwd)
		}
		apiSrv = &http.Server{
			Addr:              *apiAddr,
			Handler:           apiHandler,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		go func() {
			log.Info("orb app API listening", "addr", *apiAddr)
			if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("api listen", "err", err)
				os.Exit(1)
			}
		}()
	}

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	if apiSrv != nil {
		if err := apiSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("api shutdown", "err", err)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// apnsHost picks the APNs host. A development build's device token is only
// valid against the sandbox, and sending it to production returns
// BadDeviceToken, which reads as a bad token rather than a wrong host.
func apnsHost(production bool) string {
	if production {
		return push.HostProduction
	}
	return push.HostSandbox
}

// parseOTAWindow reads "START-END" as hours in the device's local time.
func parseOTAWindow(v string) (ota.Window, error) {
	var start, end int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d-%d", &start, &end); err != nil {
		return ota.Window{}, fmt.Errorf("want START-END, e.g. 2-5: %w", err)
	}
	if start < 0 || start > 23 || end < 0 || end > 23 {
		return ota.Window{}, fmt.Errorf("hours must be 0-23, got %d-%d", start, end)
	}
	return ota.Window{StartHour: start, EndHour: end}, nil
}

// durationOr reads a duration from the environment, falling back on the default
// when unset or unparseable.
func durationOr(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
