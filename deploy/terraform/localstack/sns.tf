resource "aws_sns_topic" "wallet_events" {
  name                        = "wallet-events.fifo"
  fifo_topic                  = true
  content_based_deduplication = false
}
