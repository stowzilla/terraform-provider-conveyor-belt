# Triggers Example: Using SNS and SQS triggers with conveyor-belt
#
# This example demonstrates event-driven Lambda functions using
# SNS topics and SQS queues as triggers.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    conveyor-belt = {
      source  = "stowzilla/conveyor-belt"
      version = "~> 0.22"
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

# SNS Topic for order events
resource "aws_sns_topic" "order_events" {
  name = "${var.app_name}-${var.environment}-order-events"
}

# SQS Queue for background jobs
resource "aws_sqs_queue" "background_jobs" {
  name                       = "${var.app_name}-${var.environment}-background-jobs"
  visibility_timeout_seconds = 300  # Should be >= Lambda timeout
  message_retention_seconds  = 86400
}

# DynamoDB table for orders
resource "aws_dynamodb_table" "orders" {
  name         = "${var.app_name}-${var.environment}-orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }
}

# Conveyor resource with trigger configuration
resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = var.app_name
  lambda_source_dir = "${path.module}/lambda"
  
  frontend_urls = ["https://app.example.com"]
  
  lambda_config = {
    # Shared configuration for all Lambdas
    shared = {
      env_vars = {
        LOG_LEVEL = "info"
      }
    }
    
    # API Lambda - publishes events to SNS
    api = {
      env_vars = {
        SNS_TOPIC_ARN = aws_sns_topic.order_events.arn
        SQS_QUEUE_URL = aws_sqs_queue.background_jobs.url
      }
      dynamodb_tables = [
        { name = aws_dynamodb_table.orders.name, access = "read_write" }
      ]
    }
    
    # Order processor - triggered by SNS topic
    # When a message is published to the topic, this Lambda is invoked
    order_processor = {
      timeout = 60
      env_vars = {
        ORDERS_TABLE = aws_dynamodb_table.orders.name
      }
      dynamodb_tables = [
        { name = aws_dynamodb_table.orders.name, access = "read_write" }
      ]
      # SNS trigger configuration
      sns_triggers = [
        { topic_arn = aws_sns_topic.order_events.arn }
      ]
    }
    
    # Background worker - triggered by SQS queue
    # Polls the queue and processes messages in batches
    background_worker = {
      timeout     = 300
      memory_size = 512
      env_vars = {
        ORDERS_TABLE = aws_dynamodb_table.orders.name
      }
      dynamodb_tables = [
        { name = aws_dynamodb_table.orders.name, access = "read_only" }
      ]
      # SQS trigger configuration
      sqs_triggers = [
        { 
          queue_arn  = aws_sqs_queue.background_jobs.arn
          batch_size = 10  # Process up to 10 messages per invocation
        }
      ]
    }
  }
}

# Outputs
output "api_url" {
  description = "API Gateway invoke URL"
  value       = conveyor_belt.main.gateway_urls["api"]
}

output "sns_topic_arn" {
  description = "SNS topic ARN for order events"
  value       = aws_sns_topic.order_events.arn
}

output "sqs_queue_url" {
  description = "SQS queue URL for background jobs"
  value       = aws_sqs_queue.background_jobs.url
}

output "lambda_arns" {
  description = "Lambda function ARNs"
  value       = conveyor_belt.main.lambda_functions
}
