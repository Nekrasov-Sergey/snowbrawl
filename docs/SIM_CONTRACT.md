# Контракт `web/sim/sim.js` — для разработчика игры

`sim.js` — единственное место, где живут правила игры. Его исполняет браузер (оффлайн-режим)
и сервер (goja, для всех сетевых матчей). Всё, что здесь описано, сервер проверяет при
запуске (`sim.Compile`) и в тестах `internal/sim`.

## Что запрещено внутри sim.js

Сервер исполняет файл в чистом JS-окружении без браузера. Поэтому в sim.js **нельзя**:

- обращаться к `window`, `document`, DOM, Canvas, Web Audio, `localStorage`, `fetch`;
- использовать `performance.now()`, `Date.now()`, `new Date()`, `setTimeout`, `requestAnimationFrame`;
- использовать `Math.random()` — вместо него `state.rng.next()` (детерминированный, из seed);
- использовать ES-модули (`import`/`export`), `async/await` не нужен и не поддерживается для этого контракта;
- держать состояние в замыканиях модуля: всё состояние матча — внутри объекта `state`.

Можно: ES2015+ синтаксис (`let`, стрелки, деструктуризация, классы), `Math.*`, `JSON`,
обычные массивы/объекты. Файл — UMD-обёртка: в браузере создаёт глобал `SnowBrawlSim`,
в CommonJS — `module.exports`. Сервер ищет именно глобал `SnowBrawlSim`.

## Экспорт модуля

```js
SnowBrawlSim = {
  SIM_VERSION: '1.0.0',                 // semver правил игры, показывается в админке и логах
  W, H, GRAVITY, CHARGE_FULL_MS, KO_ANIM_MS,
  ARENAS, ROLE_STATS, SPECIALS, MODES, HERO_DESCRIPTIONS, ABILITY_HINT_TEXT, ALL_ROLES,
  makeRng(seed), shuffle(rng, arr),

  createMatch(config, seed) -> state,
  applyInput(state, playerId, input) -> bool,
  setBot(state, playerId, isBot) -> bool,
  step(state, dtSeconds) -> events[],
  snapshot(state) -> object,
  isOver(state) -> bool,
  winner(state) -> 'A' | 'B' | null
}
```

Сервер обязательно использует: `SIM_VERSION`, `ARENAS.length`, `ALL_ROLES`, `createMatch`,
`applyInput`, `setBot`, `step`, `snapshot`, `isOver`, `winner`. Остальное — для клиента.

### `createMatch(config, seed)`

```js
config = {
  mode: 1|2|3|4,                 // размер команды
  arenaIndex: 0..ARENAS.length-1,
  durationMs?: 300000,           // таймер матча; по умолчанию 5 минут, потом ничья
  players: [                     // ровно 2*mode штук
    { id: 'p1a2b3c4', team: 'A', role: 'Бомбер', bot: false, nick: 'Сергей' },
    { id: 'bot1',     team: 'B', role: 'Танк',   bot: true,  nick: 'Бот 1' },
  ]
}
```

`seed` — uint32; при одинаковых seed и одинаковой последовательности `applyInput`/`step`
результат должен совпадать байт в байт (проверяется тестом `TestDeterministic`).
Бросать исключение при плохой конфигурации можно и нужно: сервер залогирует и не создаст матч.

### `applyInput(state, playerId, input)`

`input = { kind, x, y, power? }`, координаты уже в системе арены. Виды:

| kind | смысл |
|---|---|
| `move` | цель перемещения |
| `chargeStart` | начать замах, (x,y) — прицел |
| `aim` | обновить прицел во время замаха |
| `throw` | бросить; `power` 0..1 от клиента; sim сверяет со своим таймингом (допуск 0.25) |
| `special` | способность (Q) |

Возвращает `true`, если ввод принят. Неизвестный игрок, мёртвый боец, оглушение — `false`,
без исключений. Значения из сети надо считать враждебными: зажимать координаты, проверять `isFinite`.

### `setBot(state, playerId, isBot)`

Переключает управление бойцом на ИИ и обратно. Сервер зовёт при дисконнекте, AFK,
возвращении игрока. Должно работать в любой момент матча.

### `step(state, dt)`

Продвигает матч на `dt` секунд (сервер зовёт с 1/20). Внутри физика идёт подшагами ≤ 1/60 с,
чтобы поведение не зависело от частоты тиков. Возвращает массив событий шага:

```
{type:'chargeStart', playerId}
{type:'throw', playerId, power, special: null|'explosive'|'freeze'}
{type:'hit', targetId, x, y, freeze, hp}      // попадание, боец жив
{type:'ko', targetId, x, y}                    // третье попадание
{type:'wallHit', x, y}  {type:'miss', x, y}  {type:'explosion', x, y}
{type:'special', playerId, special}  {type:'wallPlaced', playerId}
{type:'matchEnd', winner: 'A'|'B'|null}
```

События — единственный источник для звука и эффектов на клиенте. Новые события добавлять
можно свободно: клиент игнорирует неизвестные.

### `snapshot(state)`

Сериализуемый объект для рендера и сети (через `JSON.stringify`, поэтому без функций и циклов):

```
{ v, tick, time, timeLeft, mode, arena, over, winner,
  players: [{ id, team, role, nick, bot, x, y, hp, stun, koed, koAt, hitAt,
              moving, anim, charging, power, aimX, aimY, special, cd }],
  balls:   [{ id, x, y, z, r, team, ex, fr }],
  walls:   [{ x, y, w, h, team, ttl, life }] }
```

Клиент интерполирует `players[].x/y/anim/power` и `balls[].x/y/z` по `id` между снапшотами —
поэтому у снежков **обязательно** стабильный `id`. Округление до десятых сделано ради
размера JSON: снапшот 4×4 — около 2 КБ, 20 раз в секунду на игрока.

## Обязательные правила для сервера

- **Матч должен заканчиваться.** Таймер `durationMs` с ничьёй обязателен: без него
  два спрятавшихся игрока держат матч и память сервера вечно.
- **Боты должны уметь играть сами**: сервер добирает ими команды и заменяет отключившихся.
- `isOver`/`winner` должны соответствовать событию `matchEnd`.

## Что изменено относительно прототипа

Порт из `SnowBrawl_prototype.html` точный, кроме:

1. `performance.now()` → `state.time` (мс с начала матча), `Math.random()` → `state.rng`.
2. Звук, частицы, тряска, снежинки, следы снежков вынесены в клиент и управляются событиями.
3. Физика подшагами ≤ 1/60 с (иначе на 20 тиках снежок пролетает окно попадания).
4. **ИИ: сила броска подбирается по дистанции** (`(dist+20-140)/380`), а не `0.35+dist/500`.
   В прототипе боты систематически перебрасывали цель и за 80+ секунд не попадали друг в друга
   ни разу; для AI Fill это неприемлемо.
5. **ИИ: обход застревания** — если с прошлого решения бот не сдвинулся, а цель далеко,
   он уходит вбок. В прототипе бот в 1×1 навсегда застревал за колонной у точки появления.
6. После попадания замах сбрасывается (по ТЗ боец теряет возможность атаковать).
7. Добавлены таймер матча (5 мин → ничья), `setBot`, `id` у снежков, события.

## Как проверить свои изменения

```bash
go test ./internal/sim/        # компиляция в goja, детерминизм, матч ботов завершается, ввод
make bench                     # производительность: должно оставаться < 30 % ядра
make dev                       # и открыть http://localhost:8080 → «Тренировка с ботами»
```

При смене правил поднимайте `SIM_VERSION`. Формат снапшота/событий — часть контракта с
клиентом (`web/client/render.js`), а не с сервером: сервер их не читает.
