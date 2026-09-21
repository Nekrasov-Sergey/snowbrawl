package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// Боевые адреса Яндекс ID. В тестах подменяются на httptest — ради этого они и вынесены в поля
// Config, а не зашиты в код.
const (
	YandexAuthURL  = "https://oauth.yandex.ru/authorize"
	YandexTokenURL = "https://oauth.yandex.ru/token" //nolint:gosec // это адрес ручки, а не секрет
	YandexInfoURL  = "https://login.yandex.ru/info"
)

// maxBody — сколько байт ответа провайдера читаем. Профиль и токен весят сотни байт; всё, что
// больше, — повод оборвать, а не складывать в память.
const maxBody = 64 << 10

// profile — то немногое, что нам нужно от Яндекса. Почту и телефон не запрашиваем и не читаем.
type profile struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	RealName    string `json:"real_name"`
	FirstName   string `json:"first_name"`
}

// nickSuggestion — имя, которое стоит предложить игроку ником. Порядок от «как человек сам себя
// назвал» к «хоть что-то»: display_name у Яндекса и есть отображаемое имя.
func (p profile) nickSuggestion() string {
	for _, v := range []string{p.DisplayName, p.FirstName, p.RealName, p.Login} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// exchange меняет код авторизации на access_token. Токен нужен ровно на один запрос профиля и
// дальше выбрасывается: доступ к Яндексу нам больше ни для чего не нужен, а хранить чужой
// секрет без причины — плохая идея.
func (s *Service) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := s.do(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		// В лог уходит только код ошибки провайдера — ни кода авторизации, ни секрета.
		return "", errors.Errorf("обмен кода отклонён: %s %s", out.Error, out.ErrorDescription)
	}
	return out.AccessToken, nil
}

// fetchProfile читает профиль пользователя.
func (s *Service) fetchProfile(ctx context.Context, token string) (profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.InfoURL+"?format=json", nil)
	if err != nil {
		return profile{}, err
	}
	// Токен заголовком, а не параметром запроса: параметры оседают в логах и истории.
	req.Header.Set("Authorization", "OAuth "+token)
	var p profile
	if err := s.do(req, &p); err != nil {
		return profile{}, err
	}
	if p.ID == "" {
		return profile{}, errors.New("профиль без идентификатора")
	}
	return p, nil
}

// do выполняет запрос к провайдеру и разбирает JSON. Общий для обоих вызовов, потому что общими
// должны быть и ограничения: таймаут, запрет редиректов, потолок на размер ответа.
func (s *Service) do(req *http.Request, out any) error {
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.Wrap(err, "запрос к провайдеру")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return errors.Wrap(err, "чтение ответа провайдера")
	}
	if resp.StatusCode != http.StatusOK {
		// Тело целиком не логируем: в нём может оказаться эхо кода авторизации. Достаточно
		// статуса и поля error, если провайдер его прислал.
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return errors.Errorf("провайдер ответил %d %s", resp.StatusCode, e.Error)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.Wrap(err, "ответ провайдера не разобран")
	}
	return nil
}

// newHTTPClient — клиент к провайдеру: короткий таймаут, чтобы зависший Яндекс не держал наши
// горутины, и запрет редиректов, чтобы ответ не увёл нас на чужой адрес.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
