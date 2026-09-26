# 0021. A telemetria é iniciada no construtor dela, antes de todo hook do Fx

Status: aceita · 2026-09-26

## Contexto

O Fx impõe duas ordens que não se negociam: **todo construtor roda antes de todo hook**, e um `fx.Invoke` força a construção do grafo inteiro enquanto a aplicação é montada. Enquanto o start do pipeline de telemetria foi um hook `OnStart`, o relay da outbox, o consumidor da fila e o worker de referência recebiam na construção um `Tracer` e um `Logger` que o processo **substituía logo depois**, e que ninguém relia. O span e o log dos três nasciam num provider sem exportador e morriam dentro do processo.

O SDK fecha as saídas fáceis: as opções do `TracerProvider` são fixadas na construção, então instalar o exportador é **trocar** o provider, não configurá-lo — e o campo que os consumidores copiaram envelhece. A borda HTTP escapava porque as rotas são montadas dentro do hook do servidor, que corre depois; era assimetria, não decisão.

O defeito foi medido antes de ser desenhado: um teste que compara o que o grafo entregou com o que o start instalou falhava nos dois campos. Uma entrega que envelhece é indistinguível de um componente calado — o único sinal dela é a ausência do que deveria ter chegado.

## Opções consideradas

1. Adiar os três consumidores para dentro de hooks, como as rotas já fazem.
2. O `Boot` sobe o pipeline antes do grafo e o injeta pronto.
3. Entregar uma indireção estável: um `Tracer` e um `Handler` que delegam ao provider corrente, trocado atomicamente no start.
4. Passar o pipeline inteiro aos componentes, em vez do `Tracer` e do `Logger`.
5. Um construtor que devolve o pipeline **já iniciado**, e nenhum hook de start.

## Decisão

Opção 5. A regra do Fx que causa o defeito — todo construtor antes de todo hook — é a mesma que o corrige quando o start muda de lado: um provider roda antes de tudo que depende dele, então não existe mais instante em que um componente receba telemetria anterior ao start. A garantia passa a ser do framework, não da disciplina de quem escreve a fiação.

A opção 1 é a correção mínima e resolve os sintomas conhecidos, mas não fecha a classe: o próximo componente que pedir `Tracer` num construtor volta a pegar o antigo, em silêncio. O padrão de adiar já existia no arquivo, aplicado num lugar só — é a prova de que "lembre-se de adiar" não se sustenta. A opção 2 foi a primeira escolha, e a medição a derrubou: sem o pipeline no grafo, dois testes de configuração inválida param com *missing type* em vez do erro que afirmam, porque chegam sem passar pelo `Boot`; e tirar o hook sem suprir nada deixa as harnesses da suíte de jornada rodando o processo com o pipeline não iniciado, trocando um defeito silencioso por outro. A opção 3 acrescenta dois tipos, um ponto de sincronização e um salto por span para preservar uma ordem que não precisa existir, e não remove a janela — só a torna inofensiva, o que é mais difícil de verificar. A opção 4 espalharia um tipo de plataforma por código que pede só um `Tracer`, trocando um defeito de ordem por um acoplamento.

O construtor mora no pacote que possui o pipeline, porque o contrato "sai pronto" é dele; à fiação fica só a razão de ser provider e não hook. Uma consequência é que a correção alcança os três pontos de entrada de uma vez — o `Boot`, as harnesses da suíte de jornada e os testes do composition root —, porque todos montam o processo pelo mesmo construtor do grafo.

Com isso a telemetria fica fora do ciclo de vida nas duas pontas: abre antes de tudo, e descarrega depois de tudo, com o orçamento próprio de [ADR 0012](0012-flush-de-telemetria-fora-do-lifecycle.md).

## Consequências

- Nenhum componente pode receber `Tracer` ou `Logger` que o processo ainda vá substituir, e o start do pipeline não volta a ser registrado como hook.
- Um construtor passa a fazer trabalho de subida: uma falha de telemetria aborta a construção do grafo em vez do start. As duas encerram a subida com erro, e abortar antes é mais cedo.
- **O que se paga:** o start perde o contexto do Fx, que carregava o prazo de subida. O construtor deriva um prazo próprio do `SHUTDOWN_TIMEOUT`, porque o grafo é montado antes de existir contexto de execução, e um exportador que travasse ali travaria contra orçamento nenhum.
- A telemetria fica de pé mesmo quando a subida falha — e é a subida que falha que tem algo a relatar, então o caminho de erro também descarrega o buffer.
- O teste de regressão é sobre **identidade**, não sobre semântica: uma implementação que entregasse um provider novo e igualmente sem exportador passaria. É o limite do que o SDK expõe, e ainda assim é a única afirmação que distingue o estado defeituoso do correto sem alcançar campo privado de outro pacote.

## Onde está no código

- `internal/platform/telemetry/otel.go` — `Started`, o construtor que devolve o pipeline iniciado; `Exporting`, que responde se os exportadores foram instalados.
- `internal/platform/app/app.go` — `newPipeline` e o prazo derivado; `register`, que já não tem hook de start para a telemetria.
- `internal/platform/app/app_test.go` — `TestNew_handsTheBackgroundWorkTheTelemetryThatStartInstalls`.
