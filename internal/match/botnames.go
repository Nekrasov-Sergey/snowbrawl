package match

import "strconv"

// BotNames — имена ботов, одно слово в зимней тематике: ник бойца — «Бот Сугроб» в PvP и
// «Союзник Сугроб» в PvE. Тот же список лежит в web/client/i18n.js (I18n.BOT_NAMES): там их
// переводят и берут в оффлайн-тренировке, совпадение списков сверяет i18n_test.go. Имён мобов
// и ролей sim.js (Вьюга, Ком, Йети, Рой…) здесь нет, чтобы бот не путался с противником.
var BotNames = []string{
	"Снежок", "Сугроб", "Сосулька", "Метель", "Пурга", "Буран", "Иней", "Льдинка", "Снежинка", "Морозко",
	"Позёмка", "Наст", "Айсберг", "Пломбир", "Валенок", "Варежка", "Санки", "Ледник", "Сквозняк", "Холодок",
}

// BotPrefix — первое слово ника бота: в PvP это противник или напарник-бот, в PvE — союзник.
func BotPrefix(gameMode string) string {
	if gameMode != "" && gameMode != "pvp" {
		return "Союзник"
	}
	return "Бот"
}

// PickBotNick выбирает ник бота, которого ещё нет среди used (ники целиком). intn — источник
// случайности вида rand.IntN. Если пул исчерпан, к имени дописывается номер.
func PickBotNick(gameMode string, used map[string]bool, intn func(int) int) string {
	prefix := BotPrefix(gameMode) + " "
	free := make([]string, 0, len(BotNames))
	for _, n := range BotNames {
		if !used[prefix+n] {
			free = append(free, prefix+n)
		}
	}
	if len(free) > 0 {
		return free[intn(len(free))]
	}
	base := prefix + BotNames[intn(len(BotNames))]
	for i := 2; ; i++ {
		if nick := base + " " + strconv.Itoa(i); !used[nick] {
			return nick
		}
	}
}
