# O endereço do LocalStack visto de quem roda o apply. O padrão é o do host; o
# serviço `provision` do Compose passa o nome do serviço na rede dele.
variable "localstack_endpoint" {
  type    = string
  default = "http://localhost:4566"
}
