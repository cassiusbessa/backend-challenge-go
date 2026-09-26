# Atalhos para os rituais do ambiente local. Cada alvo é o comando que o
# `README.md` publica, e não uma abstração no lugar dele: quem quer entender o
# ritual lê o README, e quem já entendeu digita o alvo.

# O Compose lê o `.env` sozinho; o make não. Sem esta inclusão um `.env` que troca
# o banco ou a senha constrói um ambiente e deixa os alvos daqui, e o verificador
# que eles chamam, falando com outro. Valor com `#` viraria comentário aqui, e é
# por isso que `.env.example` pede percent-encoding na senha.
-include .env

POSTGRES_USER ?= junglegaming
POSTGRES_PASSWORD ?= junglegaming
POSTGRES_DB ?= junglegaming
# O Compose chama de POSTGRES_DB o banco que o Makefile chama de APP_DB. É o mesmo
# banco, e o default de um é o valor do outro para os dois não divergirem.
APP_DB ?= $(POSTGRES_DB)
SUITE_DB ?= junglegaming_test
KC_BOOTSTRAP_ADMIN_USERNAME ?= admin
KC_BOOTSTRAP_ADMIN_PASSWORD ?= admin
GF_SECURITY_ADMIN_USER ?= admin
GF_SECURITY_ADMIN_PASSWORD ?= admin
AWS_ACCESS_KEY_ID ?= test
AWS_SECRET_ACCESS_KEY ?= test
AWS_REGION ?= us-east-1
# O número de réplicas do `up`, com o mesmo padrão do Compose: o `WAGER_REPLICAS`
# do `.env`, ou três.
WAGER_REPLICAS ?= 3
REPLICAS ?= $(WAGER_REPLICAS)

# O Compose recusa só parte dos números inválidos, e sem nomear a variável: o
# `--scale` com zero derruba as réplicas, e com -1 entra em pânico. O Kubernetes
# aceita zero e fica sem réplica. Por isso o número é conferido antes de qualquer
# passo, com a mesma mensagem em todo alvo que o usa.
REPLICAS_REFUSED = echo "REPLICAS must be an integer of at least 1, got '$(REPLICAS)'" >&2; exit 2
CHECK_REPLICAS = case '$(REPLICAS)' in ''|*[!0-9]*) $(REPLICAS_REFUSED);; esac; \
	if [ '$(REPLICAS)' -lt 1 ]; then $(REPLICAS_REFUSED); fi

# O cluster das réplicas. O Kind roda pela versão fixada aqui, sem instalação no
# host; `KIND=kind` usa um binário instalado. O contexto vai explícito em toda
# chamada, para o `kubectl` nunca agir sobre o cluster que o operador deixou
# ativo.
KIND ?= go run sigs.k8s.io/kind@v0.33.0
CLUSTER ?= junglegaming
NAMESPACE ?= junglegaming
KUBECTL ?= kubectl --context kind-$(CLUSTER)
# As imagens que o nó não baixa: a das réplicas, construída pelo Compose, e as
# duas que o cluster usa na mesma versão do Compose.
#
# Elas entram por arquivo, e não por `kind load docker-image`: com o image store
# do containerd, o `docker save` de uma imagem baixada exporta o índice de todas
# as plataformas, o conteúdo só existe para a do host, e o import no nó falha.
CLUSTER_IMAGES := junglegaming-wager:latest migrate/migrate:v4.19.0 prom/prometheus:v3.13.3-busybox

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

# Uma escrita pelo papel da aplicação, numa transação desfeita: prova que os GRANT
# de um banco valem sem deixar linha. O banco revertido é conferido com ela também,
# porque os privilégios que a reversão revoga e a subida reconcede são justamente o
# que mais tende a quebrar, e conferir só o banco intocado não os exerce.
WRITE_PROBE := BEGIN; SET ROLE wager_app; \
	INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at) \
	VALUES ('00000000-0000-4000-8000-000000000001', '00000000-0000-4000-8000-000000000002', 'BRL', 0, 1, now(), now()); \
	ROLLBACK

.DEFAULT_GOAL := help
.PHONY: help up down cluster-up provision migrate test test-journey scenarios cover-journey mutation verify migrate-reversibility rules-test

help: ## lista os alvos
	@grep -hE '^[a-z][a-z-]*:.*## ' $(MAKEFILE_LIST) | sed -e 's/:.*## /|/' | awk -F'|' '{printf "%-24s %s\n", $$1, $$2}'

up: ## sobe a stack com REPLICAS réplicas do processo, três por padrão, e espera cada serviço ficar saudável
	@$(CHECK_REPLICAS)
	docker compose up -d --build --wait --scale wager=$(REPLICAS)

down: ## derruba a stack e descarta os volumes dela, voltando ao estado limpo
	docker compose down -v

# O `kubectl wait` espera uma condição só, e o Job termina em uma de duas. O
# prazo cobre a imagem já carregada no nó e o schema já aplicado pelo Compose.
MIGRATE_JOB_WAIT = for attempt in $$(seq 1 120); do \
		state=$$($(KUBECTL) -n $(NAMESPACE) get job migrate -o jsonpath='{.status.conditions[?(@.status=="True")].type}'); \
		case "$$state" in \
		*Failed*) echo "the migration Job $(NAMESPACE)/migrate failed; its log:" >&2; \
			$(KUBECTL) -n $(NAMESPACE) logs job/migrate >&2; exit 1;; \
		*Complete*) exit 0;; \
		esac; \
		sleep 1; \
	done; \
	echo "the migration Job $(NAMESPACE)/migrate did not finish within 120s" >&2; exit 1

# O cluster parte da mesma subida do Compose, que provisiona o broker e aplica o
# schema, e as réplicas do Compose param enquanto ele existe: com elas de pé,
# seriam processos a mais disputando a outbox, a fila e as esperas. O nó entra na
# rede do Compose e alcança cada serviço pelo nome.
cluster-up: ## sobe REPLICAS réplicas num cluster Kind sobre os serviços do Compose, em localhost:8091
	@$(CHECK_REPLICAS)
	docker compose up -d --build --wait
	docker compose stop wager
	@$(KIND) get clusters 2>/dev/null | grep -qx '$(CLUSTER)' || $(KIND) create cluster --config deploy/k8s/kind.yaml
	@docker network inspect junglegaming -f '{{range .Containers}}{{println .Name}}{{end}}' | grep -qx '$(CLUSTER)-control-plane' \
		|| docker network connect junglegaming $(CLUSTER)-control-plane
	@archive=$$(mktemp) && trap 'rm -f "$$archive"' EXIT \
		&& docker save --platform "$$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}')" -o "$$archive" $(CLUSTER_IMAGES) \
		&& $(KIND) load image-archive --name $(CLUSTER) "$$archive"
	$(KUBECTL) apply -f deploy/k8s/namespace.yaml
	@$(KUBECTL) -n $(NAMESPACE) create secret generic wager-credentials \
		--from-literal=DATABASE_URL='$(APP_URL)' \
		--from-literal=AWS_ACCESS_KEY_ID='$(AWS_ACCESS_KEY_ID)' \
		--from-literal=AWS_SECRET_ACCESS_KEY='$(AWS_SECRET_ACCESS_KEY)' \
		--from-literal=AWS_REGION='$(AWS_REGION)' \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(NAMESPACE) create configmap wager-migrations --from-file=deploy/migrations \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(NAMESPACE) create configmap wager-maps \
		--from-file=deploy/local/clients.yaml --from-file=deploy/local/queue-senders.yaml \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
	$(KUBECTL) -n $(NAMESPACE) delete job migrate --ignore-not-found --wait=true
	$(KUBECTL) apply -f deploy/k8s/migrate.yaml
	@$(MIGRATE_JOB_WAIT)

# O mesmo serviço que o `up` roda antes das réplicas, e o que o CI roda: o
# Terraform vem da imagem, na versão fixada no `compose.yaml`, e não do host.
provision: ## roda de novo o apply do broker local: filas, DLQ, tópico e remetente IAM
	docker compose run --rm provision

migrate: ## aplica o schema nos dois bancos, cada um nomeado no comando
	$(MIGRATE) "$(APP_URL)" up
	$(PSQL) -d $(APP_DB) -tAc "SELECT 1 FROM pg_database WHERE datname='$(SUITE_DB)'" | grep -q 1 \
		|| docker compose exec -T postgres createdb -U $(POSTGRES_USER) $(SUITE_DB)
	$(MIGRATE) "$(SUITE_URL)" up

# Os módulos de `scripts/` entram por nome: o `./...` do módulo raiz para na
# fronteira de módulo, e sem estas duas linhas os testes do verificador não
# rodam em lugar nenhum. O `-C` tem de ser o primeiro flag.
test: ## a suíte de unidade, que não sobe Docker
	go test -race -count=1 ./...
	go test -C scripts/envcheck -race -count=1 ./...
	go test -C scripts/testgates -race -count=1 ./...

test-journey: ## a suíte de jornada: em série, e contra o banco dela
	DATABASE_URL="$(SUITE_HOST_URL)" go test -race -count=1 -p 1 -tags=integration ./...

# Os cenários obrigatórios do enunciado, na ordem em que ele os lista, cada um no
# próprio `go test`: é o que dá a cada cenário o próprio veredito e a própria
# repetição, e o que deixa o alvo seguir depois de uma falha. Os parâmetros
# `SCENARIO_*` não são declarados aqui: o make exporta para a receita o que veio
# do ambiente ou da linha de comando, e o padrão de cada um mora no teste, num
# lugar só. `SCENARIO_REPEAT` é o `-count` do `go test`, e não um parâmetro que o
# teste lê.
SCENARIOS := \
	TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce \
	TestRacingBets_settleAgainstTheBalanceAlreadyCommitted \
	TestLockedWallet_doesNotHoldTheOthers \
	TestInterruption_changesNothingWhenTheRemovalNeverReachedTheBroker \
	TestPublishers_sendEachEventOnce \
	TestEarlyReversal_waitsAndThenResolvesOrExpires \
	TestRestart_keepsIdempotencyTheWaitAndTheLedger \
	TestChannels_settleTheSameOperationOnceOverHTTPAndTheQueue
SCENARIO_REPEAT ?= 1

# O log inteiro de cada cenário fica neste diretório, com o JSON de todas as
# instâncias. A tela mostra só o que o teste relatou — o que mediu e por que
# falhou — e o veredito; um `go test` que falha sem veredito, como um erro de
# compilação, mostra o fim do log no lugar.
#
# Um `go test` cujo `-run` não casa com teste nenhum, ou com `-count=0`, sai 0
# sem ter rodado nada; por isso o cenário só passa com o `--- PASS` do próprio
# nome no log. O `-timeout 0` tira o limite de 10 minutos do binário inteiro,
# que somaria todas as repetições: cada espera de um cenário já carrega o
# `SCENARIO_DEADLINE`, e cada parada de instância o próprio teto.
SCENARIO_LOGS := .quality/scenarios

scenarios: ## os oito cenários obrigatórios, um `go test` cada, contra a stack de pé
	@rm -rf $(SCENARIO_LOGS); mkdir -p $(SCENARIO_LOGS); failed=""; \
	for name in $(SCENARIOS); do \
		log="$(SCENARIO_LOGS)/$$name.log"; \
		echo "== $$name"; \
		DATABASE_URL="$(SUITE_HOST_URL)" go test -v -race -timeout 0 -count=$(SCENARIO_REPEAT) -tags=integration \
			-run "^$${name}\$$" ./internal/e2e/scenarios/ > "$$log" 2>&1; status=$$?; \
		grep -E '^=== RUN .*/|^ *--- (PASS|FAIL|SKIP)|^ +[a-z_]+_test\.go:[0-9]+: ' "$$log" || tail -n 20 "$$log"; \
		if [ $$status -ne 0 ]; then failed="$$failed $$name"; \
		elif ! grep -q "^--- PASS: $$name " "$$log"; then echo "    no run of $$name passed"; failed="$$failed $$name"; fi; \
	done; \
	if [ -n "$$failed" ]; then \
		echo "failed:"; for name in $$failed; do echo "  $$name ($(SCENARIO_LOGS)/$$name.log)"; done; \
		exit 1; \
	fi; \
	echo "every scenario passed; the full log of each is in $(SCENARIO_LOGS)/"

# O promtool vem da mesma imagem do Prometheus que o Compose sobe, então a
# versão que testa é a que avalia. O diretório inteiro é montado porque o teste
# nomeia o arquivo de regras por caminho relativo a ele.
rules-test: ## o teste de unidade das regras de alerta, com o promtool da imagem
	docker run --rm -v "$$PWD/deploy/prometheus:/rules:ro" --entrypoint promtool \
		prom/prometheus:v3.13.3-busybox test rules /rules/rules/settlement_test.yml

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

# As opções vão explícitas: os defaults de flag do verificador são os mesmos deste
# arquivo, e sem passá-los um `.env` que troca o banco ou a senha do administrador
# deixaria o verify vermelho sobre um ambiente exatamente como declarado.
verify: ## responde se o ambiente está no estado que os arquivos versionados declaram
	go run -C scripts/envcheck . -root "$$PWD" \
		-postgres-user "$(POSTGRES_USER)" -app-db "$(APP_DB)" -suite-db "$(SUITE_DB)" \
		-admin-user "$(KC_BOOTSTRAP_ADMIN_USERNAME)" -admin-password "$(KC_BOOTSTRAP_ADMIN_PASSWORD)" \
		-grafana-user "$(GF_SECURITY_ADMIN_USER)" -grafana-password "$(GF_SECURITY_ADMIN_PASSWORD)"

# Este alvo derruba o schema, e é por isso que o nome dele diz contra quem: o
# banco da suíte, nunca o da aplicação, e nunca como parte de `up`.
migrate-reversibility: ## sobe, reverte e sobe de novo o banco da suíte, e confere que nada ficou sujo
	$(MIGRATE) "$(SUITE_URL)" up
	$(MIGRATE) "$(SUITE_URL)" down -all
	$(MIGRATE) "$(SUITE_URL)" up
	$(PSQL) -d $(SUITE_DB) -tAc "SELECT dirty FROM schema_migrations" | grep -qx f
	$(PSQL) -d $(SUITE_DB) -v ON_ERROR_STOP=1 -c "$(WRITE_PROBE)"
	$(PSQL) -d $(APP_DB) -v ON_ERROR_STOP=1 -c "$(WRITE_PROBE)"
	@echo "reversibility ok: both databases are clean and write through wager_app"
