// Package auth — кто пришёл: гость или игрок, вошедший через Яндекс ID. И того и другого сервер
// узнаёт по одной подписанной куке (cookie.go) с id записи игрока (internal/accounts), а не по IP:
// у мобильного игрока адрес меняется между загрузками страницы, а кука остаётся.
//
// Гость получает куку сразу, при открытии страницы игры (Refresh), а запись в базе появляется
// только когда он открывает игровой сокет. Боты, которые скачивают страницу и не играют, базу
// не засоряют. Браузер без кук остаётся одноразовым гостем, как было до записей.
//
// Вход — обычный authorization code: /auth/yandex уводит игрока на Яндекс, /auth/yandex/callback
// принимает код, меняет его на токен, читает профиль и превращает гостя этого браузера в аккаунт
// (или сливает его с уже существующим, см. accounts.Link). Возврат — редиректом на саму игру:
// страница перезагружается, открывает новый WebSocket, и сервер узнаёт аккаунт по куке ещё до
// первого сообщения.
//
// Если ClientID или секрет пусты, ручек Яндекса нет, а гостевая кука работает как обычно.
package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/store"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Config — настройки входа. Адреса провайдера вынесены в поля, чтобы тесты подставляли
// httptest-сервер вместо Яндекса.
type Config struct {
	ClientID     string
	ClientSecret string
	PublicURL    string // https://snowbrawl.ru
	Secret       string // ключ подписи кук; пустой — берётся из базы (и создаётся там же)
	AuthURL      string
	TokenURL     string
	InfoURL      string
	DevLogin     bool // /auth/dev/login — только для разработки
	TrustProxy   bool
}

// Enabled — включён ли вход через Яндекс.
func Enabled(c Config) bool { return c.ClientID != "" && c.ClientSecret != "" }

// metaSecret — ключ подписи кук в служебной таблице базы.
const metaSecret = "auth_secret"

// Service — состояние входа: настройки, записи игроков, лимитер, ключ подписи.
type Service struct {
	cfg         Config
	yandex      bool
	accs        *accounts.Store
	http        *http.Client
	log         zerolog.Logger
	secret      []byte
	redirectURI string
	secure      bool
	starts      *limiter
	callbacks   *limiter
	now         func() time.Time
	// nickTaken сообщает, занят ли ник кем-то вне базы (живой бронью гостя без куки).
	// Ставит хаб; пока не задан — считаем, что не занят.
	nickTaken func(key string) bool
}

// New готовит сервис: разбирает публичный адрес, добывает ключ подписи, подставляет боевые
// адреса провайдера вместо пустых. db может быть nil — тогда ключ живёт до перезапуска.
func New(cfg Config, accs *accounts.Store, db *store.DB, log zerolog.Logger) (*Service, error) {
	s := &Service{
		cfg: cfg, yandex: Enabled(cfg), accs: accs, http: newHTTPClient(), log: log,
		starts:    newLimiter(10, time.Minute),
		callbacks: newLimiter(20, time.Minute),
		now:       time.Now,
	}
	if cfg.PublicURL != "" || s.yandex {
		pub, err := url.Parse(strings.TrimSuffix(cfg.PublicURL, "/"))
		if err != nil || pub.Host == "" {
			return nil, errors.New("auth: SNOWBRAWL_PUBLIC_URL должен быть полным адресом игры")
		}
		s.redirectURI = pub.String() + "/auth/yandex/callback"
		s.secure = pub.Scheme == "https"
	}
	if s.cfg.AuthURL == "" {
		s.cfg.AuthURL = YandexAuthURL
	}
	if s.cfg.TokenURL == "" {
		s.cfg.TokenURL = YandexTokenURL
	}
	if s.cfg.InfoURL == "" {
		s.cfg.InfoURL = YandexInfoURL
	}
	secret, err := resolveSecret(cfg, db, log)
	if err != nil {
		return nil, err
	}
	s.secret = secret
	return s, nil
}

// Yandex — включён ли вход через Яндекс.
func (s *Service) Yandex() bool { return s != nil && s.yandex }

// SetNickTaken сообщает сервису, как узнать про ники, занятые гостями без записи. Колбэк обязан
// быть быстрым; его зовут из HTTP-обработчика внутри транзакции базы.
func (s *Service) SetNickTaken(fn func(key string) bool) { s.nickTaken = fn }

// resolveSecret добывает ключ подписи кук. Из конфига — если задан; иначе из базы, а если и там
// нет, генерируем и сохраняем. Ключ обязан пережить деплой: иначе каждая выкладка разлогинивала
// бы всех, а гостей превращала бы в новых игроков.
func resolveSecret(cfg Config, db *store.DB, log zerolog.Logger) ([]byte, error) {
	if cfg.Secret != "" {
		return []byte(cfg.Secret), nil
	}
	if db == nil {
		// Без базы ключ живёт до перезапуска — приемлемо только в тестах, но сказать об этом
		// надо вслух: иначе «почему меня разлогинивает» будет загадкой.
		log.Warn().Msg("auth: ключ подписи не задан и негде хранить — вход будет слетать при перезапуске")
		return []byte(randomHex(32)), nil
	}
	if v, ok, err := db.Meta(metaSecret); err != nil {
		return nil, err
	} else if ok && len(v) >= 32 {
		return []byte(v), nil
	}
	key := randomHex(32)
	if err := db.SetMeta(metaSecret, key); err != nil {
		return nil, err
	}
	log.Info().Msg("auth: создан ключ подписи кук")
	return []byte(key), nil
}

// Register вешает ручки входа на роутер.
func (s *Service) Register(r *gin.Engine) {
	if s.yandex {
		r.GET("/auth/yandex", s.start)
		r.GET("/auth/yandex/callback", s.callback)
	}
	r.POST("/auth/logout", s.logout)
	if s.cfg.DevLogin {
		// Вход без провайдера: нужен, чтобы проверять игру локально, не выходя в сеть и не
		// держа секреты на машине. Конфиг не даёт включить это в боевой сборке.
		r.GET("/auth/dev/login", s.devLogin)
		s.log.Warn().Msg("auth: включён офлайновый вход /auth/dev/login — только для разработки")
	}
}

// Identify — id записи игрока по куке запроса или пустая строка. Этим сервер узнаёт игрока на
// апгрейде /ws: кука приходит сама, потому что сокет открывается на тот же origin.
//
// Кука с id, записи по которому ещё нет, и нулевой эпохой — это гость, который пока не открывал
// игру: запись заведёт хаб. Подделать такую куку нельзя, она подписана.
func (s *Service) Identify(r *http.Request) string {
	id, _, _ := s.identify(r)
	return id
}

// identify — id, запись (если есть) и нашлась ли она.
func (s *Service) identify(r *http.Request) (string, accounts.Account, bool) {
	if s == nil {
		return "", accounts.Account{}, false
	}
	c, err := r.Cookie(CookieSession)
	if err != nil || c.Value == "" {
		return "", accounts.Account{}, false
	}
	id, epoch, err := parseSession(s.secret, c.Value, s.now())
	if err != nil {
		return "", accounts.Account{}, false
	}
	acc, ok := s.accs.Get(id)
	switch {
	case ok && acc.Epoch == epoch:
		return id, acc, true
	case !ok && epoch == 0:
		return id, accounts.Account{}, false
	}
	// Все куки отозваны («выйти на всех устройствах»).
	return "", accounts.Account{}, false
}

// start уводит игрока на Яндекс.
func (s *Service) start(c *gin.Context) {
	if !s.starts.allow(ws.ClientIP(c.Request, s.cfg.TrustProxy), s.now()) {
		c.String(http.StatusTooManyRequests, msg(c.Request, "слишком часто, попробуйте через минуту"))
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

// callback принимает код авторизации и превращает гостя в аккаунт (или находит аккаунт).
func (s *Service) callback(c *gin.Context) {
	ip := ws.ClientIP(c.Request, s.cfg.TrustProxy)
	if !s.callbacks.allow(ip, s.now()) {
		c.String(http.StatusTooManyRequests, msg(c.Request, "слишком часто, попробуйте через минуту"))
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
	s.link(c, accounts.ProviderYandex, prof.ID, prof.Login, prof.nickSuggestion(), back)
}

// link — общая часть входа и dev-входа: гость этого браузера становится аккаунтом или
// сливается с существующим, игрок получает куки аккаунта.
func (s *Service) link(c *gin.Context, provider, sub, login, suggestNick, back string) {
	guestID, _, _ := s.identify(c.Request)
	acc, created, err := s.accs.Link(guestID, provider, sub, login, suggestNick, s.nickTaken, s.now())
	if err != nil {
		s.fail(c, http.StatusInternalServerError, "не удалось создать аккаунт", err)
		return
	}
	s.issue(c.Writer, acc)
	s.log.Info().Str("account", acc.ID).Str("provider", provider).Str("guest", guestID).
		Bool("created", created).Str("nick", acc.Nick).Msg("auth: вход")
	c.Redirect(http.StatusFound, back)
}

// issue выдаёт куки записи. Аккаунту — подписанную и подсказку для клиента; гостю — только
// подписанную: подсказка значит «вошёл через Яндекс», и по ней вкладки узнают о входе и выходе.
func (s *Service) issue(w http.ResponseWriter, acc accounts.Account) {
	if acc.Guest() {
		s.issueGuest(w, acc.ID, acc.Epoch)
		return
	}
	exp := s.now().Add(SessionTTL)
	s.setCookie(w, CookieSession, sessionValue(s.secret, acc.ID, acc.Epoch, exp), SessionTTL, true)
	s.setCookie(w, CookieHint, acc.ID, SessionTTL, false)
}

func (s *Service) issueGuest(w http.ResponseWriter, id string, epoch int) {
	exp := s.now().Add(GuestTTL)
	s.setCookie(w, CookieSession, sessionValue(s.secret, id, epoch, exp), GuestTTL, true)
}

// logout — выход из аккаунта. Браузер тут же становится новым гостем: прежняя гостевая запись
// уже стала аккаунтом (или слилась с ним), а наследовать чужой ник и роль следующему человеку
// за этим компьютером незачем. POST, чтобы чужая картинка на постороннем сайте не разлогинивала.
func (s *Service) logout(c *gin.Context) {
	s.clearCookie(c.Writer, CookieHint, false)
	s.issueGuest(c.Writer, accounts.NewID(), 0)
	c.Status(http.StatusNoContent)
}

// devLogin пускает под любым аккаунтом без провайдера. Существует только в dev-сборке
// (см. config.Load), нужен для проверки игры без выхода в сеть.
func (s *Service) devLogin(c *gin.Context) {
	sub := c.DefaultQuery("sub", "dev")
	s.link(c, accounts.ProviderYandex, "dev:"+sub, "dev-"+sub, c.Query("nick"), returnPath(c.Query("return")))
}

// Refresh выдаёт гостю куку при открытии страницы игры и продлевает выданные. Работает только
// на самой странице (index.html): её открывает каждый заход, а статика и API ходят следом, и трогать
// базу на каждой картинке незачем.
func (s *Service) Refresh() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if s == nil || (path != "/" && path != "/index.html") || c.Request.Method != http.MethodGet {
			c.Next()
			return
		}
		id, acc, found := s.identify(c.Request)
		cookie, _ := c.Request.Cookie(CookieSession)
		switch {
		case id == "":
			// Куки нет, она чужая, просрочена или отозвана — это новый гость.
			s.issueGuest(c.Writer, accounts.NewID(), 0)
		case !found || acc.Guest():
			// Гость продлевается при каждом заходе, но не чаще раза в сутки: срок его записи
			// тоже считается от последнего захода.
			if s.remaining(cookie.Value) < GuestTTL-24*time.Hour {
				s.issueGuest(c.Writer, id, acc.Epoch)
			}
		case s.remaining(cookie.Value) < refreshBefore:
			s.issue(c.Writer, acc)
		}
		c.Next()
	}
}

// remaining — сколько куке осталось жить.
func (s *Service) remaining(value string) time.Duration {
	parts := strings.Split(value, ".")
	if len(parts) != 5 {
		return 0
	}
	unix, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0
	}
	return time.Unix(unix, 0).Sub(s.now())
}

// fail отвечает игроку человеческим текстом, а подробности оставляет в логе: в тексте страницы
// не должно быть ничего про код, токен и устройство сервера.
func (s *Service) fail(c *gin.Context, status int, text string, err error) {
	if err != nil {
		s.log.Warn().Err(err).Str("path", c.Request.URL.Path).Msg("auth: вход не удался")
	}
	c.String(status, msg(c.Request, text))
}
