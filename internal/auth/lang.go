package auth

// Язык страниц входа. Ручки /auth/* отвечают текстом прямо в браузер, поэтому говорят на языке
// игрока: клиент кладёт выбранный язык в куку sb_lang (см. web/client/i18n.js), а до первого
// выбора её нет — и тогда отвечаем по-русски, как и раньше.
//
// Отдельной настройки на сервере нет и не нужно: перевод здесь ровно на восемь строк, а всё
// остальное клиент показывает сам.

import "net/http"

// CookieLang — имя куки с языком интерфейса. Её ставит клиент, сервер только читает.
const CookieLang = "sb_lang"

// messagesEN — переводы текстов, которыми отвечают ручки входа. Ключ — русская строка,
// как и в словаре клиента: так текст в коде читается без заглядывания в таблицу.
var messagesEN = map[string]string{
	"слишком часто, попробуйте через минуту":           "too many attempts, try again in a minute",
	"вход не начинался в этом браузере":                "the sign-in was not started in this browser",
	"вход не подтверждён, попробуйте ещё раз":          "the sign-in was not confirmed, try again",
	"Яндекс не ответил, попробуйте ещё раз":            "Yandex did not respond, try again",
	"не удалось прочитать профиль, попробуйте ещё раз": "could not read your profile, try again",
	"не удалось создать аккаунт":                       "could not create an account",
}

// msg переводит текст ответа под язык игрока. Незнакомый язык и отсутствие куки — русский.
func msg(r *http.Request, ru string) string {
	ck, err := r.Cookie(CookieLang)
	if err != nil || ck.Value != "en" {
		return ru
	}
	if en, ok := messagesEN[ru]; ok {
		return en
	}
	return ru
}
