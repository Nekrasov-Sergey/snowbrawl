package hub

import (
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/Nekrasov-Sergey/snowbrawl/internal/moderation"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/protocol"
	"github.com/Nekrasov-Sergey/snowbrawl/internal/session"
)

// Права на удаление сообщений проверяем внутри пакета: роли выдаются по IP, а в
// интеграционных тестах все клиенты приходят с 127.0.0.1 и роль у них общая.
func TestCanDeleteChatRules(t *testing.T) {
	mod, err := moderation.Open("", zerolog.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	const (
		playerIP  = "10.0.0.1"
		adminIP   = "10.0.0.2"
		admin2IP  = "10.0.0.3"
		creatorIP = "10.0.0.4"
	)
	for ip, rank := range map[string]string{
		adminIP:   protocol.RankAdmin,
		admin2IP:  protocol.RankAdmin,
		creatorIP: protocol.RankCreator,
	} {
		if err := mod.SetRank(ip, rank, "", now); err != nil {
			t.Fatal(err)
		}
	}
	h := &Hub{mod: mod}

	player := &session.Player{ID: "p1", IP: playerIP}
	admin := &session.Player{ID: "p2", IP: adminIP}
	admin2 := &session.Player{ID: "p3", IP: admin2IP}
	creator := &session.Player{ID: "p4", IP: creatorIP}

	msg := func(author *session.Player) chatEntry {
		return chatEntry{msg: protocol.ChatMessage{ID: 1, PID: author.ID}, authorIP: author.IP}
	}

	cases := []struct {
		name    string
		deleter *session.Player
		entry   chatEntry
		want    bool
	}{
		{"игрок удаляет своё", player, msg(player), true},
		{"игрок удаляет чужое", player, msg(admin), false},
		{"админ удаляет игрока", admin, msg(player), true},
		{"админ удаляет своё", admin, msg(admin), true},
		{"админ удаляет другого админа", admin, msg(admin2), false},
		{"админ удаляет создателя", admin, msg(creator), false},
		{"создатель удаляет админа", creator, msg(admin), true},
		{"создатель удаляет игрока", creator, msg(player), true},
	}
	for _, c := range cases {
		if got := h.canDeleteChat(c.deleter, c.entry); got != c.want {
			t.Errorf("%s: получилось %v, ожидалось %v", c.name, got, c.want)
		}
	}

	// Роль автора берётся актуальная: снятие роли с админа делает его сообщения удаляемыми.
	if err := mod.SetRank(admin2IP, protocol.RankPlayer, "", now); err != nil {
		t.Fatal(err)
	}
	if !h.canDeleteChat(admin, msg(admin2)) {
		t.Error("после снятия роли сообщение должно стать удаляемым для админа")
	}
}
