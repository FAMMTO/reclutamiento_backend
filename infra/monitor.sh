#!/usr/bin/env bash
# monitor.sh — chequeo de salud de Jobbly en el VPS.
# Ejecutar desde cron cada 5 minutos:
#   */5 * * * * /opt/jobbly/monitor.sh >> /var/log/jobbly-monitor.log 2>&1
#
# Variables de entorno requeridas (poner en /opt/jobbly/.env.monitor):
#   ALERT_EMAIL — destinatario de alertas (ej. ops@jobbly.mx)
#   API_BASE    — URL base de la API (ej. https://api.jobbly.mx)

set -euo pipefail

ENV_FILE="${1:-/opt/jobbly/.env.monitor}"
[[ -f "$ENV_FILE" ]] && source "$ENV_FILE"

API_BASE="${API_BASE:-https://api.jobbly.mx}"
ALERT_EMAIL="${ALERT_EMAIL:-}"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
FAILURES=()

log() { echo "[$TIMESTAMP] $*"; }

send_alert() {
  local subject="$1" body="$2"
  log "ALERTA: $subject"
  if [[ -n "$ALERT_EMAIL" ]] && command -v mail &>/dev/null; then
    echo "$body" | mail -s "[Jobbly] $subject" "$ALERT_EMAIL"
  fi
}

# ── 1. Liveness (healthz) ─────────────────────────────────────────────────────
http_status=$(curl -sf -o /dev/null -w "%{http_code}" "${API_BASE}/healthz" --max-time 5 || echo "000")
if [[ "$http_status" != "200" ]]; then
  FAILURES+=("healthz HTTP $http_status")
  send_alert "API caída" "GET /healthz devolvió $http_status en $TIMESTAMP"
else
  log "healthz OK"
fi

# ── 2. Readiness (readyz — incluye DB + River) ────────────────────────────────
readyz_body=$(curl -sf "${API_BASE}/readyz" --max-time 5 || echo "TIMEOUT")
if [[ "$readyz_body" == *"river_stuck_jobs"* || "$readyz_body" == "TIMEOUT" ]]; then
  FAILURES+=("readyz: $readyz_body")
  send_alert "Worker River atascado" "GET /readyz: $readyz_body en $TIMESTAMP"
else
  log "readyz OK"
fi

# ── 3. Servicios systemd ──────────────────────────────────────────────────────
for svc in jobbly-api jobbly-worker; do
  state=$(systemctl is-active "$svc" 2>/dev/null || echo "unknown")
  if [[ "$state" != "active" ]]; then
    FAILURES+=("$svc $state")
    send_alert "Servicio $svc caído" "systemctl is-active $svc = $state en $TIMESTAMP"
  else
    log "$svc active"
  fi
done

# ── 4. Postgres: conexiones cerca del límite ──────────────────────────────────
pg_conn=$(sudo -u postgres psql -Atc \
  "SELECT count(*) FROM pg_stat_activity WHERE datname='jobbly_prod'" 2>/dev/null || echo "0")
pg_max=$(sudo -u postgres psql -Atc "SHOW max_connections" 2>/dev/null || echo "100")
if (( pg_conn > pg_max * 80 / 100 )); then
  FAILURES+=("postgres conexiones altas: $pg_conn/$pg_max")
  send_alert "Postgres: conexiones > 80%" "$pg_conn de $pg_max en $TIMESTAMP"
else
  log "postgres conexiones OK ($pg_conn/$pg_max)"
fi

# ── 5. Espacio en disco ───────────────────────────────────────────────────────
disk_pct=$(df / --output=pcent | tail -1 | tr -d ' %')
if (( disk_pct > 85 )); then
  FAILURES+=("disco ${disk_pct}% lleno")
  send_alert "Disco > 85% lleno" "${disk_pct}% usado en $TIMESTAMP"
else
  log "disco OK (${disk_pct}%)"
fi

# ── Resumen ───────────────────────────────────────────────────────────────────
if [[ ${#FAILURES[@]} -gt 0 ]]; then
  log "FALLOS: ${FAILURES[*]}"
  exit 1
else
  log "todos los checks OK"
fi
