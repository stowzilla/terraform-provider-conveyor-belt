# Advanced Example: Using conveyor-belt resources with Cognito authentication
#
# This example demonstrates how to create Lambda functions and an API Gateway
# with Cognito authentication using the granular Conveyor resources.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    conveyor-belt = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.17"
    }
  }
}

# Variables
variable "environment" {
  description = "Environment name"
  type        = string
  default     = "dev"
}

variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "app_name" {
  description = "Application name"
  type        = string
  default     = "myapp"
}

variable "frontend_url" {
  description = "Frontend application URL"
  type        = string
  default     = "https://app.example.com"
}

# AWS Provider
provider "aws" {
  region = var.aws_region
}

# Conveyor Provider
provider "conveyor-belt" {
  environment            = var.environment
  aws_region             = var.aws_region
  default_lambda_timeout = 30
  default_lambda_memory  = 256
  
  default_tags = {
    Application = var.app_name
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}

# Cognito User Pool
resource "aws_cognito_user_pool" "main" {
  name = "${var.app_name}-${var.environment}"
  
  password_policy {
    minimum_length    = 8
    require_lowercase = true
    require_numbers   = true
    require_symbols   = false
    require_uppercase = true
  }
  
  auto_verified_attributes = ["email"]
}

resource "aws_cognito_user_pool_client" "main" {
  name         = "${var.app_name}-${var.environment}-client"
  user_pool_id = aws_cognito_user_pool.main.id
  
  explicit_auth_flows = [
    "ALLOW_USER_PASSWORD_AUTH",
    "ALLOW_REFRESH_TOKEN_AUTH"
  ]
}

# Lambda Functions
resource "conveyor_belt_lambda" "onboarding" {
  name       = "onboarding"
  app_name   = var.app_name
  source_dir = "${path.module}/lambda"
  
  env_vars = {
    COGNITO_USER_POOL_ID = aws_cognito_user_pool.main.id
    COGNITO_CLIENT_ID    = aws_cognito_user_pool_client.main.id
  }
  
  tables = ["customers"]
}

resource "conveyor_belt_lambda" "customer" {
  name       = "customer"
  app_name   = var.app_name
  source_dir = "${path.module}/lambda"
  
  env_vars = {
    LOG_LEVEL = "info"
  }
  
  tables = ["customers", "orders"]
}

resource "conveyor_belt_lambda" "admin" {
  name       = "admin"
  app_name   = var.app_name
  source_dir = "${path.module}/lambda"
  
  timeout = 60
  memory  = 512
  
  env_vars = {
    ADMIN_MODE = "true"
  }
  
  tables = ["customers", "orders", "inventory"]
}

# Public API Gateway (no auth)
resource "conveyor_belt_gateway" "public" {
  name     = "public"
  app_name = var.app_name
  
  frontend_urls = [var.frontend_url]
  
  routes = [
    {
      name       = "health"
      verb       = "GET"
      path       = "/health"
      lambda_arn = conveyor_belt_lambda.onboarding.arn
      auth       = "none"
    },
    {
      name       = "signup"
      verb       = "POST"
      path       = "/signup"
      lambda_arn = conveyor_belt_lambda.onboarding.arn
      auth       = "none"
    },
    {
      name       = "login"
      verb       = "POST"
      path       = "/login"
      lambda_arn = conveyor_belt_lambda.onboarding.arn
      auth       = "none"
    }
  ]
}

# Private API Gateway (Cognito auth)
resource "conveyor_belt_gateway" "private" {
  name     = "private"
  app_name = var.app_name
  
  frontend_urls          = [var.frontend_url]
  cognito_user_pool_arns = [aws_cognito_user_pool.main.arn]
  
  routes = [
    {
      name       = "get_profile"
      verb       = "GET"
      path       = "/profile"
      lambda_arn = conveyor_belt_lambda.customer.arn
      auth       = "cognito"
    },
    {
      name       = "update_profile"
      verb       = "PUT"
      path       = "/profile"
      lambda_arn = conveyor_belt_lambda.customer.arn
      auth       = "cognito"
    },
    {
      name       = "list_orders"
      verb       = "GET"
      path       = "/orders"
      lambda_arn = conveyor_belt_lambda.customer.arn
      auth       = "cognito"
    }
  ]
}

# Admin API Gateway (Cognito auth)
resource "conveyor_belt_gateway" "admin" {
  name     = "admin"
  app_name = var.app_name
  
  frontend_urls          = [var.frontend_url]
  cognito_user_pool_arns = [aws_cognito_user_pool.main.arn]
  
  routes = [
    {
      name       = "list_customers"
      verb       = "GET"
      path       = "/customers"
      lambda_arn = conveyor_belt_lambda.admin.arn
      auth       = "cognito"
    },
    {
      name       = "get_customer"
      verb       = "GET"
      path       = "/customers/{id}"
      lambda_arn = conveyor_belt_lambda.admin.arn
      auth       = "cognito"
    },
    {
      name       = "list_inventory"
      verb       = "GET"
      path       = "/inventory"
      lambda_arn = conveyor_belt_lambda.admin.arn
      auth       = "cognito"
    }
  ]
}

# Outputs
output "cognito_user_pool_id" {
  description = "Cognito User Pool ID"
  value       = aws_cognito_user_pool.main.id
}

output "cognito_client_id" {
  description = "Cognito Client ID"
  value       = aws_cognito_user_pool_client.main.id
}

output "public_api_url" {
  description = "Public API Gateway URL"
  value       = conveyor_belt_gateway.public.invoke_url
}

output "private_api_url" {
  description = "Private API Gateway URL (requires Cognito token)"
  value       = conveyor_belt_gateway.private.invoke_url
}

output "admin_api_url" {
  description = "Admin API Gateway URL (requires Cognito token)"
  value       = conveyor_belt_gateway.admin.invoke_url
}

output "lambda_arns" {
  description = "Lambda function ARNs"
  value = {
    onboarding = conveyor_belt_lambda.onboarding.arn
    customer   = conveyor_belt_lambda.customer.arn
    admin      = conveyor_belt_lambda.admin.arn
  }
}
