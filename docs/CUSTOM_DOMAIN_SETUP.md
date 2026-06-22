# Custom Domain Setup Guide

This guide provides complete Terraform code for setting up an API Gateway custom domain to use with the Conveyor provider.

## Overview

Setting up a custom domain for unified API access requires:

1. ACM certificate for your domain (must be in us-east-1 for API Gateway)
2. DNS validation records for the certificate
3. API Gateway custom domain name resource
4. Route 53 alias record pointing to the custom domain

Once created, you can pass the domain name to Conveyor's `custom_domain_name` attribute.

## Prerequisites

- Route 53 hosted zone for your domain
- Terraform AWS provider configured
- Appropriate IAM permissions for ACM, API Gateway, and Route 53

## Terraform Code

Create a new file (e.g., `api_domain.tf`) in your infrastructure directory:

### Variables

```hcl
variable "domain_name" {
  description = "Base domain name (e.g., 'example.com')"
  type        = string
}

variable "api_subdomain" {
  description = "Subdomain for API (e.g., 'api' for api.example.com)"
  type        = string
  default     = "api"
}
```

### ACM Certificate

```hcl
# ACM Certificate for api.example.com
# Note: Must be in us-east-1 for API Gateway custom domains
resource "aws_acm_certificate" "api" {
  provider          = aws.us_east_1  # Use us-east-1 provider alias
  domain_name       = "${var.api_subdomain}.${var.domain_name}"
  validation_method = "DNS"

  tags = {
    Name      = "${var.api_subdomain}.${var.domain_name}"
    ManagedBy = "Terraform"
  }

  lifecycle {
    create_before_destroy = true
  }
}

# DNS validation records
resource "aws_route53_record" "api_cert_validation" {
  for_each = {
    for dvo in aws_acm_certificate.api.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      record = dvo.resource_record_value
      type   = dvo.resource_record_type
    }
  }

  zone_id = data.aws_route53_zone.main.zone_id
  name    = each.value.name
  type    = each.value.type
  ttl     = 300
  records = [each.value.record]
}

# Certificate validation (waits for DNS propagation)
resource "aws_acm_certificate_validation" "api" {
  provider                = aws.us_east_1
  certificate_arn         = aws_acm_certificate.api.arn
  validation_record_fqdns = [for record in aws_route53_record.api_cert_validation : record.fqdn]
}
```

### API Gateway Custom Domain

```hcl
# API Gateway Custom Domain
resource "aws_api_gateway_domain_name" "api" {
  domain_name              = "${var.api_subdomain}.${var.domain_name}"
  regional_certificate_arn = aws_acm_certificate_validation.api.certificate_arn

  endpoint_configuration {
    types = ["REGIONAL"]
  }

  tags = {
    Name      = "${var.api_subdomain}.${var.domain_name}"
    ManagedBy = "Terraform"
  }
}

# Route 53 alias record pointing to the custom domain
resource "aws_route53_record" "api" {
  zone_id = data.aws_route53_zone.main.zone_id
  name    = "${var.api_subdomain}.${var.domain_name}"
  type    = "A"

  alias {
    name                   = aws_api_gateway_domain_name.api.regional_domain_name
    zone_id                = aws_api_gateway_domain_name.api.regional_zone_id
    evaluate_target_health = false
  }
}
```

### Data Sources

```hcl
# Look up existing Route 53 hosted zone
data "aws_route53_zone" "main" {
  name         = var.domain_name
  private_zone = false
}
```

### Outputs

```hcl
output "api_domain_name" {
  value       = aws_api_gateway_domain_name.api.domain_name
  description = "Custom domain name for API Gateway (pass to Conveyor)"
}

output "api_domain_regional_domain_name" {
  value       = aws_api_gateway_domain_name.api.regional_domain_name
  description = "Regional domain name for the custom domain"
}

output "api_base_url" {
  value       = "https://${aws_api_gateway_domain_name.api.domain_name}"
  description = "Base URL for API requests"
}
```

## Provider Configuration

If your main infrastructure is not in us-east-1, you'll need a provider alias:

```hcl
# Main provider (your region)
provider "aws" {
  region = "us-west-2"
}

# Provider for ACM certificates (must be us-east-1 for API Gateway)
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
}
```

## Usage with Conveyor

After creating the custom domain, configure Conveyor to use it:

```hcl
resource "conveyor_belt" "main" {
  source            = "${path.module}/routes.tf.rb"
  app_name          = "myapp"
  lambda_source_dir = "${path.module}/lambda"
  frontend_urls     = ["https://app.example.com"]
  
  # Use the custom domain
  custom_domain_name = aws_api_gateway_domain_name.api.domain_name
  # Or reference the output:
  # custom_domain_name = module.dns.api_domain_name
}

# Access unified URLs
output "api_url" {
  value = conveyor_belt.main.custom_domain_url
  # => "https://api.example.com"
}

output "ops_api_url" {
  value = "${conveyor_belt.main.custom_domain_url}/ops"
  # => "https://api.example.com/ops"
}
```

## Verification Steps

After applying the Terraform configuration:

### 1. Verify Certificate Status

```bash
aws acm describe-certificate \
  --certificate-arn $(terraform output -raw api_cert_arn) \
  --region us-east-1 \
  --query 'Certificate.Status'
```

Expected output: `"ISSUED"`

### 2. Verify Domain Resolution

```bash
dig api.example.com
```

Should return an A record pointing to the API Gateway regional domain.

### 3. Verify Custom Domain in API Gateway

```bash
aws apigateway get-domain-name \
  --domain-name api.example.com
```

### 4. Test API Endpoint

After Conveyor creates the base path mappings:

```bash
# Test a specific gateway endpoint
curl https://api.example.com/ops/health

# List base path mappings
aws apigateway get-base-path-mappings \
  --domain-name api.example.com
```

## Troubleshooting

### Certificate Validation Timeout

If certificate validation takes too long:

1. Check DNS propagation: `dig _acme-challenge.api.example.com`
2. Verify Route 53 records were created
3. Wait up to 30 minutes for DNS propagation

### Domain Not Found Error

If Conveyor reports "Custom domain does not exist":

1. Verify the domain was created: `aws apigateway get-domain-name --domain-name api.example.com`
2. Ensure you're using the correct AWS region
3. Check IAM permissions for `apigateway:GetDomainName`

### Base Path Conflict

If you see "Base path already mapped":

1. List existing mappings: `aws apigateway get-base-path-mappings --domain-name api.example.com`
2. Remove conflicting mapping or use a different gateway name
3. Ensure only one Conveyor resource manages the domain

## Complete Example

See the [examples/with-custom-domain](../examples/with-custom-domain) directory for a complete working example.
