SHELL := /bin/bash
COMPOSE := docker compose -f deploy/compose/infra.yml
DB_URL ?= postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable

.PHONY: infra-up infra-down infra-logs psql migrate migrate-down ctrlapi-build ctrlapi-run fmt vet test

infra-up:
	$(COMPOSE) up -d
	@echo "Waiting for Postgres..."
	@for i in $$(seq 1 30); do \
		docker exec stayconnect-pg pg_isready -U stayconnect >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@echo "Infra ready."

infra-down:
	$(COMPOSE) down

infra-logs:
	$(COMPOSE) logs -f --tail=100

psql:
	docker exec -it stayconnect-pg psql -U stayconnect -d stayconnect

migrate:
	@for f in $$(ls control-plane/migrations/*.up.sql | sort); do \
		echo ">> $$f"; \
		docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -v ON_ERROR_STOP=1 < $$f || exit 1; \
	done

migrate-down:
	@for f in $$(ls control-plane/migrations/*.down.sql | sort -r); do \
		echo ">> $$f"; \
		docker exec -i stayconnect-pg psql -U stayconnect -d stayconnect -v ON_ERROR_STOP=1 < $$f || exit 1; \
	done

ctrlapi-build:
	cd control-plane && go build -o ../bin/ctrlapi ./cmd/ctrlapi

ctrlapi-run:
	cd control-plane && go run ./cmd/ctrlapi

ctrlapi-install: ctrlapi-build
	@[ $$(realpath bin/ctrlapi) = $$(realpath /opt/stayconnect/bin/ctrlapi 2>/dev/null || echo x) ] || install -m 0755 bin/ctrlapi /opt/stayconnect/bin/ctrlapi
	install -m 0644 deploy/systemd/stayconnect-ctrlapi.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-ctrlapi.service
	systemctl restart stayconnect-ctrlapi.service
	@[ -x /opt/stayconnect/bin/stayconnect-backup-cleanup ] && /opt/stayconnect/bin/stayconnect-backup-cleanup --apply || true

# Install the backup-retention tool, config, and daily safety-net timer (both hosts).
backup-cleanup-install:
	install -m 0755 deploy/scripts/stayconnect-backup-cleanup.sh /opt/stayconnect/bin/stayconnect-backup-cleanup
	[ -f /etc/stayconnect/backup-retention.conf ] || install -m 0644 deploy/scripts/backup-retention.conf /etc/stayconnect/backup-retention.conf
	install -m 0644 deploy/systemd/stayconnect-backup-cleanup.service /etc/systemd/system/
	install -m 0644 deploy/systemd/stayconnect-backup-cleanup.timer /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-backup-cleanup.timer

scd-build:
	cd data-plane && go build -o ../bin/scd ./cmd/scd

portald-build:
	cd data-plane && go build -o ../bin/portald ./cmd/portald

acctd-build:
	cd data-plane && go build -o ../bin/acctd ./cmd/acctd

dataplane-build: scd-build portald-build acctd-build

phase1-install: dataplane-build
	@mkdir -p /opt/stayconnect/bin
	@[ $$(realpath bin/scd)     = $$(realpath /opt/stayconnect/bin/scd     2>/dev/null || echo x) ] || install -m 0755 bin/scd     /opt/stayconnect/bin/scd
	@[ $$(realpath bin/portald) = $$(realpath /opt/stayconnect/bin/portald 2>/dev/null || echo x) ] || install -m 0755 bin/portald /opt/stayconnect/bin/portald
	chmod 0755 /opt/stayconnect/bin/scd /opt/stayconnect/bin/portald
	install -m 0644 deploy/systemd/stayconnect-scd.service     /etc/systemd/system/
	install -m 0644 deploy/systemd/stayconnect-portald.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-scd.service
	systemctl enable --now stayconnect-portald.service

web-install:
	cd web-admin && npm install --no-fund --no-audit
	install -m 0644 deploy/systemd/stayconnect-web-admin.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-web-admin.service
	systemctl restart stayconnect-web-admin.service

phase2-install: dataplane-build
	chmod 0755 deploy/scripts/tc-setup.sh
	@[ $$(realpath bin/acctd) = $$(realpath /opt/stayconnect/bin/acctd 2>/dev/null || echo x) ] || install -m 0755 bin/acctd /opt/stayconnect/bin/acctd
	chmod 0755 /opt/stayconnect/bin/acctd
	install -m 0644 deploy/systemd/stayconnect-tc-setup.service /etc/systemd/system/
	install -m 0644 deploy/systemd/stayconnect-acctd.service    /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-tc-setup.service
	systemctl restart stayconnect-scd.service
	systemctl enable --now stayconnect-acctd.service

# ---- Edge-first refactor -------------------------------------------------
# The site-local database (one per hotel). SITE_DB_NAME on the pilot shares
# the central Postgres container; production appliances run their own.
SITE_DB_NAME ?= stayconnect_site
SITE_DB_URL  ?= postgres://stayconnect:stayconnect@127.0.0.1:5432/$(SITE_DB_NAME)?sslmode=disable

edge-db-create:
	docker exec stayconnect-pg psql -U stayconnect -d postgres -tc \
		"SELECT 1 FROM pg_database WHERE datname = '$(SITE_DB_NAME)'" | grep -q 1 || \
		docker exec stayconnect-pg psql -U stayconnect -d postgres -c "CREATE DATABASE $(SITE_DB_NAME)"

edge-migrate: edge-db-create
	@for f in $$(ls data-plane/migrations/*.up.sql | sort); do \
		echo ">> $$f"; \
		docker exec -i stayconnect-pg psql -U stayconnect -d $(SITE_DB_NAME) -v ON_ERROR_STOP=1 < $$f || exit 1; \
	done

edged-build:
	cd data-plane && go build -o ../bin/edged ./cmd/edged

netd-build:
	cd data-plane && go build -o ../bin/netd ./cmd/netd

netd-install: netd-build
	@[ $$(realpath bin/netd) = $$(realpath /opt/stayconnect/bin/netd 2>/dev/null || echo x) ] || install -m 0755 bin/netd /opt/stayconnect/bin/netd
	install -m 0644 deploy/systemd/stayconnect-netd.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-netd.service
	systemctl restart stayconnect-netd.service

# Apply the site-local networking migration (Phase 19) to the site DB.
edge-net-migrate:
	docker exec -i stayconnect-pg psql -U stayconnect -d $(SITE_DB_NAME) -v ON_ERROR_STOP=1 < data-plane/migrations/0002_edge_networking.up.sql

sitemigrate-build:
	cd data-plane && go build -o ../bin/sitemigrate ./cmd/sitemigrate

edged-install: edged-build
	@[ $$(realpath bin/edged) = $$(realpath /opt/stayconnect/bin/edged 2>/dev/null || echo x) ] || install -m 0755 bin/edged /opt/stayconnect/bin/edged
	install -m 0644 deploy/systemd/stayconnect-edged.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable --now stayconnect-edged.service
	systemctl restart stayconnect-edged.service

# Build the Hotel Admin standalone bundle on THIS machine (workstation/CI) and
# produce hotel-admin/hotel-admin-deploy.tgz. Never build on the appliance.
hotel-admin-package:
	bash deploy/scripts/deploy-hotel-admin.sh package hotel-admin

# Install a pre-built bundle on the appliance (atomic release + symlink flip,
# restarts only the hotel-admin unit, rolls back on failure). Requires
# hotel-admin/hotel-admin-deploy.tgz to have been produced by the package step
# and copied to the appliance.
hotel-admin-install:
	install -m 0644 deploy/systemd/stayconnect-hotel-admin.service /etc/systemd/system/
	install -m 0755 deploy/scripts/deploy-hotel-admin.sh /opt/stayconnect/bin/deploy-hotel-admin.sh
	@mkdir -p /opt/stayconnect/releases/hotel-admin
	bash deploy/scripts/deploy-hotel-admin.sh install hotel-admin/hotel-admin-deploy.tgz
	systemctl enable stayconnect-hotel-admin.service

fmt:
	cd control-plane && gofmt -w .
	cd data-plane && gofmt -w .

vet:
	cd control-plane && go vet ./...
	cd data-plane && go vet ./...

test:
	cd control-plane && go test ./...
	cd data-plane && go test ./...
	cd license && go test ./...
