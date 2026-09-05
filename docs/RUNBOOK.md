# Эксплуатация (RUNBOOK)

Всё, что нужно, чтобы поднять, обновить и починить сервер, не читая код.

## Что нужно

- VPS: Ubuntu 24.04, 1–2 vCPU, 2 ГБ RAM, 20 ГБ диска, публичный IPv4. На 100 игроков онлайн
  хватает с большим запасом (сервер занимает ~50–100 МБ RAM и доли ядра).
- Docker и Docker Compose plugin.
- Открытые порты: 80 (и 443 при TLS), 22 для SSH.

## Первый запуск на VPS

```bash
# 1. Docker
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER && newgrp docker

# 2. Каталог и файлы
sudo mkdir -p /opt/snowbrawl && sudo chown $USER /opt/snowbrawl && cd /opt/snowbrawl
# скопируйте сюда deploy/docker-compose.yml, deploy/docker-compose.tls.yml,
# deploy/Caddyfile, deploy/deploy.sh, deploy/.env.example  (scp или git clone репозитория)
cp .env.example .env
nano .env            # SNOWBRAWL_ADMIN_TOKEN=$(openssl rand -hex 24), образ, домен

# 3. Запуск (без домена, по IP)
docker compose up -d
curl http://127.0.0.1/healthz
```

Игра: `http://<IP-сервера>/`. Админка: `http://<IP>/admin/?token=<SNOWBRAWL_ADMIN_TOKEN>`.

### Включить домен и HTTPS

1. Купить домен, создать A-запись на IP сервера, дождаться, пока `ping домен` покажет IP.
2. В `.env` указать `SNOWBRAWL_DOMAIN=ваш.домен` и добавить `SNOWBRAWL_TLS=true`.
3. Перезапустить со стеком TLS:

```bash
docker compose -f docker-compose.yml -f docker-compose.tls.yml --profile tls up -d
```

Caddy сам получит и будет продлевать сертификат Let's Encrypt. Игра: `https://ваш.домен/`.

## Обновление

Автоматически: пуш тега `v*` в GitHub → образ → выкладка (см. VERSIONING.md).
Вручную на VPS:

```bash
cd /opt/snowbrawl && ./deploy.sh v0.2.0     # или ./deploy.sh для latest
```

Скрипт включает дренаж (игроки видят баннер, новые матчи не стартуют), ждёт окончания
текущих матчей до 3 минут, обновляет контейнер, проверяет `/healthz`.

Откат: `./deploy.sh v0.1.9`.

## Диагностика

| Симптом | Что делать |
|---|---|
| Сайт не открывается | `docker compose ps`, `docker compose logs --tail 200 server`; `curl -v http://127.0.0.1/healthz` |
| Открывается, но «нет соединения с сервером» (красная точка слева сверху) | WebSocket не проходит: проверьте, что за прокси (если он свой) включён апгрейд `Upgrade: websocket`; Caddy делает это сам |
| Игроки жалуются на «сервер переполнен» | лимит `SNOWBRAWL_MAX_CONNS` (200) — поднять в `.env` и `docker compose up -d` |
| Матчи «залипли» | `/admin/state` покажет матчи и тики; матч без людей удаляется сам через 60 с, любой матч заканчивается таймером через 5 мин |
| Нужно срочно перезапустить | `docker compose restart server` — текущие матчи получат «матч прерван», клиенты переподключатся сами |
| Посмотреть кто онлайн | `/admin/?token=…` или `curl -H "X-Admin-Token: …" http://127.0.0.1/admin/state` |
| Отменить дренаж | `curl -X DELETE -H "X-Admin-Token: …" http://127.0.0.1/admin/drain` |

Логи — JSON в stdout контейнера, ротация 5×20 МБ настроена в compose:
`docker compose logs -f server`. Уровень: `SNOWBRAWL_LOG_LEVEL=debug` в `.env`.

## Переменные окружения сервера

| Переменная | По умолчанию | Смысл |
|---|---|---|
| `SNOWBRAWL_ADDR` | `:8080` | адрес прослушивания внутри контейнера |
| `SNOWBRAWL_ADMIN_TOKEN` | — | токен админки; пустой = админка выключена |
| `SNOWBRAWL_WEB_DIR` | — | каталог клиента с диска (разработка) |
| `SNOWBRAWL_MAX_CONNS` | 200 | максимум WebSocket-соединений |
| `SNOWBRAWL_LOG_LEVEL` | info | debug/info/warn/error |
| `SNOWBRAWL_LOG_PRETTY` | false | человекочитаемые логи |
| `SNOWBRAWL_TRUST_PROXY` | false | брать IP из X-Forwarded-For (за Caddy — true) |
| `SNOWBRAWL_QUEUE_WAIT` | 10s | ожидание живых игроков в Quick Match |
| `SNOWBRAWL_RECONNECT_TTL` | 60s | сколько держать место за отключившимся |
| `SNOWBRAWL_AFK_TIMEOUT` | 20s | бездействие до передачи бойца боту |
| `SNOWBRAWL_ROOM_TTL` | 10m | жизнь пустой комнаты |
| `SNOWBRAWL_ROOMS_PER_IP` | 3 | живых комнат на IP |
| `SNOWBRAWL_MSG_RATE` | 30 | сообщений/с на соединение |
| `SNOWBRAWL_DRAIN_TIMEOUT` | 3m | сколько показывать «до перезапуска» |

## Резервные копии

Не нужны: сервер не хранит данных. Достаточно сохранить `.env` (токен админки, домен).

## Безопасность

- Персональные данные не собираются: только ник, выбранный игроком, и IP в логах
  (стандартные технические логи).
- Админка защищена токеном; при утечке — сменить в `.env` и `docker compose up -d`.
- Обновления ОС: `sudo apt update && sudo apt upgrade` раз в месяц; Docker-образ
  распространяется через GHCR из этого репозитория.
