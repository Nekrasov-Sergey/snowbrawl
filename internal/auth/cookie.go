package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
)

// Куки. Их две, и вторая нужна не для безопасности.
//
// CookieSession — кто это: httpOnly, подписанная, её видит только сервер. Её получает каждый —
// гость при первом открытии игры, игрок при входе через Яндекс. В ней id записи игрока
// (internal/accounts) и эпоха. Подпись, а не таблица сессий: проверка идёт на каждом апгрейде
// /ws и на каждой загрузке страницы, а подпись проверяется без обращения к диску. Цена — отозвать
// одну куку нельзя, только все сразу (Account.Epoch), и этого достаточно.
//
// CookieHint — тот же id, но без подписи и без httpOnly: её читает клиент, и есть она только у
// вошедших через Яндекс. Нужна ровно для одного случая: игрок вошёл или вышел в ДРУГОЙ вкладке,
// а эта держит открытый сокет. Сравнив куку со своим состоянием при возврате фокуса, вкладка сама
// переподключается. Пуш по сокету тут невозможен: соединение открыто от прежнего лица.
const (
	CookieSession = "sb_acc"
	CookieHint    = "sb_acc_hint"
	CookieState   = "sb_state"
)

// SessionTTL — сколько живёт кука аккаунта; refreshBefore — за сколько до конца она продлевается
// при заходе. Полгода: игра не хранит ничего ценного, а требовать повторный вход у того, кто
// заходит раз в месяц, незачем. GuestTTL — кука гостя: столько же, сколько живёт его запись без
// заходов, и продлевается она при каждом заходе.
const (
	SessionTTL     = 180 * 24 * time.Hour
	GuestTTL       = accounts.GuestTTL
	refreshBefore  = 30 * 24 * time.Hour
	stateTTL       = 10 * time.Minute
	sessionVersion = "v1"
)

var errBadCookie = errors.New("auth: кука не разобрана")

// sessionValue собирает значение куки: v1.<id>.<epoch>.<exp>.<подпись>. Epoch внутри подписи —
// то, чем «выйти на всех устройствах» обесценивает все выданные куки разом.
func sessionValue(secret []byte, accountID string, epoch int, exp time.Time) string {
	body := sessionVersion + "." + accountID + "." + strconv.Itoa(epoch) + "." + strconv.FormatInt(exp.Unix(), 10)
	return body + "." + sign(secret, body)
}

// parseSession разбирает значение куки и проверяет подпись и срок. Эпоху сверяет вызывающий:
// она лежит в аккаунте, а этот файл про аккаунты ничего не знает.
func parseSession(secret []byte, value string, now time.Time) (accountID string, epoch int, err error) {
	parts := strings.Split(value, ".")
	if len(parts) != 5 || parts[0] != sessionVersion {
		return "", 0, errBadCookie
	}
	body := strings.Join(parts[:4], ".")
	if !hmac.Equal([]byte(parts[4]), []byte(sign(secret, body))) {
		return "", 0, errBadCookie
	}
	epoch, err = strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, errBadCookie
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return "", 0, errBadCookie
	}
	if now.After(time.Unix(exp, 0)) {
		return "", 0, errBadCookie
	}
	if parts[1] == "" {
		return "", 0, errBadCookie
	}
	return parts[1], epoch, nil
}

// stateValue — подписанный state для OAuth: сам state, куда вернуть игрока и срок. Возврат
// хранится здесь, а не в query колбэка, иначе получился бы открытый редирект.
func stateValue(secret []byte, state, returnPath string, exp time.Time) string {
	body := strings.Join([]string{state, base64.RawURLEncoding.EncodeToString([]byte(returnPath)),
		strconv.FormatInt(exp.Unix(), 10)}, ".")
	return body + "." + sign(secret, body)
}

func parseState(secret []byte, value string, now time.Time) (state, returnPath string, err error) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 {
		return "", "", errBadCookie
	}
	body := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(parts[3]), []byte(sign(secret, body))) {
		return "", "", errBadCookie
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.After(time.Unix(exp, 0)) {
		return "", "", errBadCookie
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errBadCookie
	}
	return parts[0], string(raw), nil
}

func sign(secret []byte, body string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// randomHex — случайная строка для state и генерации ключа подписи.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// setCookie ставит куку с общими для всех наших кук правилами. SameSite=Lax обязателен:
// со Strict браузер не отдал бы куку при возврате с домена Яндекса, и вход не сработал бы.
func (s *Service) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration, httpOnly bool) {
	// nolint для gosec: Secure нельзя зашить константой — по http игра работает на localhost и
	// на демо по IP, где браузер такую куку просто выбросил бы. Флаг берётся из публичного
	// адреса (s.secure), то есть в бою на https он всегда включён.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure зависит от схемы публичного адреса
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: httpOnly, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter, name string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // см. setCookie: Secure зависит от схемы
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: httpOnly, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}
