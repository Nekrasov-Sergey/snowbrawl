#!/usr/bin/env bash
# Выкладка новой версии на сервер с дренажом текущих матчей.
#
# Использование (на VPS, из каталога с docker-compose.yml и .env):
#   ./deploy.sh v1.2.3            # выкатить тег
#   ./deploy.sh                   # выкатить latest
#   DRAIN_TIMEOUT=60 ./deploy.sh  # ждать матчи не дольше 60 с
#
# Что делает:
#   1. Включает дренаж на работающем сервере: новые матчи не стартуют, игроки видят баннер.
#   2. Ждёт, пока текущие матчи закончатся (не дольше DRAIN_TIMEOUT секунд, по умолчанию 180).
#   3. Скачивает новый образ и перезапускает контейнеры.
#   4. Проверяет /healthz.
set -euo pipefail

cd "$(dirname "$0")"
TAG="${1:-latest}"
DRAIN_TIMEOUT="${DRAIN_TIMEOUT:-180}"
COMPOSE_FILES=(-f docker-compose.yml)
PROFILE=()
if [[ -f .env ]]; then
  # shellcheck disable=SC1091
  set -a; source .env; set +a
fi
if [[ "${SNOWBRAWL_TLS:-false}" == "true" ]]; then
  COMPOSE_FILES+=(-f docker-compose.tls.yml)
  PROFILE=(--profile tls)
fi

IMAGE_BASE="${SNOWBRAWL_IMAGE%%:*}"
export SNOWBRAWL_IMAGE="${IMAGE_BASE}:${TAG}"
BASE_URL="http://127.0.0.1:${SNOWBRAWL_HTTP_PORT:-80}"
if [[ "${SNOWBRAWL_TLS:-false}" == "true" ]]; then BASE_URL="http://127.0.0.1:80"; fi

echo "==> Образ: $SNOWBRAWL_IMAGE"

if curl -fsS "$BASE_URL/healthz" >/dev/null 2>&1; then
  echo "==> Включаю дренаж"
  curl -fsS -X POST -H "X-Admin-Token: $SNOWBRAWL_ADMIN_TOKEN" "$BASE_URL/admin/drain" >/dev/null || true
  waited=0
  while (( waited < DRAIN_TIMEOUT )); do
    live=$(curl -fsS -H "X-Admin-Token: $SNOWBRAWL_ADMIN_TOKEN" "$BASE_URL/admin/state" 2>/dev/null \
      | sed -n 's/.*"matchesLive":\([0-9]*\).*/\1/p')
    live="${live:-0}"
    if [[ "$live" == "0" ]]; then
      echo "==> Живых матчей нет, можно перезапускать"
      break
    fi
    echo "    ждём: матчей в игре $live (прошло ${waited}с из ${DRAIN_TIMEOUT})"
    sleep 5
    waited=$((waited + 5))
  done
else
  echo "==> Сервер не отвечает, дренаж пропущен"
fi

echo "==> Скачиваю образ и перезапускаю"
docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" pull
docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" up -d --remove-orphans

echo "==> Проверяю здоровье"
for _ in $(seq 1 20); do
  if out=$(curl -fsS "$BASE_URL/healthz" 2>/dev/null); then
    echo "==> OK: $out"
    docker image prune -f >/dev/null 2>&1 || true
    exit 0
  fi
  sleep 1
done
echo "!!! Сервер не поднялся, смотрите: docker compose logs server" >&2
exit 1
