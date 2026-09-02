// snowbrawl-server — сервер мультиплеера SnowBrawl: отдаёт клиент, держит WebSocket,
// сводит игроков в матчи и исполняет общую симуляцию sim.js.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/admin"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/sim"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/web"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// buildVersion подставляется при сборке: -ldflags "-X main.buildVersion=v1.2.3-abcdef".
var buildVersion = "dev"

func main() {
	if err := run(); err != nil {
		l := zerolog.New(os.Stderr).With().Timestamp().Logger()
		l.Fatal().Err(err).Msg("server failed")
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], buildVersion)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	started := time.Now()

	fsys, err := web.FS(cfg.WebDir)
	if err != nil {
		return err
	}
	simSrc, err := web.ReadSim(fsys)
	if err != nil {
		return err
	}
	prog, err := sim.Compile(simSrc)
	if err != nil {
		return err
	}
	log.Info().Str("build", cfg.BuildVersion).Str("sim", prog.Version()).Int("proto", protocol.Version).
		Str("addr", cfg.Addr).Bool("webFromDisk", cfg.WebDir != "").Msg("starting snowbrawl-server")

	h := hub.New(cfg, prog, log)
	h.Run()
	wsServer := ws.NewServer(ws.Options{MaxConns: cfg.MaxConns, MsgRate: cfg.MsgRate, TrustProxy: cfg.TrustProxy, Log: log}, h)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(log))
	r.GET("/ws", gin.WrapH(wsServer))
	admin.Register(r, h, admin.Info{Build: cfg.BuildVersion, SimVersion: prog.Version(), Proto: protocol.Version}, cfg.AdminToken, started)
	web.Register(r, fsys, cfg.WebDir != "", cfg.BuildVersion)

	srv := &http.Server{Addr: cfg.Addr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info().Msg("shutting down")
	h.Shutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("http shutdown")
	}
	wsServer.Wait()
	log.Info().Msg("bye")
	return nil
}

func newLogger(cfg config.Config) zerolog.Logger {
	lvl, err := zerolog.ParseLevel(strings.ToLower(cfg.LogLevel))
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	var out = os.Stdout
	var log zerolog.Logger
	if cfg.LogPretty {
		log = zerolog.New(zerolog.ConsoleWriter{Out: out, TimeFormat: "15:04:05"})
	} else {
		log = zerolog.New(out)
	}
	return log.Level(lvl).With().Timestamp().Logger()
}

// requestLogger пишет только ошибки и медленные запросы: статика и снапшоты идут мимо логов.
func requestLogger(log zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		status := c.Writer.Status()
		dur := time.Since(start)
		if status >= 500 || (status >= 400 && status != 404) || dur > 2*time.Second {
			log.Warn().Int("status", status).Str("method", c.Request.Method).Str("path", c.Request.URL.Path).
				Dur("dur", dur).Str("ip", c.ClientIP()).Msg("http")
		}
	}
}
