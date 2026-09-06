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
#   4. Проверяет /healthz; если сервер не поднялся — возвращает предыдущий образ.
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
# С Caddy сервер не публикует 80-й порт: стучаться туда нельзя, у Caddy нет сайта для
# Host: 127.0.0.1 и он ответит 404. Ходим прямо в сервер на loopback (см. docker-compose.tls.yml).
if [[ "${SNOWBRAWL_TLS:-false}" == "true" ]]; then BASE_URL="http://127.0.0.1:8080"; fi

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

# Образ, который работает прямо сейчас: если новая сборка не поднимется, вернём его.
PREV_IMAGE=""
CID=$(docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" ps -q server 2>/dev/null | head -1 || true)
if [[ -n "$CID" ]]; then
  PREV_IMAGE=$(docker inspect -f '{{.Config.Image}}' "$CID" 2>/dev/null || true)
fi

echo "==> Скачиваю образ и перезапускаю"
docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" pull
docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" up -d --remove-orphans

wait_healthy() {
  for _ in $(seq 1 20); do
    if out=$(curl -fsS "$BASE_URL/healthz" 2>/dev/null); then
      echo "$out"
      return 0
    fi
    sleep 1
  done
  return 1
}

echo "==> Проверяю здоровье"
if out=$(wait_healthy); then
  echo "==> OK: $out"
  docker image prune -f >/dev/null 2>&1 || true
  exit 0
fi

# Новая сборка не отвечает. Контейнер уже заменён, поэтому без возврата игра лежит до следующего
# успешного деплоя. Поднимаем обратно предыдущий образ и всё равно выходим с ошибкой, чтобы шаг
# деплоя в Actions остался красным.
echo "!!! Сервер не поднялся на $SNOWBRAWL_IMAGE" >&2
if [[ -n "$PREV_IMAGE" && "$PREV_IMAGE" != "$SNOWBRAWL_IMAGE" ]]; then
  echo "!!! Возвращаю предыдущий образ: $PREV_IMAGE" >&2
  export SNOWBRAWL_IMAGE="$PREV_IMAGE"
  docker compose "${COMPOSE_FILES[@]}" "${PROFILE[@]}" up -d --remove-orphans
  if out=$(wait_healthy); then
    echo "!!! Откат выполнен, игра работает на прежней версии: $out" >&2
  else
    echo "!!! Откат не помог, смотрите: docker compose logs server" >&2
  fi
else
  echo "!!! Предыдущий образ неизвестен, смотрите: docker compose logs server" >&2
fi
exit 1
