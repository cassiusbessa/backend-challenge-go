-- A tabela nova sai, e com ela os gatilhos, a função e o privilégio que só
-- existem por causa dela. Nenhuma tabela financeira é tocada, aqui nem na
-- subida.
--
-- As linhas de inbox gravadas vão junto, e com elas a memória de qual mensagem
-- já foi concluída. Uma mensagem que voltar à fila depois disso é reaplicada, e
-- o que a segura é a idempotência, que deduz a operação e permanece.

DROP TABLE IF EXISTS inbox_messages;

DROP FUNCTION IF EXISTS inbox_messages_refuse_change();
