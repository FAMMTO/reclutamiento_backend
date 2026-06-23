#!/usr/bin/env bash
# setup.sh — provisioning inicial del VPS para Jobbly
# Ejecutar como root en Ubuntu 22.04 LTS
# Uso: bash setup.sh

set -euo pipefail

JOBBLY_USER="jobbly"
INSTALL_DIR="/opt/jobbly"
CADDY_VERSION="2.8.4"
GO_VERSION="1.24.3"

echo "==> Actualizando paquetes"
apt-get update -y && apt-get upgrade -y

echo "==> Instalando dependencias"
apt-get install -y curl wget gnupg2 lsb-release ca-certificates \
    postgresql postgresql-contrib ufw fail2ban

echo "==> Creando usuario jobbly"
id -u "$JOBBLY_USER" &>/dev/null || useradd -r -s /bin/false -d "$INSTALL_DIR" "$JOBBLY_USER"

echo "==> Creando directorio de la app"
mkdir -p "$INSTALL_DIR"
chown "$JOBBLY_USER:$JOBBLY_USER" "$INSTALL_DIR"

echo "==> Instalando Caddy $CADDY_VERSION"
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | tee /etc/apt/sources.list.d/caddy-stable.list
apt-get update -y && apt-get install -y caddy

echo "==> Configurando PostgreSQL"
PG_USER="jobbly"
PG_DB="jobbly_prod"
sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='$PG_USER'" | grep -q 1 || \
    sudo -u postgres psql -c "CREATE USER $PG_USER WITH PASSWORD 'CHANGE_ME';"
sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='$PG_DB'" | grep -q 1 || \
    sudo -u postgres createdb -O "$PG_USER" "$PG_DB"

echo "==> Configurando UFW"
ufw allow ssh
ufw allow http
ufw allow https
ufw --force enable

echo "==> Instalando unit files de systemd"
cp jobbly-api.service    /etc/systemd/system/
cp jobbly-worker.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable jobbly-api jobbly-worker

echo "==> Instalando Caddyfile"
cp Caddyfile /etc/caddy/Caddyfile
mkdir -p /var/log/caddy
chown caddy:caddy /var/log/caddy
systemctl enable caddy

cat <<'MSG'

Setup completo. Pasos manuales restantes:
  1. Copia .env.production.example a /opt/jobbly/.env y rellena los valores reales
  2. Copia los binarios: scp jobbly-api jobbly-worker root@<VPS>:/opt/jobbly/
  3. Ajusta la contraseña de Postgres: sudo -u postgres psql -c "ALTER USER jobbly PASSWORD '<nueva>';"
  4. Inicia los servicios:
       systemctl start jobbly-api jobbly-worker caddy
  5. Verifica: systemctl status jobbly-api jobbly-worker caddy

MSG
