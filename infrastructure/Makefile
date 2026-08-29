# The consolidated Hello Sense backend: orb, orb-algo, Postgres.
#
# `make` on its own lists everything. The common path is:
#
#     make init            create .env and secrets/
#     make build
#     make up
#     make migrate
#     make verify
#
# PLATFORM. On Linux the device-facing TLS terminator runs as a fourth
# container with host networking. On macOS it does not, because Docker Desktop
# would put a userland NAT hop in front of the one client on the network that
# cannot tolerate surprises. This Makefile picks the right shape for you; see
# README.md if you want the reasoning.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

UNAME_S := $(shell uname -s)

# Exported rather than passed as -f flags, so scripts/ inherit the same view
# of the project without every one of them re-deriving it.
ifeq ($(UNAME_S),Linux)
  COMPOSE_FILE := docker-compose.yml:docker-compose.linux.yml
  SENSE_NOTE := container (host networking)
else
  COMPOSE_FILE := docker-compose.yml
  SENSE_NOTE := ON THE HOST, not Docker (see README)
endif

# ORB_PUBLIC=1 adds Caddy in front of the app API, on 8443, with a real
# certificate. Opt-in, because it is the only listener here that faces the
# internet and it needs a certificate in /etc/letsencrypt that a home
# deployment has no reason to have.
ifneq ($(strip $(ORB_PUBLIC)),)
  COMPOSE_FILE := $(COMPOSE_FILE):docker-compose.public.yml
  PUBLIC_NOTE := yes, Caddy on $${APP_PUBLISH:-8443}
else
  PUBLIC_NOTE := no (set ORB_PUBLIC=1)
endif
export COMPOSE_FILE

COMPOSE := docker compose
PSQL_USER := $(shell . ./.env 2>/dev/null; echo $${POSTGRES_USER:-hello})
PSQL_DB   := $(shell . ./.env 2>/dev/null; echo $${POSTGRES_DB:-orb})
STAMP     := $(shell date +%Y-%m-%d-%H%M%S)

.PHONY: help init build up down restart ps logs logs-orb migrate migrate-status migrate-baseline \
        verify psql shell backup restore check-secrets config clean nuke

## ---------------------------------------------------------------- meta ----

help: ## Show this help
	@echo ""
	@echo "  Hello Sense backend, on $(UNAME_S)"
	@echo "  sense-server: $(SENSE_NOTE)"
	@echo ""
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""

config: ## Print the fully resolved Compose configuration
	@$(COMPOSE) config

## --------------------------------------------------------------- setup ----

init: ## Create .env and secrets/ from the templates (never overwrites)
	@if [ -f .env ]; then \
	  echo ".env already exists, leaving it alone"; \
	else \
	  cp .env.example .env; \
	  echo "created .env  <- edit POSTGRES_PASSWORD and the APNS block"; \
	fi
	@mkdir -p secrets backups acme
	# 711, not 700. orb runs as uid 10001 and has to traverse this directory to
	# reach the APNs key; 700 owned by your user means it cannot, and orb
	# crash-loops on "permission denied" for a file that looks perfectly
	# readable. 711 lets any uid open a file it already knows the name of
	# without being able to list the directory. The files themselves stay 600.
	# Docker Desktop on macOS rewrites ownership on bind mounts, so this only
	# ever bites on Linux, which is to say on the machine you deploy to.
	@chmod 711 secrets
	@echo "secrets/ ready (gitignored). Put the APNs .p8 and the device certs here."
	@echo "  then:  sudo chown 10001 secrets/AuthKey_*.p8 && sudo chmod 600 secrets/AuthKey_*.p8"
	@echo ""
	@echo "next:  make build && make up && make migrate"

## --------------------------------------------------------------- build ----

build: ## Build the orb and orb-algo images
	@$(COMPOSE) build

build-nocache: ## Rebuild from scratch, ignoring layer cache
	@$(COMPOSE) build --no-cache

## ----------------------------------------------------------- lifecycle ----

up: ## Start everything in the background
	@$(COMPOSE) up -d
	@echo ""
	@$(MAKE) --no-print-directory ps

down: ## Stop and remove containers. Volumes are KEPT.
	@$(COMPOSE) down

restart: ## Restart every service
	@$(COMPOSE) restart

ps: ## Show service status
	@$(COMPOSE) ps

logs: ## Tail logs from everything
	@$(COMPOSE) logs -f --tail=100

logs-orb: ## Tail orb's logs only
	@$(COMPOSE) logs -f --tail=200 orb

## --------------------------------------------------------------- schema ----

migrate: ## Apply pending schema migrations
	@./scripts/migrate.sh

migrate-status: ## Show which migrations are applied
	@./scripts/migrate.sh --status

migrate-baseline: ## Adopt a database whose schema was already applied by hand
	@./scripts/migrate.sh --baseline

psql: ## Open a psql shell on the orb database
	@$(COMPOSE) exec postgres psql -U $(PSQL_USER) -d $(PSQL_DB)

shell: ## Open a shell inside the orb container
	@$(COMPOSE) exec orb /bin/sh

## --------------------------------------------------------------- verify ----

verify: ## Prove the stack actually works, not just that it started
	@echo "== container health =="
	@$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}'
	@echo ""
	@echo "== orb app API =="
	@if curl -fsS -m 5 http://localhost:$${ORB_API_PORT:-9999}/ping >/dev/null 2>&1; then \
	  echo "  /ping           OK"; \
	else \
	  echo "  /ping           FAILED"; exit 1; \
	fi
	@echo ""
	@echo "== orb-algo =="
	@if $(COMPOSE) exec -T orb-algo curl -fsS -m 5 http://localhost:8090/health >/dev/null 2>&1; then \
	  echo "  /health         OK"; \
	else \
	  echo "  /health         FAILED"; exit 1; \
	fi
	@echo ""
	@echo "== database =="
	@$(COMPOSE) exec -T postgres psql -U $(PSQL_USER) -d $(PSQL_DB) -qtA \
	  -c "SELECT '  tables         ' || count(*) FROM information_schema.tables WHERE table_schema='public';" \
	  -c "SELECT '  migrations     ' || count(*) FROM schema_migrations;" 2>/dev/null \
	  || echo "  (schema_migrations missing: run make migrate)"
	@echo ""
	@echo "== timezone data in the orb image =="
	@echo "  (missing tzdata silently breaks alarms across a DST change)"
	@if $(COMPOSE) exec -T orb ls /usr/share/zoneinfo/UTC >/dev/null 2>&1; then \
	  echo "  zoneinfo        OK"; \
	else \
	  echo "  zoneinfo        MISSING"; exit 1; \
	fi
	@echo ""
	@echo "all checks passed"

## --------------------------------------------------------------- backup ----

backup: ## Dump every database to ./backups (gitignored)
	@mkdir -p backups/$(STAMP)
	@$(COMPOSE) exec -T postgres pg_dumpall -U $(PSQL_USER) | gzip > backups/$(STAMP)/pg_dumpall.sql.gz
	@$(COMPOSE) exec -T postgres pg_dump -U $(PSQL_USER) -Fc -d $(PSQL_DB) > backups/$(STAMP)/$(PSQL_DB).dump
	@echo "wrote backups/$(STAMP)/"
	@ls -la backups/$(STAMP)/

restore: ## Restore DUMP=<file> into the orb database. Destructive.
	@test -n "$(DUMP)" || { echo "usage: make restore DUMP=backups/<stamp>/orb.dump"; exit 1; }
	@test -f "$(DUMP)" || { echo "no such file: $(DUMP)"; exit 1; }
	@echo "This OVERWRITES the $(PSQL_DB) database from $(DUMP)."
	@read -p "Type the database name to confirm: " ans; [ "$$ans" = "$(PSQL_DB)" ] || { echo "aborted"; exit 1; }
	@$(COMPOSE) exec -T postgres pg_restore -U $(PSQL_USER) -d $(PSQL_DB) --clean --if-exists --no-owner < "$(DUMP)"
	@echo "restored"

## -------------------------------------------------------------- hygiene ----

check-secrets: ## Fail if anything secret would be committed
	@git rev-parse --is-inside-work-tree >/dev/null 2>&1 || { \
	  echo "  not a git repository, so there is nothing to check yet."; \
	  echo "  run this again once this directory is committed."; exit 0; }
	@echo "checking for secrets that git can see..."
	@bad=$$(git ls-files --cached --others --exclude-standard 2>/dev/null \
	  | grep -E '(^|/)(\.env$$|secrets/|.*\.(p8|key|pem|der|crt|csr|srl|jar|dump)$$)' || true); \
	if [ -n "$$bad" ]; then \
	  echo ""; echo "  REFUSING: these are visible to git and must not be:"; \
	  echo "$$bad" | sed 's/^/    /'; echo ""; exit 1; \
	fi
	@echo "  clean: no keys, certs, dumps or .env are tracked or untracked-but-visible"

clean: ## Remove built images. Data is untouched.
	@$(COMPOSE) down --rmi local

nuke: ## Remove containers AND the database volume. Irreversible.
	@echo "This DELETES the database volume. Everything in Postgres is lost."
	@read -p "Type NUKE to confirm: " ans; [ "$$ans" = "NUKE" ] || { echo "aborted"; exit 1; }
	@$(COMPOSE) down -v
