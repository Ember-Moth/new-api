FRONTEND_DIR = ./frontend
BACKEND_DIR = .

.PHONY: all build-frontend build-all-frontends start-backend dev dev-api dev-web reset-setup

all: build-all-frontends start-backend

build-frontend:
	@echo "Building frontend..."
	@cd $(FRONTEND_DIR) && bun install && DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION=$(cat ../VERSION) bun run build

build-all-frontends: build-frontend

start-backend:
	@echo "Starting backend dev server..."
	@cd $(BACKEND_DIR) && go run main.go &

dev-api:
	@echo "Starting backend dev server..."
	@cd $(BACKEND_DIR) && go run main.go

dev-web:
	@echo "Starting frontend dev server..."
	@cd $(FRONTEND_DIR) && bun install && bun run dev

dev: start-backend dev-web

reset-setup:
	@echo "Resetting local setup wizard state..."
	@if [ -z "$$SQL_DSN" ]; then \
		echo "SQL_DSN is required for PostgreSQL-only local development."; \
		exit 1; \
	fi
	@psql "$$SQL_DSN" -v ON_ERROR_STOP=1 \
		-c "DELETE FROM setups; DELETE FROM users WHERE role = 100; DELETE FROM options WHERE key IN ('SelfUseModeEnabled', 'DemoSiteEnabled');"
	@echo "PostgreSQL setup state reset. Restart the local backend process before testing the setup wizard."
