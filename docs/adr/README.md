# Decisões de arquitetura

Um arquivo por decisão, no formato [MADR](https://adr.github.io/madr/) mínimo — contexto, opções consideradas, decisão, consequências, e onde está no código. Uma decisão registrada **não é editada**: quando o mundo muda, uma nova a substitui e a antiga ganha o status `substituída`, com o link nos dois sentidos. É isso que faz esta pasta guardar história em vez de só o estado atual.

O que entra aqui é a decisão que teve **alternativa**: o que foi considerado, o que foi escolhido e o que se paga por isso. A estrutura do sistema está em [`docs/`](../); a divisão inteira está em [`ARCHITECTURE.md`](../../ARCHITECTURE.md).

As vinte primeiras foram registradas em 2026-09-26, extraídas do que o `ARCHITECTURE.md` e os comentários do código já diziam em prosa. A data de cada uma é a do registro, não a da escolha. Da 0021 em diante, cada uma nasce no mesmo recorte do código que explica.

## Índice

| # | Decisão | Em uma linha |
| --- | --- | --- |
| [0001](0001-indice-unico-como-arbitro-da-idempotencia.md) | Índice único como árbitro da idempotência | A consulta prévia tem janela; o índice não. A perdedora relê a vencedora depois do rollback. |
| [0002](0002-lock-pessimista-com-guarda-de-versao.md) | Lock pessimista com guarda de versão | `FOR UPDATE` serializa a decisão; a versão no `UPDATE` prova que nada furou o lock. |
| [0003](0003-already-reversed-decidido-sob-o-lock.md) | `ALREADY_REVERSED` decidido sob o lock | A consulta roda depois do lock e decide sozinha; o índice parcial fica como invariante. |
| [0004](0004-rejeicao-duravel-ao-lado-do-resultado.md) | Rejeição durável ao lado do resultado | A função de trabalho devolve `nil` e commita a linha `REJECTED`; a recusa vira erro depois. |
| [0005](0005-moeda-denormalizada-na-transacao-e-no-lancamento.md) | Moeda gravada na transação e no lançamento | O lançamento é autocontido e a FK composta prova que ele concorda com a transação. |
| [0006](0006-202-para-pending-reference.md) | `202` para a espera por referência | O código descreve o request: criou e decidiu, criou e não decidiu, encontrou. |
| [0007](0007-catalogo-fechado-de-failure-code.md) | Catálogo fechado de `failureCode` | Enumerável para o mapa de status e as séries; a colisão de carteira fica fora, em `409`. |
| [0008](0008-interfaces-de-comportamento-no-consumidor.md) | Interfaces de comportamento no consumidor | `retryable`, `defective` e `replayed` são declaradas em `problem`; o caso de uso as satisfaz sem saber. |
| [0009](0009-raw-message-na-referencia-opcional.md) | `json.RawMessage` na referência opcional | Ausente, `null` e inválido são três estados; um typo não pode virar "não cita nada". |
| [0010](0010-stack-capturada-uma-vez.md) | Stack capturada uma vez | Só em falha de infraestrutura, na fronteira onde ela é vista primeiro. |
| [0011](0011-tres-runners-de-fundo-separados.md) | Três runners de fundo separados | Os turnos têm formas diferentes; um runner comum exporia o lease para quem não o tem. |
| [0012](0012-flush-de-telemetria-fora-do-lifecycle.md) | Flush de telemetria fora do lifecycle | O Fx pula o hook que não alcança; o flush tem orçamento próprio depois de tudo. |
| [0013](0013-publicar-fora-da-transacao-sob-lease.md) | Publicar fora da transação SQL, sob lease | Reivindica, publica sem transação, confirma pelo token. Entrega ao menos uma vez. |
| [0014](0014-tempo-do-banco-na-outbox.md) | Tempo do banco na outbox | Lease pelo `now()` do banco; ordem por `publish_seq`, atribuído sob o lock. |
| [0015](0015-duas-contagens-na-outbox.md) | Duas contagens na outbox | Tentativa move o backoff; recusa permanente decide a morte. Juntas matariam linha sadia. |
| [0016](0016-inbox-em-savepoint.md) | Inbox em savepoint | No commit da operação, para memória da mensagem e saldo viverem ou morrerem juntos. |
| [0017](0017-invisibilidade-cobre-long-poll-e-processamento.md) | Invisibilidade cobre long poll e processamento | Invariante verificada na subida; violá-la consome entregas em silêncio, medido. |
| [0018](0018-desistencia-na-quinta-entrega-redrive-em-quinze.md) | Desistência na quinta entrega, redrive em quinze | O consumidor decide; o broker é rede de segurança e não pode descartar antes. |
| [0019](0019-mapa-de-remetentes-pela-identidade-observada.md) | Mapa de remetentes pela identidade observada | O nome do principal IAM não chega ao consumidor; o valor é opaco e comparado por igualdade. |
| [0020](0020-jwks-separado-do-issuer.md) | JWKS separado do issuer | Sem descoberta OIDC na subida, para o IdP não entrar no caminho de boot. |
| [0021](0021-telemetria-iniciada-no-construtor-antes-dos-hooks.md) | Telemetria iniciada no construtor dela | Todo construtor roda antes de todo hook; um start em hook entregava provider que ele substituía. |
| [0022](0022-reconciliacao-em-uma-sentenca-com-veredito-no-caso-de-uso.md) | Reconciliação em uma sentença, com o veredito no caso de uso | Uma `SELECT` é o snapshot e não espera lock; o SQL devolve números e `reconcilewallet` nomeia o desvio. |
| [0023](0023-cursor-opaco-amarrado-a-carteira.md) | Cursor opaco amarrado à carteira | O cursor de A apresentado em B é recusado como malformado; assinar não compra nada, posição nua responde errado em silêncio. |
| [0024](0024-observador-de-divergencia-por-cursor-em-memoria.md) | Observador de divergência por cursor em memória | Varre toda carteira, porque a divergência que importa não move `updated_at`; a série é contador, com o de carteiras conferidas ao lado. A falha do veredito foi substituída pela 0026. |
| [0025](0025-alertas-como-regras-do-prometheus-testadas.md) | Alertas como regras do Prometheus, testadas | O `promtool` da imagem do Compose testa as duas regras no CI; sem Alertmanager, porque não há para onde notificar. |
| [0026](0026-veredito-que-falha-avanca-o-cursor-do-observador.md) | Um veredito que falha avança o cursor do observador | A página que falha deixa o cursor; o veredito que falha o põe na carteira, que volta na passagem seguinte sem travar as depois dela. |
| [0027](0027-instancias-como-grafos-do-fx-no-binario-de-teste.md) | Instâncias do processo como grafos do Fx no binário de teste | Nenhuma garantia passa pela memória, então N grafos independentes provam o que N processos provariam, sem construir o binário nem perder o instante de parar um. |
| [0028](0028-falha-e-contagem-na-fronteira-de-rede-do-broker.md) | Falha e contagem na fronteira de rede do broker | Um proxy do teste recusa a remoção e conta cada publicação no fio, abaixo do SDK e antes da deduplicação do tópico. |
| [0029](0029-contrato-http-segue-o-enunciado.md) | O contrato HTTP segue o enunciado | Nomes, `POST` na reconciliação e a consulta por identificador externo; a rejeição fica em problem details com `transactionId`, a única divergência. |
| [0030](0030-causation-id-e-a-mensagem-que-causou-o-commit.md) | O `causationId` é a mensagem que causou o commit | A transação já está em `data`; a mensagem é o único elo do evento com a entrega, e o `transactionId-optional` do exemplo é ilustrativo. |
| [0031](0031-diferenca-fora-do-int64-falha-a-reconciliacao.md) | A diferença que não cabe em `int64` falha a reconciliação | Como a soma que não cabe; manter o veredito sem a diferença cobriria só um dos dois estados, e o alerta de falhas fica para o degrau de operação. |
| [0032](0032-apply-do-broker-como-servico-do-compose.md) | O apply do broker como serviço do Compose | O processo espera o apply; scripts do LocalStack descreveriam as filas duas vezes, e o Terraform sai do host. |
| [0033](0033-replicas-do-compose-atras-de-um-haproxy.md) | As réplicas do Compose atrás de um HAProxy | Três réplicas em `localhost:8090`, com sonda de readiness e reenvio só da conexão recusada; o Kind segue como caminho de operação. |
| [0034](0034-cluster-kind-sobre-os-servicos-do-compose.md) | O cluster Kind sobre os serviços do Compose | O nó entra na rede do Compose; a migration é um Job ordenado pelo `make`, e o número de réplicas é parâmetro do comando, não do manifesto. |
| [0035](0035-series-das-replicas-por-agente-com-escrita-remota.md) | As séries das réplicas do cluster por um agente com escrita remota | O IP de pod não sai do nó; um Prometheus agente raspa por pod e escreve no do Compose, com os exemplares, e o painel não muda. |
| [0036](0036-teste-de-carga-com-veredito-e-resolucao-pela-chave.md) | O teste de carga como módulo próprio, com veredito e resolução pela chave | A chegada sem resposta se resolve reenviando a chave; o veredito é por carteira, e a subida dos contadores não usa `increase()`. |
| [0037](0037-serie-e-alerta-do-veredito-que-falha.md) | A série e o alerta da reconciliação que não produziu veredito | A opção que o 0031 deixou para a operação: uma série por origem, e o terceiro alerta só sobre o observador. |
| [0038](0038-relay-paralelo-e-encadeado-por-carteira.md) | O relay envia por várias carteiras ao mesmo tempo e encadeia cada uma | Sob carga a varredura por evento consumia o banco; agora sai um por carteira ao mesmo tempo, e o próximo de cada carteira sem nova varredura. |
| [0039](0039-series-do-trafego-nascem-em-zero.md) | As séries do tráfego nascem em zero | O que o 0024 fez para a reconciliação vale para todo contador de negócio e para a latência HTTP: tabelas fechadas no construtor, e não a ingestão experimental do zero pelo Prometheus. |
| [0040](0040-spans-do-caso-de-uso-e-da-unit-of-work-pelo-span-pai.md) | Os spans do caso de uso e da unit of work nascem do span pai | O span filho sai do provider do span que o contexto carrega: nenhum tracer injetado, e turno de fundo sem entrada não abre trace. |
