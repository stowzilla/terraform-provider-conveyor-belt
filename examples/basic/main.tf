# Basic Example: Using conveyor_belt_lambda and conveyor_belt_gateway resources
#
# This example demonstrates how to create Lambda functions and an API Gateway
# using the granular Conveyor resources.

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

# AWS Provider
provider "aws" {
  region = var.aws_region
}

# Conveyor Provider with defaults
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

# Lambda Functions
resource "conveyor_belt_lambda" "customer" {
  name       = "customer"
  app_name   = var.app_name
  source_dir = "${path.module}/lambda"
  
  env_vars = {
    LOG_LEVEL = "info"
  }
  
  tables = ["customers"]
}

resource "conveyor_belt_lambda" "orders" {
  name       = "orders"
  app_name   = var.app_name
  source_dir = "${path.module}/lambda"
  
  # Override provider defaults
  timeout = 60
  memory  = 512
  
  env_vars = {
    LOG_LEVEL = "debug"
  }
  
  tables = ["orders", "customers"]
}

# API Gateway
resource "conveyor_belt_gateway" "api" {
  name     = "api"
  app_name = var.app_name
  
  frontend_urls = [
    "https://app.example.com"
  ]
  
  routes = [
    {
      name       = "get_customer"
      verb       = "GET"
      path       = "/customer"
      lambda_arn = conveyor_belt_lambda.customer.arn
      auth       = "none"
    },
    {
      name       = "create_order"
      verb       = "POST"
      path       = "/orders"
      lambda_arn = conveyor_belt_lambda.orders.arn
      auth       = "none"
    },
    {
      name       = "list_orders"
      verb       = "GET"
      path       = "/orders"
      lambda_arn = conveyor_belt_lambda.orders.arn
      auth       = "none"
    }
  ]
}

# Outputs
output "api_url" {
  description = "API Gateway invoke URL"
  value       = conveyor_belt_gateway.api.invoke_url
}

output "lambda_arns" {
  description = "Lambda function ARNs"
  value = {
    customer = conveyor_belt_lambda.customer.arn
    orders   = conveyor_belt_lambda.orders.arn
  }
}
