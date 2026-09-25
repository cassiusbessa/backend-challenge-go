-- A tabela nova sai, e com ela o índice, o gatilho e o privilégio que só
-- existem por causa dela. Nenhuma das três tabelas financeiras é tocada, aqui
-- nem na subida.
--
-- Os eventos ainda não publicados vão junto. Nenhum consumidor existe hoje, e é
-- por isso que o custo é zero; quando houver, a reversão passa a exigir drenar
-- a fila antes.

DROP TABLE IF EXISTS outbox_events;

DROP FUNCTION IF EXISTS outbox_events_refuse_payload_change();
