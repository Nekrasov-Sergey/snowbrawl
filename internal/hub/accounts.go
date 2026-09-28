package hub

import (
	"encoding/json"
	"errors"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/accounts"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/ws"
)

// Записи игроков в игре: смена ника и прогресс обучения. Само хранилище — internal/accounts,
// опознание по куке — internal/auth; здесь только последствия для игры.
//
// Обработчики этого файла пишут в базу, поэтому держат h.mu не всё время, а кусками: взять
// игрока — отпустить — записать — взять снова и применить. Под h.mu в базу можно только читать
// (см. internal/accounts).

// SetAuthInfo сообщает, какие способы входа включены: клиент по этому полю решает, показывать ли
// кнопку «Войти через Яндекс». Вызывать до Run.
func (h *Hub) SetAuthInfo(info *protocol.AuthInfo) { h.authInfo = info }

// accountInfo переводит запись в то, что видит клиент. Ни provider-специфичного id, ни времени
// входа: клиенту они не нужны. Отметку «ник выдал сервер» получает только аккаунт Яндекса: у него
// она значит «имя из профиля не подошло», и клиент предлагает выбрать ник. Гостю, которому ник
// выдали по кнопке «Играть гостем», так и задумано.
func accountInfo(a accounts.Account) *protocol.Account {
	return &protocol.Account{ID: a.ID, Provider: a.Provider, Nick: a.Nick, NickAuto: a.NickAuto && !a.Guest()}
}

// sendAccount отправляет игроку состояние его записи. Вызывать под h.mu (база только читается).
func (h *Hub) sendAccount(p *session.Player) {
	acc, ok := h.accs.Get(p.AccountID)
	if !ok {
		return
	}
	p.Send(protocol.MustEncode(protocol.SAccount, protocol.AccountState{
		Account: accountInfo(acc), Tutorial: acc.Tutorial,
	}))
}

// withRecord берёт игрока соединения и id его записи под h.mu и отпускает мьютекс. Пустой id —
// у игрока записи нет (браузер без кук). false — игрока нет, ответ уже отправлен.
func (h *Hub) withRecord(c *ws.Conn) (playerID, accountID string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.current(c)
	if !ok {
		return "", "", false
	}
	return p.ID, p.AccountID, true
}

// again снова берёт h.mu и возвращает игрока, если он всё ещё на этом соединении. Вызывающий
// обязан отпустить мьютекс, если ok.
func (h *Hub) again(c *ws.Conn, playerID string) (*session.Player, bool) {
	h.mu.Lock()
	p, _ := c.Session.(*session.Player)
	if p == nil || p.Conn != c || p.ID != playerID {
		h.mu.Unlock()
		return nil, false
	}
	return p, true
}

// handleNickSet меняет ник записи. Игрок без записи (браузер без кук) меняет ник по-старому —
// переподключением с новым hello; игроку с записью переподключение стоило бы выхода из комнаты
// в момент, когда он просто хотел переименоваться.
func (h *Hub) handleNickSet(c *ws.Conn, data json.RawMessage) {
	playerID, accountID, ok := h.withRecord(c)
	if !ok {
		return
	}
	if accountID == "" {
		h.sendErr(c, protocol.ErrNoAccount, "смена ника без записи игрока — через hello")
		return
	}
	var req protocol.NickSet
	if err := json.Unmarshal(data, &req); err != nil {
		h.sendErr(c, protocol.ErrBadMessage, "bad nick.set")
		return
	}
	nick, err := protocol.NormalizeNick(req.Nick)
	if err != nil {
		h.sendErr(c, nickErrCode(err), err.Error())
		return
	}
	now := h.now()
	// Ник, занятый живой бронью другой сессии, не отдаём: иначе в лобби окажутся два одинаковых
	// имени. Проверка симметрична той, что не пускает гостя без записи на ник из базы.
	switch err := h.accs.Rename(accountID, nick, h.heldByOther(playerID), now); {
	case errors.Is(err, accounts.ErrNickTaken):
		h.sendErr(c, protocol.ErrNickTaken, "nick is taken")
		return
	case errors.Is(err, accounts.ErrTooSoon):
		h.sendErr(c, protocol.ErrRenameCooldown, "ник менялся недавно")
		return
	case err != nil:
		h.log.Error().Err(err).Str("account", accountID).Msg("nick.set: запись не обновлена")
		h.sendErr(c, protocol.ErrInternal, "не удалось сменить ник")
		return
	}
	p, ok := h.again(c, playerID)
	if !ok {
		return
	}
	defer h.mu.Unlock()
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
	h.log.Info().Str("player", p.ID).Str("account", accountID).Str("nick", nick).Msg("nick changed")
}

// heldByOther возвращает проверку «ник держит живая бронь другой сессии», исключая самого
// игрока: его собственная бронь переименованию мешать не должна. Берёт h.mu, поэтому звать её
// под h.mu нельзя.
func (h *Hub) heldByOther(self string) func(key string) bool {
	return func(key string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		hold, ok := h.nicks[key]
		return ok && hold.owner != self
	}
}

// handleTutorialDone отмечает пройденный урок. От игрока без записи приходить не должно, но если
// пришло — молчим: клиент не обязан знать в двух местах, есть ли у него запись.
func (h *Hub) handleTutorialDone(c *ws.Conn, data json.RawMessage) {
	playerID, accountID, ok := h.withRecord(c)
	if !ok || accountID == "" {
		return
	}
	var req protocol.TutorialDone
	if err := json.Unmarshal(data, &req); err != nil || req.ID == "" {
		return
	}
	if !h.accs.AddTutorial(accountID, req.ID) {
		return
	}
	if p, ok := h.again(c, playerID); ok {
		h.sendAccount(p)
		h.mu.Unlock()
	}
}

// handleTutorialSync сливает прогресс, накопленный на устройстве, с записью. Слияние, а не
// замена: множество пройденного только растёт, поэтому конфликтов между устройствами не бывает
// и порядок входа не важен.
func (h *Hub) handleTutorialSync(c *ws.Conn, data json.RawMessage) {
	playerID, accountID, ok := h.withRecord(c)
	if !ok || accountID == "" {
		return
	}
	var req protocol.TutorialSync
	if err := json.Unmarshal(data, &req); err != nil {
		return
	}
	if len(req.IDs) > maxTutorialSync {
		req.IDs = req.IDs[:maxTutorialSync]
	}
	h.accs.AddTutorial(accountID, req.IDs...)
	// Отвечаем всегда: клиенту нужен полный список, даже если добавить было нечего — именно им
	// он дополнит своё локальное хранилище.
	if p, ok := h.again(c, playerID); ok {
		h.sendAccount(p)
		h.mu.Unlock()
	}
}

// maxTutorialSync — потолок на список в tutorial.sync. Уроков в игре семь; всё, что больше, —
// не прогресс, а попытка раздуть базу.
const maxTutorialSync = 64
