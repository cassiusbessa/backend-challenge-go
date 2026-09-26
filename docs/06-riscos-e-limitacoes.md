# Riscos e limitações

O que está declarado em vez de apresentado como coberto. Cada item diz o que falta, por que ainda falta e o que o fecharia.

## Não implementado

Tudo o que não está nesta tabela está implementado e coberto pela suíte de jornada.

| O quê | Estado | Nota |
| --- | --- | --- |
| Consumidor dos eventos | não existe | O tópico `wallet-events.fifo` é provisionado sem subscription, de propósito. A suíte de jornada anexa um assinante só pelo tempo do caso. |
| Métrica e painel do atraso da outbox | não existem | A linha morta fica visível no log e no banco, mas nenhum alarme a observa. |
| Observador contínuo da reconciliação, série e alerta de divergência | não existem | A rota e o caso de uso `reconcilewallet` já produzem o veredito; ninguém o pede periodicamente, e uma divergência só aparece no log de quem chamou a rota. |
| Guia de execução em múltiplas instâncias e simulação de falha | não existe | As instruções de subida e teste estão no `README.md`. |

## Lacunas de verificação

**A amarração das interfaces de comportamento não tem teste.** `problem` declara `retryable`, `defective` e `replayed` e o caso de uso as satisfaz por estrutura ([ADR 0008](adr/0008-interfaces-de-comportamento-no-consumidor.md)). Nenhum teste passa `submitwager.ErrOutcomeInFlight` ou `ErrRaceUnresolved` por `problem.From`; o teste de `problem` usa um fake local. Um rename em qualquer dos lados degrada `503` com `Retry-After` em `500` sem que nada fique vermelho. O que fecha: um teste em `problem_test.go` sobre os erros reais, e uma assertiva anônima no produtor.

**A forma do identificador de principal da nuvem nunca é exercitada localmente.** O broker local registra o identificador da conta; a AWS registra o do principal ([ADR 0019](adr/0019-mapa-de-remetentes-pela-identidade-observada.md)). O valor é opaco e só comparado por igualdade dentro do mapa, então o risco é baixo — mas um mapa com o valor errado manda toda mensagem legítima para a DLQ, e é a linha de log com a identidade observada que corrige.

**O piso de cobertura tem margem estreita.** 70% em `internal/platform` e 80% em `internal/app`. Os caminhos de I/O de `postgres`, `probe` e `telemetry` são cobertos pela suíte de integração, que não entra nesse cálculo.

## Guardas para o que não deveria acontecer

Três erros existem para um estado que o desenho torna inalcançável, e respondem `503` com `Retry-After` em vez de cair por baixo em silêncio. Se um deles aparecer na série de `retryable`, é algo a investigar, não a ignorar.

| Guarda | Estado impossível | Por que é impossível |
| --- | --- | --- |
| `storage.ErrLostWrite` | `UPDATE` sob `FOR UPDATE` afetando zero linhas | o lock serializa; a versão é a assertiva |
| `submitwager.ErrOutcomeInFlight` | linha lida em `PENDING` | o `CHECK` recusa gravar esse status |
| `submitwager.ErrRaceUnresolved` | vencedora do índice de chave ausente na releitura | um `INSERT` só recebe `23505` depois que a outra transação commitou, e nada apaga transações |

## Custos aceitos

- **`LOSS` com referência carrega a citada e a descarta.** O construtor não proíbe um `LOSS` de citar; `citedFor` busca a operação e `Loss` a ignora. Um `SELECT` gasto, sem efeito. Proibir exigiria um token novo para um problema que não existe em produção.
- **Identidades cunhadas e não usadas.** Quatro UUIDs por chegada — transação, lançamento, dois eventos — mesmo quando o desfecho usa dois. O preço de o domínio nunca chamar um minter.
- **Vinte linhas duplicadas entre dois runners de fundo** ([ADR 0011](adr/0011-tres-runners-de-fundo-separados.md)).

## Ambiente local

- O LocalStack community não persiste: qualquer reinício esvazia filas e tópico, e é preciso rodar `terraform apply` de novo. O IAM dele é parcial — o principal é criado, mas a política pode não ser aplicada como na AWS.
- O Compose sobe uma réplica do processo. As três instâncias que o desafio pede ficam para Kind ou k3d, com a migration como Job que roda uma vez antes das réplicas — decidido, sem manifesto nem guia ainda, como a tabela acima registra.
- O realm de teste não tem mapper de audience, e os clientes estão com `fullScopeAllowed`. A borda não confere `aud` ([ADR 0020](adr/0020-jwks-separado-do-issuer.md)).
- O `causationId` do envelope sai omitido em todo commit que nenhuma mensagem causou: a operação por HTTP tem só `correlationId`, e o commit diferido do worker de referência é disparado pelo prazo, não pela mensagem. A travessia entre os dois commits é o identificador da transação, que o primeiro evento carrega e o segundo usa como correlação.
