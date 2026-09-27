# 0036. O teste de carga como módulo próprio, com veredito e resolução pela chave

Status: aceita · 2026-09-26

## Contexto

As garantias do sistema — nenhum saldo negativo, nenhuma operação perdida ou duplicada, nenhum conflito de versão sob o lock — precisam valer com N processos disputando o mesmo banco sob carga, inclusive quando uma réplica morre no meio. Os números que interessam são latência, throughput, erro, conflito de versão, atraso da outbox e saturação do pool, sem meta de vazão. Uma chegada interrompida por uma réplica que morre tem desfecho desconhecido para o cliente: pode ter sido gravada ou não.

## Opções consideradas

1. `k6`, com o veredito e a contagem escritos em JavaScript.
2. `vegeta` para gerar a carga, e um verificador à parte.
3. Um módulo Go próprio em `scripts/`, só com a biblioteca padrão, que conta o erro de transporte como perda.
4. Um módulo Go próprio em `scripts/`, só com a biblioteca padrão, que resolve cada chegada sem resposta reenviando a mesma chave e termina num veredito por carteira.

## Decisão

Opção 4. O `k6` dá os percentis de graça, mas o veredito de consistência e a resolução pela própria chave teriam de ser escritos do mesmo tamanho em JavaScript, fora do toolchain do projeto. O `vegeta` é uma dependência para a parte fácil, e a difícil continuaria sendo o verificador. Contar o erro como perda faria a morte forçada de uma réplica reprovar sempre, e a idempotência é justamente o que torna o desfecho de uma chegada interrompida recuperável: reenviar a chave devolve o replay do que foi gravado, ou a primeira conclusão do que não foi. A carga passa a provar isso sob falha em vez de só mencioná-lo.

Módulo próprio porque o `Dockerfile` copia `cmd/` e `internal/` inteiros: o teste de carga lá dentro faria a imagem ser julgada velha a cada mudança dele.

## Consequências

- `make load` sobe o alvo e roda a carga: as réplicas do Compose, só com Docker, ou as do cluster com `TARGET=cluster`. `LOAD_KILL=graceful|forced` para ou mata uma réplica no meio da janela.
- A mistura é semeada: um quarto das chegadas em quatro carteiras quentes, `BET`, `WIN` e `LOSS`, e uma chegada em vinte repetindo chave e corpo de uma anterior. Cada worker mantém uma conexão, para as chegadas se espalharem pelas réplicas.
- O veredito confere cada carteira — saldo, versão e lançamentos contra a contagem da carga, e a reconciliação consistente — e falha também com conflito de versão acima de zero, com réplica pedida que não decidiu chegada, e com a outbox que não drena no prazo.
- Os números do servidor vêm do backend de métrica, e a subida de um contador é o maior valor na janela menos o valor no início, com a série ausente no início valendo zero: `increase()` sobre um filho de vetor que nasce em 1 dentro da janela lê 0, e o primeiro conflito de versão passaria calado. Backend fora ou série ausente falha a execução.
- **O que se paga:** uma execução padrão leva minutos, porque espera a outbox drenar; os percentis são do cliente, com a rede do host no meio; e o módulo precisa ser nomeado no lint e nos testes do CI, porque o `./...` da raiz não o alcança.

## Onde está no código

- `scripts/loadtest/load.go` — a abertura das carteiras, os workers e `classify`.
- `scripts/loadtest/resolve.go` — `resolveAll`, a resolução pela chave.
- `scripts/loadtest/verdict.go` — `tallies` e `checkWallet`.
- `scripts/loadtest/backend.go` — `rise`, `figures` e `drain`.
- `scripts/loadtest/kill.go` — `killHalfway` e os comandos de cada alvo.
- `Makefile` — o alvo `load`.
