resource "aws_iam_user" "wager_sender" {
  name = "wager-sender"
}

resource "aws_iam_user_policy" "wager_sender_send" {
  name = "wager-sender-send"
  user = aws_iam_user.wager_sender.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["sqs:SendMessage"]
        Resource = aws_sqs_queue.wager_transactions.arn
      }
    ]
  })
}

resource "aws_iam_access_key" "wager_sender" {
  user = aws_iam_user.wager_sender.name
}

resource "local_sensitive_file" "wager_sender_key" {
  filename        = "${path.module}/wager-sender.keys"
  file_permission = "0600"
  content         = "aws_access_key_id=${aws_iam_access_key.wager_sender.id}\naws_secret_access_key=${aws_iam_access_key.wager_sender.secret}\n"
}
