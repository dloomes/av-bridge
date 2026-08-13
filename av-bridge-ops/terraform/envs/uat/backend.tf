terraform {
  backend "s3" {
    bucket         = "avrmm-tf-state-uat"
    key            = "envs/uat/main.tfstate"
    region         = "eu-west-2"
    dynamodb_table = "avrmm-tf-lock-uat"
    encrypt        = true
    profile        = "avrmm-uat"
  }
}
