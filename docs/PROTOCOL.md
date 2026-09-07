# Протокол WebSocket

Эндпоинт: `/ws`. Текстовые кадры, JSON. Каждое сообщение — конверт:

```json
{ "t": "<тип>", "d": { ...данные } }
```

Версия протокола: **2** (`internal/protocol.Version`, `SBNet.PROTO` в клиенте).
Меняется только при несовместимом изменении сообщений. Добавление необязательных полей —
не несовместимое изменение.

Полезные константы и структуры — в `internal/protocol/protocol.go`, он же источник истины.

## Клиент → сервер

| Тип | Данные | Когда |
|---|---|---|
| `hello` | `{token?, nick, build, proto}` | первое сообщение после подключения |
| `ping` | — | по желанию, сервер ответит `pong` |
| `room.create` | `{mode, arena, gameMode?, campaign?, difficulty?, visibility?}` | создать комнату, стать хостом |
| `room.join` | `{code}` | войти по коду (четыре цифры `1234`; старый префикс `SNB-` отбрасывается); можно и в комнату с идущим матчем |
| `room.slot` | `{team: "A"\|"B", index}` | занять слот команды (в PvE только `"A"`) |
| `room.role` | `{role}` | выбрать бойца |
| `room.config` | `{mode, arena, gameMode?, campaign?, difficulty?, visibility?}` | хост меняет настройки комнаты |
| `room.ready` | `{ready}` | отметить готовность; когда готовы все люди в комнате, матч стартует сам |
| `room.kick` | `{playerId}` | хост выгоняет |
| `room.start` | — | хост стартует матч независимо от готовности, пустые слоты займут боты |
| `room.leave` | — | выйти из комнаты |
| `room.list` | `{section: "pvp"\|"pve", page}` | запросить страницу списка комнат и подписаться на её обновления |
| `room.unlist` | — | снять подписку (игрок ушёл с экрана списка) |
| `match.join` | — | войти в идущий матч за бойца своего слота в комнате |
| `match.leave` | — | выйти из боя, оставшись в комнате: место сохраняется, бойца ведёт бот |
| `input` | `{kind, x, y, power?}` | ввод в матче, пробрасывается в `sim.applyInput` как есть |
| `training` | `{on, mode?, arena?, role?}` | клиент играет тренировку с ботами; сервер в ней не участвует, отметка нужна только админке |

`input.kind`: `move`, `chargeStart`, `aim`, `throw` (с `power` 0..1), `cancelCharge`, `special`.
Координаты в системе арены 900×560. Клиент шлёт `aim`/`move` не чаще ~15 раз в секунду.
Сенсорный клиент направления стиков тоже превращает в точки арены (`web/client/intent.js`),
поэтому серверу неважно, мышь это или стик.

## Сервер → клиент

| Тип | Данные | Когда |
|---|---|---|
| `welcome` | `{token, playerId, nick, build, sim, proto, draining?, resume}` | ответ на `hello`; `resume` ∈ `menu\|room\|match` — куда вернуться |
| `error` | `{code, msg?}` | ошибка обработки; коды ниже |
| `reload` | — | клиент устарел (другая сборка или протокол): `location.reload()` |
| `drain` | `{active, inSeconds?}` | сервер готовится к перезапуску (баннер) |
| `room.state` | `{code, hostId, mode, arena, gameMode?, campaign?, difficulty?, visibility, players[], inMatch, readyCount, lastWinner?}` | полное состояние лобби при любом изменении |
| `room.left` | `{code?: "kicked"}` | вы вышли/вас выгнали |
| `room.list` | `{section, page, pages, total, rooms[]}` | страница списка комнат; пока игрок подписан, сервер сам присылает её заново при изменениях |
| `room.match` | `{code, timeLeftMs, slots[]}` | состояние идущего матча для тех, кто сидит в лобби этой комнаты; уходит при изменении |
| `match.start` | `{matchId, mode, arena, gameMode?, players[], yourId, tickRate, roomCode?}` | матч начался, вы переподключились или вошли за бойца своего слота |
| `match.roster` | `{players[]}` | состав матча изменился: кто-то занял место бота или вышел |
| `snapshot` | `{tick, s: <снапшот sim.js>, e?: [события шага], cd?: мс до старта}` | каждый тик (20/с) |
| `match.end` | `{winner: "A"\|"B"\|"", yourTeam, reason, roomCode?}` | `reason` PvP ∈ `ko\|timeout\|abandoned\|shutdown`, PvE ∈ `cleared\|wiped\|objective\|expired` |
| `pong` | — | ответ на `ping` |
| `online` | `{n}` | число игроков на сервере; шлётся при каждом изменении |

`room.state.players[]`: `{id, nick, team, index, role, host, connected, ready, inMatch}`.
`match.start.players[]` и `match.roster.players[]`: `{id, nick, team, index, role, bot, botLevel?}` —
`index` вместе с `team` привязывает бойца к слоту комнаты.
`room.match.slots[]`: `{team, index, nick, role, bot, hp, koed}` — `bot: true` значит, что бойца
сейчас ведёт бот: слот свободен или его человек ушёл в лобби.
`room.list.rooms[]`: `{code, section, gameMode?, campaign?, mode, arena, humans, bots, capacity,
inMatch, visibility, hostNick, ageMs, joinable, needCode?}`. Комнаты идут по времени создания,
старые первыми, по 25 на страницу. У закрытой комнаты `code` пустой и `needCode: true` — иначе
список сам выдавал бы то, что защищает код.
Слот комнаты и боец матча — одно и то же. Вошедший в комнату (в ожидании или во время матча)
получает свободный слот, `room.slot` меняет его на другой свободный, а `match.join` сажает игрока
за бойца именно этого слота: `match.start.yourId` у него равен id бойца, а не id сессии. Ник в
снапшоте остаётся ботовским (симуляция не умеет переименовывать бойцов), поэтому имена в
интерфейсе клиент берёт из состава матча. Выход из боя (`match.leave`) оставляет игрока в комнате,
и место держится за ним, пока он в комнате: ушёл из комнаты или истекла сессия — слот свободен.

`snapshot.cd` > 0 — идёт отсчёт перед началом матча: симуляция стоит, `input` сервером не
применяется, клиент рисует «3, 2, 1». Поля нет — матч идёт.

Формат `snapshot.s` и `snapshot.e` определяет `sim.js` (`snapshot(state)` и события `step`),
см. [SIM_CONTRACT.md](SIM_CONTRACT.md). Сервер их не разбирает и передаёт как есть.

## Коды ошибок

`bad_message`, `bad_version`, `not_allowed`, `bad_nick`, `room_not_found`, `room_full`,
`room_limit`, `busy` (сначала выйдите из комнаты/матча), `draining`, `bad_mode`,
`bad_arena`, `bad_role`, `bad_gamemode`, `bad_slot`, `bad_code`, `too_many_tries`
(не больше пяти неудачных попыток кода с адреса в минуту — защита от перебора закрытых комнат),
`slot_taken`, `no_slots`, `server_full`, `internal`.

## Сессия и переподключение

`welcome.token` клиент хранит в localStorage и присылает в следующем `hello`. Сервер помнит
игрока `ReconnectTTL` (60 с) после обрыва. Если пришёл `hello` с токеном, пока старое
соединение ещё живо, старое закрывается с причиной `replaced by new connection`
(две вкладки одного браузера = один игрок).

## Лимиты

Сообщение ≤ 4 КБ; 30 сообщений/с на соединение (всплеск 60), сверх — закрытие 1008;
очередь отправки 256 сообщений, при переполнении соединение закрывается.
