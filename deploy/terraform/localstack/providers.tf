provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    iam = var.localstack_endpoint
    sns = var.localstack_endpoint
    sqs = var.localstack_endpoint
    sts = var.localstack_endpoint
  }
}
