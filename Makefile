VPS ?= root@api.jobbly.mx
INSTALL_DIR := /opt/jobbly

.PHONY: build deploy deploy-api deploy-worker restart logs

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/jobbly-api   ./cmd/api
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/jobbly-worker ./cmd/worker

deploy: build deploy-api deploy-worker restart

deploy-api:
	scp bin/jobbly-api $(VPS):$(INSTALL_DIR)/jobbly-api

deploy-worker:
	scp bin/jobbly-worker $(VPS):$(INSTALL_DIR)/jobbly-worker

restart:
	ssh $(VPS) "systemctl restart jobbly-api jobbly-worker"

logs:
	ssh $(VPS) "journalctl -u jobbly-api -u jobbly-worker -f --no-pager"

# Instala los unit files de systemd en el VPS (solo necesario la primera vez o al cambiarlos)
install-units:
	scp infra/jobbly-api.service    $(VPS):/etc/systemd/system/
	scp infra/jobbly-worker.service $(VPS):/etc/systemd/system/
	ssh $(VPS) "systemctl daemon-reload && systemctl enable jobbly-api jobbly-worker"
