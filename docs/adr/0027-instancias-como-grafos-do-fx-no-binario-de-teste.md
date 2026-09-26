# 0027. Instâncias do processo como grafos do Fx no binário de teste

Status: aceita · 2026-09-26

## Contexto

O enunciado exige provar os cenários de concorrência com três ou mais instâncias independentes, e trata "dependência de instância única" como falha eliminatória: idempotência em memória, lock no processo, decisão que só vale porque a mesma memória viu as duas chegadas. A suíte de jornada sobe o processo dentro do binário de teste, com `app.New` e `Start`, e o máximo que um caso sobe hoje são duas cópias. Os cenários precisam de uma frota de N instâncias sobre o mesmo banco e o mesmo broker, de parar uma delas no instante que o caso escolhe, e de parar todas e subir outras sobre o que as primeiras gravaram.

## Opções consideradas

1. Executar o binário construído N vezes, como processos do sistema operacional.
2. Subir N réplicas do serviço no Compose.
3. Montar N grafos do Fx independentes no binário de teste, cada um com `app.New`: o próprio pool, a própria porta efêmera, o próprio registry de métricas, o próprio pipeline de telemetria e os próprios quatro componentes de fundo.

## Decisão

Opção 3. O que um processo separado acrescentaria é a memória isolada pelo kernel, e nenhuma garantia do sistema passa pela memória: o lock da linha da carteira, os índices únicos da chave e do identificador externo, a inbox por consumidor e mensagem e o lease da outbox moram no PostgreSQL. Isso foi conferido no código antes da escolha: cada `app.New` monta o próprio registry, o próprio pipeline e o próprio pool, nenhum pacote chama `otel.Set*` nem `slog.SetDefault`, e o único `sync.Once` vive dentro de structs de instância.

A opção 1 pagaria por esse isolamento construindo o binário no `TestMain`, reservando portas, capturando o log de N processos e perdendo o acesso direto ao grafo que os outros casos usam — para provar a mesma coisa, porque o banco decide igual para dois processos e para dois grafos. A opção 2 depende das portas fixas do `compose.yaml`, amarra o teste a uma stack com N cópias do serviço, e não consegue parar uma réplica no instante entre o commit e a remoção da mensagem que o caso da interrupção precisa.

## Consequências

- Todo cenário roda, por padrão, sobre três instâncias cujo único estado comum é o banco e o broker, e as chegadas de um cenário são repartidas entre elas por índice. Reiniciar é parar a frota inteira e subir outra sobre o mesmo banco, sem nada herdado em memória.
- Um estado de pacote de que um desfecho dependa passa a ser proibido: no binário de teste ele seria compartilhado pelas instâncias, e o cenário passaria onde processos separados falhariam.
- **O que se paga:** as instâncias dividem o runtime do Go — o escalonador, o coletor, o `http.DefaultTransport`. Um defeito que só aparecesse com processos isolados pelo kernel não aparece aqui. Réplicas como processos separados, em orquestrador, ficam para a operação e para o teste de carga ([06 · Riscos](../06-riscos-e-limitacoes.md)).

## Onde está no código

- `internal/e2e/scenarios/harness_test.go` — `scene.launch` e `scene.boot`, que montam cada instância com `app.New`; `fleet.at`, a repartição por índice; `instance.stop` e `fleet.stop`, a morte de uma e o reinício de todas.
- `internal/platform/app/app.go` — `New`, o grafo que cada instância monta por inteiro.
