# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/), версии — semver.

## [Unreleased]

### Added
- Сервер мультиплеера на Go: WebSocket, сессии по токену, комнаты с кодом `SNB-XXXX`,
  Quick Match с добором ботами, матчи 1×1…4×4 с тиком 20/с.
- Общая симуляция `web/sim/sim.js`, исполняемая на сервере через goja и в браузере (оффлайн).
- Эталонный клиент: ник, меню, лобби комнаты, поиск матча, интерполяция снапшотов, звук и
  эффекты по событиям, тренировка с ботами.
- Реконнект в идущий матч (60 с), AFK → бот (20 с), таймер матча 5 мин → ничья.
- Дренаж перед обновлением, `/healthz`, `/api/version`, админка `/admin/*` под токеном.
- Docker-образ (distroless), docker-compose с профилем TLS (Caddy), `deploy.sh`, GitHub Actions
  (CI и релиз по тегу в GHCR с выкладкой на VPS).
- Документы: ARCHITECTURE, PROTOCOL, SIM_CONTRACT, VERSIONING, RUNBOOK, HANDOVER.

### Changed
- ИИ ботов: сила броска по дистанции и обход застревания (см. SIM_CONTRACT.md, раздел
  «Что изменено относительно прототипа»).
