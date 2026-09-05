// Package config собирает настройки сервера из флагов и переменных окружения.
// Флаг имеет приоритет над переменной окружения, переменная — над значением по умолчанию.
package config

import (
	"flag"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
)

// Config — настройки процесса сервера.
type Config struct {
	Addr         string        // адрес прослушивания, напр. ":8080"
	AdminToken   string        // токен для /admin/*; пустой — админка выключена
	WebDir       string        // каталог клиента на диске; пустой — встроенная статика
	MaxConns     int           // максимум одновременных WebSocket-соединений
	LogLevel     string        // debug|info|warn|error
	LogPretty    bool          // человекочитаемые логи (для разработки)
	BuildVersion string        // версия сборки, подставляется при линковке
	TickRate     int           // тиков симуляции в секунду
	QueueWait    time.Duration // ожидание живых игроков в Quick Match до добора ботами
	ReconnectTTL time.Duration // сколько держим место за отключившимся игроком
	AFKTimeout   time.Duration // без ввода столько — бойца ведёт бот
	RoomTTL      time.Duration // пустая комната живёт столько
	RoomsPerIP   int           // живых комнат на один IP
	MsgRate      int           // сообщений в секунду на соединение
	DrainTimeout time.Duration // сколько дренаж ждёт окончания матчей
	TrustProxy   bool          // брать IP клиента из X-Forwarded-For (за Caddy/nginx)
}

// Defaults возвращает конфигурацию по умолчанию.
func Defaults() Config {
	return Config{
		Addr:         ":8080",
		MaxConns:     200,
		LogLevel:     "info",
		BuildVersion: "dev",
		TickRate:     20,
		QueueWait:    10 * time.Second,
		ReconnectTTL: 60 * time.Second,
		AFKTimeout:   20 * time.Second,
		RoomTTL:      10 * time.Minute,
		RoomsPerIP:   3,
		MsgRate:      30,
		DrainTimeout: 3 * time.Minute,
	}
}

// Load читает окружение и аргументы командной строки.
func Load(args []string, buildVersion string) (Config, error) {
	c := Defaults()
	if buildVersion != "" {
		c.BuildVersion = buildVersion
	}
	envStr(&c.Addr, "SNOWBRAWL_ADDR")
	envStr(&c.AdminToken, "SNOWBRAWL_ADMIN_TOKEN")
	envStr(&c.WebDir, "SNOWBRAWL_WEB_DIR")
	envStr(&c.LogLevel, "SNOWBRAWL_LOG_LEVEL")
	if err := envInt(&c.MaxConns, "SNOWBRAWL_MAX_CONNS"); err != nil {
		return c, err
	}
	if err := envInt(&c.RoomsPerIP, "SNOWBRAWL_ROOMS_PER_IP"); err != nil {
		return c, err
	}
	if err := envInt(&c.MsgRate, "SNOWBRAWL_MSG_RATE"); err != nil {
		return c, err
	}
	if err := envBool(&c.LogPretty, "SNOWBRAWL_LOG_PRETTY"); err != nil {
		return c, err
	}
	if err := envBool(&c.TrustProxy, "SNOWBRAWL_TRUST_PROXY"); err != nil {
		return c, err
	}
	for _, d := range []struct {
		dst *time.Duration
		key string
	}{
		{&c.QueueWait, "SNOWBRAWL_QUEUE_WAIT"},
		{&c.ReconnectTTL, "SNOWBRAWL_RECONNECT_TTL"},
		{&c.AFKTimeout, "SNOWBRAWL_AFK_TIMEOUT"},
		{&c.RoomTTL, "SNOWBRAWL_ROOM_TTL"},
		{&c.DrainTimeout, "SNOWBRAWL_DRAIN_TIMEOUT"},
	} {
		if err := envDur(d.dst, d.key); err != nil {
			return c, err
		}
	}

	fs := flag.NewFlagSet("snowbrawl-server", flag.ContinueOnError)
	fs.StringVar(&c.Addr, "addr", c.Addr, "адрес прослушивания")
	fs.StringVar(&c.AdminToken, "admin-token", c.AdminToken, "токен админки (/admin/*)")
	fs.StringVar(&c.WebDir, "web-dir", c.WebDir, "каталог клиента на диске вместо встроенного")
	fs.IntVar(&c.MaxConns, "max-conns", c.MaxConns, "максимум WebSocket-соединений")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "уровень логов")
	fs.BoolVar(&c.LogPretty, "log-pretty", c.LogPretty, "человекочитаемые логи")
	fs.BoolVar(&c.TrustProxy, "trust-proxy", c.TrustProxy, "брать IP из X-Forwarded-For")
	fs.DurationVar(&c.QueueWait, "queue-wait", c.QueueWait, "ожидание игроков в Quick Match")
	fs.DurationVar(&c.ReconnectTTL, "reconnect-ttl", c.ReconnectTTL, "время на переподключение")
	fs.DurationVar(&c.AFKTimeout, "afk-timeout", c.AFKTimeout, "таймаут бездействия")
	if err := fs.Parse(args); err != nil {
		return c, errors.Wrap(err, "parse flags")
	}
	if c.TickRate <= 0 || c.TickRate > 60 {
		return c, errors.Errorf("bad tick rate %d", c.TickRate)
	}
	if c.MaxConns <= 0 {
		return c, errors.New("max-conns must be positive")
	}
	return c, nil
}

func envStr(dst *string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func envInt(dst *int, key string) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return errors.Wrapf(err, "env %s", key)
	}
	*dst = n
	return nil
}

func envBool(dst *bool, key string) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return errors.Wrapf(err, "env %s", key)
	}
	*dst = b
	return nil
}

func envDur(dst *time.Duration, key string) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return errors.Wrapf(err, "env %s", key)
	}
	*dst = d
	return nil
}
