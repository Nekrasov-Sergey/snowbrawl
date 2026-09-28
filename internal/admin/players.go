package admin

import (
	"sort"
	"time"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/hub"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
)

// Список игроков в админке — одна таблица на живые сессии и записи из базы. Живые идут первыми:
// о сессиях база не знает, поэтому их отбирает и сортирует админка, а записи без сессии — база
// (accounts.SearchOrder). Страницы сквозные: онлайн-строки занимают начало первых страниц.

// Виды игроков в строке списка.
const (
	kindGuest    = "guest"    // запись без провайдера
	kindYandex   = "yandex"   // вошёл через Яндекс ID
	kindNoCookie = "nocookie" // браузер без кук: записи нет, роль и бан не выдать
)

// playerRow — строка списка. Статус и пинг живых игроков страница берёт из SSE-потока по
// Session: они меняются каждую секунду, и перезапрашивать ради них список незачем.
type playerRow struct {
	ID      string    `json:"id,omitempty"`      // запись; пусто у гостя без кук
	Session string    `json:"session,omitempty"` // живая сессия, если игрок в игре
	Nick    string    `json:"nick"`
	Rank    string    `json:"rank,omitempty"`
	Kind    string    `json:"kind"`
	Login   string    `json:"login,omitempty"`
	Banned  bool      `json:"banned,omitempty"`
	SeenAt  time.Time `json:"seenAt"` // у живого — когда зашёл в эту сессию
}

// searchFunc — accounts.Store.Search; вынесена, чтобы слияние проверялось без базы.
type searchFunc func(query, filter string, exclude []string, offset, limit int) ([]accounts.Account, int, error)

// rankOrder — место роли в сортировке: админы, модераторы, остальные.
func rankOrder(rank string) int {
	switch rank {
	case protocol.RankAdmin:
		return 0
	case protocol.RankModerator:
		return 1
	}
	return 2
}

// playersPage собирает страницу списка: сначала живые (по роли, свежие сверху, по нику), потом
// записи из базы без уже показанных. Возвращает строки страницы и общее число строк.
func playersPage(sessions []hub.PlayerStat, get func(id string) (accounts.Account, bool), search searchFunc,
	query, filter string, page, perPage int) ([]playerRow, int, error) {
	var online []playerRow
	var exclude []string
	seen := map[string]bool{}
	for _, s := range sessions {
		row := playerRow{Session: s.ID, Nick: s.Nick, Rank: s.Rank, SeenAt: s.Since, Kind: kindNoCookie}
		acc, ok := get(s.Account)
		if s.Account != "" && ok {
			if seen[acc.ID] || !accounts.Matches(acc, query, filter) {
				continue
			}
			seen[acc.ID] = true
			exclude = append(exclude, acc.ID)
			row.ID, row.Login, row.Banned = acc.ID, acc.Login, acc.Banned()
			row.Kind = kindYandex
			if acc.Guest() {
				row.Kind = kindGuest
			}
		} else {
			// Гость без кук (или запись уже слита с аккаунтом, а сессия ещё доживает): роли и бана
			// у него нет, поэтому фильтры «с ролью», «забаненные» и «Яндекс» его не показывают.
			if filter != accounts.FilterAll && filter != accounts.FilterGuests {
				continue
			}
			if !accounts.MatchesQuery("", s.Nick, "", query) {
				continue
			}
		}
		online = append(online, row)
	}
	sort.SliceStable(online, func(i, j int) bool {
		a, b := online[i], online[j]
		if ra, rb := rankOrder(a.Rank), rankOrder(b.Rank); ra != rb {
			return ra < rb
		}
		if !a.SeenAt.Equal(b.SeenAt) {
			return a.SeenAt.After(b.SeenAt)
		}
		if ka, kb := protocol.NickKey(a.Nick), protocol.NickKey(b.Nick); ka != kb {
			return ka < kb
		}
		return a.Session < b.Session
	})

	if page < 0 {
		page = 0
	}
	start := page * perPage
	rows := []playerRow{}
	if start < len(online) {
		rows = append(rows, online[start:min(start+perPage, len(online))]...)
	}
	list, dbTotal, err := search(query, filter, exclude, max(0, start-len(online)), perPage-len(rows))
	if err != nil {
		return nil, 0, err
	}
	for _, a := range list {
		row := playerRow{ID: a.ID, Nick: a.Nick, Rank: a.Rank, Login: a.Login, Banned: a.Banned(),
			SeenAt: a.SeenAt, Kind: kindYandex}
		if a.Guest() {
			row.Kind = kindGuest
		}
		rows = append(rows, row)
	}
	return rows, len(online) + dbTotal, nil
}
