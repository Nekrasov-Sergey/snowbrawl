# Протокол WebSocket

Эндпоинт: `/ws`. Текстовые кадры, JSON. Каждое сообщение — конверт:

```json
{ "t": "<тип>", "d": { ...данные } }
```

Версия протокола: **1** (`internal/protocol.Version`, `SBNet.PROTO` в клиенте).
Меняется только при несовместимом изменении сообщений. Добавление необязательных полей —
не несовместимое изменение.

Полезные константы и структуры — в `internal/protocol/protocol.go`, он же источник истины.

## Клиент → сервер

| Тип | Данные | Когда |
|---|---|---|
| `hello` | `{token?, nick, build, proto}` | первое сообщение после подключения |
| `ping` | — | по желанию, сервер ответит `pong` |
| `queue.join` | `{mode: 1..4, role}` | встать в очередь Quick Match |
| `queue.leave` | — | выйти из очереди |
| `room.create` | `{mode, arena}` | создать комнату, стать хостом |
| `room.join` | `{code}` | войти по коду (`SNB-XXXX`, регистр и префикс не важны) |
| `room.slot` | `{team: "A"\|"B", index}` | занять слот команды |
| `room.role` | `{role}` | выбрать бойца |
| `room.config` | `{mode, arena}` | хост меняет режим/арену |
| `room.kick` | `{playerId}` | хост выгоняет |
| `room.start` | — | хост стартует матч, пустые слоты займут боты |
| `room.leave` | — | выйти из комнаты |
| `match.leave` | — | покинуть матч (и комнату); бойца доигрывает бот |
| `input` | `{kind, x, y, power?}` | ввод в матче, пробрасывается в `sim.applyInput` как есть |

`input.kind`: `move`, `chargeStart`, `aim`, `throw` (с `power` 0..1), `special`. Координаты
в системе арены 900×560. Клиент шлёт `aim`/`move` не чаще ~15 раз в секунду.

## Сервер → клиент

| Тип | Данные | Когда |
|---|---|---|
| `welcome` | `{token, playerId, nick, build, sim, proto, draining?, resume}` | ответ на `hello`; `resume` ∈ `menu\|queue\|room\|match` — куда вернуться |
| `error` | `{code, msg?}` | ошибка обработки; коды ниже |
| `reload` | — | клиент устарел (другая сборка или протокол): `location.reload()` |
| `drain` | `{active, inSeconds?}` | сервер готовится к перезапуску (баннер) |
| `queue.status` | `{inQueue, mode, players, needed, waitLeft}` | состояние очереди, обновляется ~2 раза/с |
| `room.state` | `{code, hostId, mode, arena, players[], inMatch, lastWinner?}` | полное состояние лобби при любом изменении |
| `room.left` | `{code?: "kicked"}` | вы вышли/вас выгнали |
| `match.start` | `{matchId, mode, arena, players[], yourId, tickRate, roomCode?}` | матч начался или вы переподключились к идущему |
| `snapshot` | `{tick, s: <снапшот sim.js>, e?: [события шага]}` | каждый тик (20/с) |
| `match.end` | `{winner: "A"\|"B"\|"", yourTeam, reason, roomCode?}` | `reason` ∈ `ko\|timeout\|abandoned\|shutdown` |
| `pong` | — | ответ на `ping` |

`room.state.players[]`: `{id, nick, team, index, role, host, connected}`.
`match.start.players[]`: `{id, nick, team, role, bot}`.

Формат `snapshot.s` и `snapshot.e` определяет `sim.js` (`snapshot(state)` и события `step`),
см. [SIM_CONTRACT.md](SIM_CONTRACT.md). Сервер их не разбирает и передаёт как есть.

## Коды ошибок

`bad_message`, `bad_version`, `not_allowed`, `bad_nick`, `room_not_found`, `room_full`,
`room_limit`, `busy` (сначала выйдите из комнаты/матча), `draining`, `bad_mode`,
`bad_arena`, `bad_role`, `bad_slot`, `server_full`, `internal`.

## Сессия и переподключение

`welcome.token` клиент хранит в localStorage и присылает в следующем `hello`. Сервер помнит
игрока `ReconnectTTL` (60 с) после обрыва. Если пришёл `hello` с токеном, пока старое
соединение ещё живо, старое закрывается с причиной `replaced by new connection`
(две вкладки одного браузера = один игрок).

## Лимиты

Сообщение ≤ 4 КБ; 30 сообщений/с на соединение (всплеск 60), сверх — закрытие 1008;
очередь отправки 256 сообщений, при переполнении соединение закрывается.
