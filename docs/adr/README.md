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
