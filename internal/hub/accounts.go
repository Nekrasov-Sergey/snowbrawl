package hub

import (
	"encoding/json"
	"errors"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Аккаунты в игре: смена ника и прогресс обучения. Само хранилище — internal/accounts, вход —
// internal/auth; здесь только последствия для игры.
//
// Всё в этом файле вызывается под h.mu. Стор аккаунтов наружу не ходит и на диск под нами не
// пишет (его фоновая горутина делает это сама), поэтому дедлок и fsync под общим мьютексом
// невозможны — то же правило, что у ролей и у ряда онлайна.

// SetAuthInfo сообщает, какие способы входа включены: клиент по этому полю решает, показывать ли
// кнопку «Войти через Яндекс». Вызывать до Run.
func (h *Hub) SetAuthInfo(info *protocol.AuthInfo) { h.authInfo = info }

// accountInfo переводит аккаунт в то, что видит клиент. Ни provider-специфичного id, ни времени
// входа: клиенту они не нужны.
func accountInfo(a accounts.Account) *protocol.Account {
	return &protocol.Account{ID: a.ID, Provider: a.Provider, Nick: a.Nick, NickAuto: a.NickAuto}
}

// sendAccount отправляет игроку состояние его аккаунта.
func (h *Hub) sendAccount(p *session.Player) {
	acc, ok := h.accs.Get(p.AccountID)
	if !ok {
		return
	}
	p.Send(protocol.MustEncode(protocol.SAccount, protocol.AccountState{
		Account: accountInfo(acc), Tutorial: acc.Tutorial,
	}))
}

// handleNickSet меняет ник аккаунта. Гость меняет ник по-старому — переподключением с новым
// hello; заводить ему второй путь незачем, а вошедшему игроку переподключение стоило бы выхода
// из комнаты в момент, когда он просто хотел переименоваться.
func (h *Hub) handleNickSet(p *session.Player, data json.RawMessage) {
	if p.AccountID == "" {
		h.sendErrP(p, protocol.ErrNoAccount, "смена ника доступна вошедшим в аккаунт")
		return
	}
	var req protocol.NickSet
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErrP(p, protocol.ErrBadMessage, "bad nick.set")
		return
	}
	nick, err := protocol.NormalizeNick(req.Nick)
	if err != nil {
		h.sendErrP(p, nickErrCode(err), err.Error())
		return
	}
	now := h.now()
	// Ник, занятый живым гостем, аккаунту не отдаём: иначе в лобби окажутся два одинаковых
	// имени. Проверка симметрична той, что не пускает гостя на ник аккаунта.
	switch err := h.accs.Rename(p.AccountID, nick, h.guestHoldsNick(p.ID), now); {
	case errors.Is(err, accounts.ErrNickTaken):
		h.sendErrP(p, protocol.ErrNickTaken, "nick is taken")
		return
	case errors.Is(err, accounts.ErrTooSoon):
		h.sendErrP(p, protocol.ErrRenameCooldown, "ник менялся недавно")
		return
	case err != nil:
		h.sendErrP(p, protocol.ErrInternal, "не удалось сменить ник")
		return
	}
	if key := protocol.NickKey(p.Nick); key != protocol.NickKey(nick) {
		h.releaseNick(key, p.ID)
	}
	p.Nick = nick
	h.holdNick(nick, p.IP, p.ID, now)
	h.sendAccount(p)
	// Ник виден соседям по лобби и в истории чата, поэтому и то и другое надо перерисовать.
	if r := h.rooms[p.RoomCode]; r != nil && r.Member(p.ID) != nil {
		h.broadcastRoom(r)
	}
	h.log.Info().Str("player", p.ID).Str("account", p.AccountID).Str("nick", nick).Msg("nick changed")
}

// guestHoldsNick возвращает проверку «ник занят живой бронью гостя», исключая самого игрока:
// его собственная бронь переименованию мешать не должна.
func (h *Hub) guestHoldsNick(self string) func(key string) bool {
	return func(key string) bool {
		hold, ok := h.nicks[key]
		return ok && hold.owner != self
	}
}

// handleTutorialDone отмечает пройденный урок. От гостя приходить не должно, но если пришло —
// молчим: клиент не обязан знать в двух местах, вошёл игрок или нет.
func (h *Hub) handleTutorialDone(p *session.Player, data json.RawMessage) {
	if p.AccountID == "" {
		return
	}
	var req protocol.TutorialDone
	if err := json.Unmarshal(data, &req); err != nil || req.ID == "" {
		return
	}
	if h.accs.AddTutorial(p.AccountID, req.ID) {
		h.sendAccount(p)
	}
}

// handleTutorialSync сливает прогресс, накопленный на устройстве, с аккаунтом. Слияние, а не
// замена: множество пройденного только растёт, поэтому конфликтов между устройствами не бывает
// и порядок входа не важен.
func (h *Hub) handleTutorialSync(p *session.Player, data json.RawMessage) {
	if p.AccountID == "" {
		return
	}
	var req protocol.TutorialSync
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}
	if len(req.IDs) > maxTutorialSync {
		req.IDs = req.IDs[:maxTutorialSync]
	}
	h.accs.AddTutorial(p.AccountID, req.IDs...)
	// Отвечаем всегда: клиенту нужен полный список, даже если добавить было нечего — именно им
	// он дополнит своё локальное хранилище.
	h.sendAccount(p)
}

// maxTutorialSync — потолок на список в tutorial.sync. Уроков в игре семь; всё, что больше, —
// не прогресс, а попытка раздуть файл аккаунтов.
const maxTutorialSync = 64
