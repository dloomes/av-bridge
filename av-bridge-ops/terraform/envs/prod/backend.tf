terraform {
  backend "s3" {
    bucket         = "avrmm-tf-state-prod"
    key            = "envs/prod/main.tfstate"
    region         = "eu-west-2"
    dynamodb_table = "avrmm-tf-lock-prod"
    encrypt        = true
    profile        = "avrmm-prod"
  }
}
