# Atalhos para os rituais do ambiente local. Cada alvo é o comando que o
# `README.md` publica, e não uma abstração no lugar dele: quem quer entender o
# ritual lê o README, e quem já entendeu digita o alvo.

POSTGRES_USER ?= junglegaming
POSTGRES_PASSWORD ?= junglegaming
APP_DB ?= junglegaming
SUITE_DB ?= junglegaming_test

# Dentro da rede do Compose o host do banco é o nome do serviço; no host é
# localhost. O `go test` roda no host, o `migrate` roda na rede.
APP_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(APP_DB)?sslmode=disable
SUITE_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/$(SUITE_DB)?sslmode=disable
SUITE_HOST_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:5432/$(SUITE_DB)?sslmode=disable

# O banco de destino vai nos argumentos do migrate. A variável MIGRATE_DB do
# `compose.yaml` é interpolada na leitura do arquivo, então `-e MIGRATE_DB=x`
# aplica no banco errado e imprime `no change` com código zero.
MIGRATE := docker compose run --rm migrate -path=/migrations -database

PSQL := docker compose exec -T postgres psql -U $(POSTGRES_USER)

.DEFAULT_GOAL := help
.PHONY: help up down provision migrate test test-journey cover-journey mutation verify migrate-reversibility

help: ## lista os alvos
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | sed -e 's/:.*## /|/' | awk -F'|' '{printf "%-24s %s\n", $$1, $$2}'

up: ## sobe a stack inteira e espera cada serviço ficar saudável
	docker compose up -d --build --wait

down: ## derruba a stack e descarta os volumes dela, voltando ao estado limpo
	docker compose down -v

provision: ## cria as filas, a DLQ, o tópico e o remetente IAM no broker local
	terraform -chdir=deploy/terraform/localstack init -input=false
	terraform -chdir=deploy/terraform/localstack apply -auto-approve -input=false

migrate: ## aplica o schema nos dois bancos, cada um nomeado no comando
	$(MIGRATE) "$(APP_URL)" up
	$(PSQL) -d $(APP_DB) -tAc "SELECT 1 FROM pg_database WHERE datname='$(SUITE_DB)'" | grep -q 1 \
		|| docker compose exec -T postgres createdb -U $(POSTGRES_USER) $(SUITE_DB)
	$(MIGRATE) "$(SUITE_URL)" up

test: ## a suíte de unidade, que não sobe Docker
	go test -race -count=1 ./...

test-journey: ## a suíte de jornada: em série, e contra o banco dela
	DATABASE_URL="$(SUITE_HOST_URL)" go test -race -count=1 -p 1 -tags=integration ./...

# O perfil unitário não vê `//go:build integration`, e o repositório SQL aparece
# a 35–56% enquanto a suíte de jornada o verifica a 94–100%. `go tool covdata`
# mescla os contadores de cada binário; `-coverpkg` com `-coverprofile`
# repetiria cada bloco uma vez por binário. Uma suíte que falha interrompe o
# alvo, então um perfil só existe quando as duas suítes passaram.
cover-journey: ## perfil de cobertura somando a suíte de unidade e a de jornada
	rm -rf .quality/covdata .quality/cover-integration.out
	mkdir -p .quality/covdata
	DATABASE_URL="$(SUITE_HOST_URL)" \
	AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
		go test -count=1 -p 1 -tags=integration -cover -coverpkg=./internal/... ./... \
		-args -test.gocoverdir="$$PWD/.quality/covdata"
	go tool covdata textfmt -i=.quality/covdata -o=.quality/cover-integration.out

# O coeficiente é a margem de tempo de cada mutante sobre o baseline medido uma
# vez no começo. O padrão do gremlins é calibrado sobre baselines de
# milissegundos: com ele a maioria dos mutantes estoura sem nunca rodar. Quem
# pede a margem maior é o mutante que inverte o ciclo de vida do processo, que só
# falha quando o prazo da própria suíte dispara.
mutation: ## relatório de mutação do gremlins sobre o módulo
	mkdir -p .quality
	gremlins unleash --timeout-coefficient=30 --output=.quality/gremlins.json .

verify: ## responde se o ambiente está no estado que os arquivos versionados declaram
	go run -C scripts/envcheck . -root "$$PWD"

# Este alvo derruba o schema, e é por isso que o nome dele diz contra quem: o
# banco da suíte, nunca o da aplicação, e nunca como parte de `up`.
migrate-reversibility: ## sobe, reverte e sobe de novo o banco da suíte, e confere que nada ficou sujo
	$(MIGRATE) "$(SUITE_URL)" up
	$(MIGRATE) "$(SUITE_URL)" down -all
	$(MIGRATE) "$(SUITE_URL)" up
	$(PSQL) -d $(SUITE_DB) -tAc "SELECT dirty FROM schema_migrations" | grep -qx f
	$(PSQL) -d $(APP_DB) -v ON_ERROR_STOP=1 -c "BEGIN; SET ROLE wager_app; \
		INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at) \
		VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000002', 'BRL', 0, 1, now(), now()); \
		ROLLBACK"
	@echo "reversibility ok: the suite database is clean and the application database still writes through wager_app"
