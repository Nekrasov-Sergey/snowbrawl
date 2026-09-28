-- Первая схема: всё, что раньше лежало в moderation.json, online.log, accounts.json и auth.key.
-- Время везде — миллисекунды unix (INTEGER): сравнивается числами и не зависит от формата
-- драйвера.

-- Служебные значения: ключ подписи кук (auth_secret), отметка импорта старых файлов.
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) STRICT;

-- Игроки: и гости, и вошедшие через Яндекс. Гость — запись без провайдера; вход через Яндекс
-- превращает её в аккаунт (или сливает с существующим), см. internal/accounts.
CREATE TABLE players (
  id         TEXT PRIMARY KEY,               -- "a" + 12 hex, виден клиенту
  provider   TEXT NOT NULL DEFAULT '',       -- '' — гость, 'yandex'
  subject    TEXT NOT NULL DEFAULT '',       -- id пользователя у провайдера
  login      TEXT NOT NULL DEFAULT '',
  nick       TEXT NOT NULL,
  nick_key   TEXT NOT NULL UNIQUE,           -- protocol.NickKey(nick): ник занят навсегда
  nick_auto  INTEGER NOT NULL DEFAULT 0,     -- ник выдал сервер
  rank       TEXT NOT NULL DEFAULT '',
  epoch      INTEGER NOT NULL DEFAULT 0,     -- растёт при «выйти на всех устройствах»
  created_at INTEGER NOT NULL,
  nick_at    INTEGER,                        -- последняя смена ника; NULL — ещё не менялся
  seen_at    INTEGER NOT NULL,
  ban_reason TEXT NOT NULL DEFAULT '',
  banned_at  INTEGER
) STRICT;

CREATE UNIQUE INDEX players_subject ON players(provider, subject) WHERE provider <> '';
-- Для уборки: гости без роли и бана, давно не заходившие.
CREATE INDEX players_gc ON players(seen_at) WHERE provider = '' AND rank = '' AND banned_at IS NULL;
-- Для списка игроков в админке: сортировка по последнему заходу.
CREATE INDEX players_seen ON players(seen_at);

-- Пройденные уроки обучения: множество, поэтому слияние устройств — просто INSERT OR IGNORE.
CREATE TABLE player_lessons (
  player_id TEXT NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  lesson    TEXT NOT NULL,
  PRIMARY KEY (player_id, lesson)
) STRICT, WITHOUT ROWID;

-- Ряд онлайна для графика в админке: пик за минуту, минута — unix-секунды её начала.
CREATE TABLE online_points (
  at INTEGER PRIMARY KEY,
  n  INTEGER NOT NULL
) STRICT;
