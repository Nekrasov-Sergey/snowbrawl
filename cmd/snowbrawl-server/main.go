// snowbrawl-server — сервер мультиплеера SnowBrawl: отдаёт клиент, держит WebSocket,
// сводит игроков в матчи и исполняет общую симуляцию sim.js.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/admin"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/auth"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/config"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/onlinestat"
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

	mod, err := moderation.Open(cfg.ModerationFile, log)
	if err != nil {
		return fmt.Errorf("moderation store: %w", err)
	}
	series, err := onlinestat.Open(cfg.OnlineFile, log)
	if err != nil {
		return fmt.Errorf("online series: %w", err)
	}
	series.Run(onlinestat.FlushEvery)
	accs, err := accounts.Open(cfg.AccountsFile, log)
	if err != nil {
		return fmt.Errorf("accounts store: %w", err)
	}
	accs.Run(accounts.FlushEvery)
	// Вход по Яндекс ID. Пустой client id — фича выключена: ручек нет, игра работает как раньше.
	var authSvc *auth.Service
	if auth.Enabled(authConfig(cfg)) {
		authSvc, err = auth.New(authConfig(cfg), accs, log)
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}
		log.Info().Str("publicURL", cfg.PublicURL).Msg("auth: вход по Яндекс ID включён")
	} else {
		log.Info().Msg("auth: вход по Яндекс ID выключен (нет client id или секрета)")
	}

	h := hub.New(cfg, prog, log, mod, series)
	h.SetAccounts(accs)
	if authSvc != nil {
		h.SetAuthInfo(&protocol.AuthInfo{Yandex: true})
		// Новый аккаунт не должен получить имя, под которым прямо сейчас играет гость.
		authSvc.SetNickTaken(h.NickTakenByGuest)
	}
	h.Run()
	wsServer := ws.NewServer(ws.Options{
		MaxConns: cfg.MaxConns, MsgRate: cfg.MsgRate, TrustProxy: cfg.TrustProxy, Log: log,
		// Аккаунт узнаётся по куке на апгрейде: игрок опознан ещё до первого сообщения.
		Identify: authSvc.Identify,
	}, h)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(log))
	if authSvc != nil {
		r.Use(authSvc.Refresh())
		authSvc.Register(r)
	}
	r.GET("/ws", gin.WrapH(wsServer))
	stopAdmin := admin.Register(r, admin.Deps{
		Hub: h, Moderation: mod, Accounts: accs,
		Info:  admin.Info{Build: cfg.BuildVersion, SimVersion: prog.Version(), Proto: protocol.Version},
		Token: cfg.AdminToken, Started: started, TrustProxy: cfg.TrustProxy,
		StreamEvery: cfg.AdminStreamEvery, Identify: authSvc.Identify,
	})
	authMethods := ""
	if authSvc != nil {
		authMethods = "yandex"
	}
	web.Register(r, fsys, cfg.WebDir != "", cfg.BuildVersion, authMethods)

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
	stopAdmin() // отпускаем висящие SSE-соединения админки, иначе они съедят таймаут ниже
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("http shutdown")
	}
	wsServer.Wait()
	// Серию закрываем последней: тик hub уже остановлен (Shutdown ждёт свою горутину), поэтому
	// дописать точку в закрытый файл некому.
	if err := series.Close(); err != nil {
		log.Error().Err(err).Msg("online series close")
	}
	// Аккаунты — по тому же правилу: фоновая горутина уже не нужна, последние изменения
	// (ник, урок, время входа) дописываем сами.
	if err := accs.Close(); err != nil {
		log.Error().Err(err).Msg("accounts close")
	}
	log.Info().Msg("bye")
	return nil
}

// authConfig переводит настройки сервера в настройки пакета входа. Ключ подписи, если он не
// задан переменной окружения, хранится рядом с файлом аккаунтов — в том же томе.
func authConfig(cfg config.Config) auth.Config {
	keyFile := ""
	if cfg.AccountsFile != "" {
		keyFile = filepath.Join(filepath.Dir(cfg.AccountsFile), "auth.key")
	}
	return auth.Config{
		ClientID: cfg.YandexClientID, ClientSecret: cfg.YandexClientSecret,
		PublicURL: cfg.PublicURL, Secret: cfg.AuthSecret, KeyFile: keyFile,
		DevLogin: cfg.AuthDevLogin, TrustProxy: cfg.TrustProxy,
	}
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
