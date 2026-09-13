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
		playerIP = "10.0.0.1"
		modIP    = "10.0.0.2"
		mod2IP   = "10.0.0.3"
		adminIP  = "10.0.0.4"
	)
	for ip, rank := range map[string]string{
		modIP:   protocol.RankModerator,
		mod2IP:  protocol.RankModerator,
		adminIP: protocol.RankAdmin,
	} {
		if err := mod.SetRank(ip, rank, "", now); err != nil {
			t.Fatal(err)
		}
	}
	h := &Hub{mod: mod}

	player := &session.Player{ID: "p1", IP: playerIP}
	moder := &session.Player{ID: "p2", IP: modIP}
	moder2 := &session.Player{ID: "p3", IP: mod2IP}
	admin := &session.Player{ID: "p4", IP: adminIP}

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

	// Роль автора берётся актуальная: снятие роли с модератора делает его сообщения удаляемыми.
	if err := mod.SetRank(mod2IP, protocol.RankPlayer, "", now); err != nil {
		t.Fatal(err)
	}
	if !h.canDeleteChat(moder, msg(moder2)) {
		t.Error("после снятия роли сообщение должно стать удаляемым для модератора")
	}
}
