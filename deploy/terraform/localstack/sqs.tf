resource "aws_sqs_queue" "wager_transactions_dlq" {
  name                        = "wager-transactions-dlq.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
}

resource "aws_sqs_queue" "wager_transactions" {
  name                        = "wager-transactions.fifo"
  fifo_queue                  = true
  content_based_deduplication = false
  receive_wait_time_seconds   = 20
  visibility_timeout_seconds  = 30

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.wager_transactions_dlq.arn
    maxReceiveCount     = 15
  })
}
