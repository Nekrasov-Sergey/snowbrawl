// Package auth — вход по Яндекс ID. Гостевой режим он не отменяет: аккаунт нужен тому, кто
// хочет один и тот же ник и прогресс на телефоне и на компьютере.
//
// Поток обычный authorization code: /auth/yandex уводит игрока на Яндекс, /auth/yandex/callback
// принимает код, меняет его на токен, читает профиль и ставит куку входа (см. cookie.go).
// Возврат — редиректом на саму игру: страница перезагружается, открывает новый WebSocket, и
// сервер узнаёт аккаунт по куке ещё до первого сообщения. Поэтому связывать открытый сокет с
// HTTP-логином не нужно ничем — ни одноразовыми кодами, ни каналом между вкладками.
//
// Если ClientID или секрет пусты, пакет не регистрирует ни одной ручки: вход просто выключен,
// как выключена админка при пустом токене.
package auth

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Config — настройки входа. Адреса провайдера вынесены в поля, чтобы тесты подставляли
// httptest-сервер вместо Яндекса.
type Config struct {
	ClientID     string
	ClientSecret string
	PublicURL    string // https://snowbrawl.ru
	Secret       string // ключ подписи кук; пустой — берётся из KeyFile (и создаётся там же)
	KeyFile      string // куда класть сгенерированный ключ
	AuthURL      string
	TokenURL     string
	InfoURL      string
	DevLogin     bool // /auth/dev/login — только для разработки
	TrustProxy   bool
}

// Enabled — включён ли вход. Проверять до New: без ClientID сервис не нужен.
func Enabled(c Config) bool { return c.ClientID != "" && c.ClientSecret != "" }

// Service — состояние входа: настройки, аккаунты, лимитер, ключ подписи.
type Service struct {
	cfg         Config
	accs        *accounts.Store
	http        *http.Client
	log         zerolog.Logger
	secret      []byte
	redirectURI string
	secure      bool
	starts      *limiter
	callbacks   *limiter
	now         func() time.Time
	// nickTaken сообщает, занят ли ник кем-то вне стора аккаунтов (живой бронью гостя).
	// Ставит хаб; пока не задан — считаем, что не занят.
	nickTaken func(key string) bool
}

// New готовит сервис: разбирает публичный адрес, добывает ключ подписи, подставляет боевые
// адреса провайдера вместо пустых.
func New(cfg Config, accs *accounts.Store, log zerolog.Logger) (*Service, error) {
	if !Enabled(cfg) {
		return nil, errors.New("auth: вход выключен (нет client id или секрета)")
	}
	pub, err := url.Parse(strings.TrimSuffix(cfg.PublicURL, "/"))
	if err != nil || pub.Host == "" {
		return nil, errors.New("auth: SNOWBRAWL_PUBLIC_URL должен быть полным адресом игры")
	}
	if cfg.AuthURL == "" {
		cfg.AuthURL = YandexAuthURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = YandexTokenURL
	}
	if cfg.InfoURL == "" {
		cfg.InfoURL = YandexInfoURL
	}
	secret, err := resolveSecret(cfg, log)
	if err != nil {
		return nil, err
	}
	return &Service{
		cfg: cfg, accs: accs, http: newHTTPClient(), log: log, secret: secret,
		redirectURI: pub.String() + "/auth/yandex/callback",
		secure:      pub.Scheme == "https",
		starts:      newLimiter(10, time.Minute),
		callbacks:   newLimiter(20, time.Minute),
		now:         time.Now,
	}, nil
}

// SetNickTaken сообщает сервису, как узнать про ники, занятые гостями. Колбэк обязан быть
// быстрым и не брать мьютекс хаба: его зовут из HTTP-обработчика под мьютексом стора аккаунтов.
func (s *Service) SetNickTaken(fn func(key string) bool) { s.nickTaken = fn }

// resolveSecret добывает ключ подписи кук. Из конфига — если задан; иначе из файла рядом с
// аккаунтами, а если и файла нет, генерируем и сохраняем. Ключ обязан пережить деплой: иначе
// каждая выкладка разлогинивала бы всех.
func resolveSecret(cfg Config, log zerolog.Logger) ([]byte, error) {
	if cfg.Secret != "" {
		return []byte(cfg.Secret), nil
	}
	if cfg.KeyFile == "" {
		// Без файла ключ живёт до перезапуска — приемлемо только в разработке, но сказать об
		// этом надо вслух: иначе «почему меня разлогинивает» будет загадкой.
		log.Warn().Msg("auth: ключ подписи не задан и негде хранить — вход будет слетать при перезапуске")
		return []byte(randomHex(32)), nil
	}
	if data, err := os.ReadFile(cfg.KeyFile); err == nil && len(data) >= 32 { //nolint:gosec // путь из конфига
		return data, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	key := []byte(randomHex(32))
	if err := os.MkdirAll(filepath.Dir(cfg.KeyFile), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfg.KeyFile, key, 0o600); err != nil {
		return nil, err
	}
	log.Info().Str("path", cfg.KeyFile).Msg("auth: создан ключ подписи кук входа")
	return key, nil
}

// Register вешает ручки входа на роутер.
func (s *Service) Register(r *gin.Engine) {
	r.GET("/auth/yandex", s.start)
	r.GET("/auth/yandex/callback", s.callback)
	r.POST("/auth/logout", s.logout)
	if s.cfg.DevLogin {
		// Вход без провайдера: нужен, чтобы проверять игру локально, не выходя в сеть и не
		// держа секреты на машине. Конфиг не даёт включить это в боевой сборке.
		r.GET("/auth/dev/login", s.devLogin)
		s.log.Warn().Msg("auth: включён офлайновый вход /auth/dev/login — только для разработки")
	}
}

// Identify — accountID по куке запроса или пустая строка. Этим сервер узнаёт игрока на апгрейде
// /ws: кука приходит сама, потому что сокет открывается на тот же origin.
func (s *Service) Identify(r *http.Request) string {
	if s == nil {
		return ""
	}
	c, err := r.Cookie(CookieSession)
	if err != nil || c.Value == "" {
		return ""
	}
	id, epoch, err := parseSession(s.secret, c.Value, s.now())
	if err != nil {
		return ""
	}
	acc, ok := s.accs.Get(id)
	if !ok || acc.Epoch != epoch {
		// Аккаунта нет или все куки отозваны («выйти на всех устройствах»).
		return ""
	}
	return id
}

// start уводит игрока на Яндекс.
func (s *Service) start(c *gin.Context) {
	if !s.starts.allow(ws.ClientIP(c.Request, s.cfg.TrustProxy), s.now()) {
		c.String(http.StatusTooManyRequests, "слишком часто, попробуйте через минуту")
		return
	}
	state := randomHex(16)
	s.setCookie(c.Writer, CookieState, stateValue(s.secret, state, returnPath(c.Query("return")), s.now().Add(stateTTL)), stateTTL, true)
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {s.cfg.ClientID},
		"redirect_uri":  {s.redirectURI},
		"state":         {state},
	}
	c.Redirect(http.StatusFound, s.cfg.AuthURL+"?"+q.Encode())
}

// returnPath — куда вернуть игрока после входа. Только путь внутри игры: «//» и абсолютные
// адреса отбрасываем, иначе ручка превращается в открытый редирект.
func returnPath(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return "/"
	}
	return v
}

// callback принимает код авторизации и заводит (или находит) аккаунт.
func (s *Service) callback(c *gin.Context) {
	ip := ws.ClientIP(c.Request, s.cfg.TrustProxy)
	if !s.callbacks.allow(ip, s.now()) {
		c.String(http.StatusTooManyRequests, "слишком часто, попробуйте через минуту")
		return
	}
	// Куку state гасим в любом исходе: она одноразовая.
	defer s.clearCookie(c.Writer, CookieState, true)

	stateCookie, err := c.Request.Cookie(CookieState)
	if err != nil {
		s.fail(c, http.StatusBadRequest, "вход не начинался в этом браузере", nil)
		return
	}
	want, back, err := parseState(s.secret, stateCookie.Value, s.now())
	if err != nil || want == "" || want != c.Query("state") {
		s.log.Warn().Str("ip", ip).Msg("auth: state не совпал")
		s.fail(c, http.StatusBadRequest, "вход не подтверждён, попробуйте ещё раз", nil)
		return
	}
	if e := c.Query("error"); e != "" {
		// Игрок нажал «Отказать» — это не ошибка сервера, возвращаем его в игру молча.
		s.log.Info().Str("error", e).Msg("auth: игрок отказал в доступе")
		c.Redirect(http.StatusFound, back)
		return
	}
	code := c.Query("code")
	if code == "" {
		s.fail(c, http.StatusBadRequest, "вход не подтверждён, попробуйте ещё раз", nil)
		return
	}

	token, err := s.exchange(c.Request.Context(), code)
	if err != nil {
		s.fail(c, http.StatusBadGateway, "Яндекс не ответил, попробуйте ещё раз", err)
		return
	}
	prof, err := s.fetchProfile(c.Request.Context(), token)
	if err != nil {
		s.fail(c, http.StatusBadGateway, "не удалось прочитать профиль, попробуйте ещё раз", err)
		return
	}
	acc, created, err := s.accs.Ensure(accounts.ProviderYandex, prof.ID, prof.Login, prof.nickSuggestion(), s.nickTaken, s.now())
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "не удалось создать аккаунт", err)
		return
	}
	// Вход пишем на диск сразу: фоновый флаш потерял бы новый аккаунт при падении, и игрок
	// после повторного входа получил бы второй, уже с другим ником.
	if err := s.accs.Flush(); err != nil {
		s.log.Error().Err(err).Msg("auth: аккаунт не записан на диск")
	}
	s.issue(c.Writer, acc)
	s.log.Info().Str("account", acc.ID).Str("provider", accounts.ProviderYandex).
		Bool("created", created).Str("nick", acc.Nick).Msg("auth: вход")
	c.Redirect(http.StatusFound, back)
}

// issue выдаёт куки входа: подписанную и подсказку для клиента.
func (s *Service) issue(w http.ResponseWriter, acc accounts.Account) {
	exp := s.now().Add(SessionTTL)
	s.setCookie(w, CookieSession, sessionValue(s.secret, acc.ID, acc.Epoch, exp), SessionTTL, true)
	s.setCookie(w, CookieHint, acc.ID, SessionTTL, false)
}

// logout снимает куки. POST, чтобы чужая картинка на постороннем сайте не разлогинивала игрока.
func (s *Service) logout(c *gin.Context) {
	s.clearCookie(c.Writer, CookieSession, true)
	s.clearCookie(c.Writer, CookieHint, false)
	c.Status(http.StatusNoContent)
}

// devLogin пускает под любым аккаунтом без провайдера. Существует только в dev-сборке
// (см. config.Load), нужен для проверки игры без выхода в сеть.
func (s *Service) devLogin(c *gin.Context) {
	sub := c.DefaultQuery("sub", "dev")
	acc, _, err := s.accs.Ensure(accounts.ProviderYandex, "dev:"+sub, "dev-"+sub, c.Query("nick"), s.nickTaken, s.now())
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "не удалось создать аккаунт", err)
		return
	}
	if err := s.accs.Flush(); err != nil {
		s.log.Error().Err(err).Msg("auth: аккаунт не записан на диск")
	}
	s.issue(c.Writer, acc)
	c.Redirect(http.StatusFound, returnPath(c.Query("return")))
}

// Refresh продлевает куку входа, когда до конца её срока осталось меньше месяца. Иначе игрок,
// заходящий регулярно, однажды всё равно окажется разлогинен.
func (s *Service) Refresh() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Апгрейд сокета не трогаем: заголовки там пишет websocket.Accept.
		if s == nil || c.Request.URL.Path == "/ws" {
			c.Next()
			return
		}
		cookie, err := c.Request.Cookie(CookieSession)
		if err == nil && cookie.Value != "" {
			if id, epoch, perr := parseSession(s.secret, cookie.Value, s.now()); perr == nil {
				if acc, ok := s.accs.Get(id); ok && acc.Epoch == epoch && s.expiringSoon(cookie.Value) {
					s.issue(c.Writer, acc)
				}
			}
		}
		c.Next()
	}
}

// expiringSoon — куке осталось жить меньше refreshBefore.
func (s *Service) expiringSoon(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 5 {
		return false
	}
	unix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return false
	}
	return time.Unix(unix, 0).Sub(s.now()) < refreshBefore
}

// fail отвечает игроку человеческим текстом, а подробности оставляет в логе: в тексте страницы
// не должно быть ничего про код, токен и устройство сервера.
func (s *Service) fail(c *gin.Context, status int, msg string, err error) {
	if err != nil {
		s.log.Warn().Err(err).Str("path", c.Request.URL.Path).Msg("auth: вход не удался")
	}
	c.String(status, msg)
}
