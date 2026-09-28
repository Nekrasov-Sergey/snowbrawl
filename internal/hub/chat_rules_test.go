package hub

import (
	"testing"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Права на удаление сообщений — таблицей, внутри пакета: так все сочетания ролей проверяются
// без живых соединений. Интеграционный вариант — TestChatDeleteRights.
func TestCanDeleteChatRules(t *testing.T) {
	t.Parallel()
	h := &Hub{}

	player := &session.Player{ID: "p1", AccountID: "a1"}
	moder := &session.Player{ID: "p2", AccountID: "a2", Rank: protocol.RankModerator}
	moder2 := &session.Player{ID: "p3", AccountID: "a3", Rank: protocol.RankModerator}
	admin := &session.Player{ID: "p4", AccountID: "a4", Rank: protocol.RankAdmin}

	msg := func(author *session.Player) chatEntry {
		return chatEntry{msg: protocol.ChatMessage{ID: 1, PID: author.ID}, authorAccount: author.AccountID, authorRank: author.Rank}
	}

	cases := []struct {
		name    string
		deleter *session.Player
		entry   chatEntry
		want    bool
	}{
		{"игрок удаляет своё", player, msg(player), true},
		{"игрок удаляет чужое", player, msg(moder), false},
		{"модератор удаляет игрока", moder, msg(player), true},
		{"модератор удаляет своё", moder, msg(moder), true},
		{"модератор удаляет другого модератора", moder, msg(moder2), false},
		{"модератор удаляет админа", moder, msg(admin), false},
		{"админ удаляет модератора", admin, msg(moder), true},
		{"админ удаляет игрока", admin, msg(player), true},
	}
	for _, c := range cases {
		if got := h.canDeleteChat(c.deleter, c.entry); got != c.want {
			t.Errorf("%s: получилось %v, ожидалось %v", c.name, got, c.want)
		}
	}
}
