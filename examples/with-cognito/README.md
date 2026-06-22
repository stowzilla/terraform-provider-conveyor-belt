# Cognito Authentication Example

This example demonstrates how to use `conveyor_belt_lambda` and `conveyor_belt_gateway` resources with Cognito authentication.

## Architecture

- **Public API** - No authentication required (signup, login, health check)
- **Private API** - Cognito authentication required (customer profile, orders)
- **Admin API** - Cognito authentication required (admin operations)

## Resources Created

- 3 Lambda functions (`onboarding`, `customer`, `admin`)
- 3 API Gateways (`public`, `private`, `admin`)
- 1 Cognito User Pool with client
- IAM roles and policies for each Lambda
- CloudWatch log groups

## Usage

```bash
# Initialize
terraform init

# Preview changes
terraform plan

# Apply
terraform apply
```

## Testing

### Public Endpoints (no auth)

```bash
# Health check
curl $(terraform output -raw public_api_url)/health

# Signup
curl -X POST $(terraform output -raw public_api_url)/signup \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "Password123"}'
```

### Private Endpoints (requires Cognito token)

```bash
# Get token first (use AWS CLI or SDK)
TOKEN="your-cognito-jwt-token"

# Get profile
curl $(terraform output -raw private_api_url)/profile \
  -H "Authorization: Bearer $TOKEN"
```

## Outputs

- `cognito_user_pool_id` - Cognito User Pool ID
- `cognito_client_id` - Cognito Client ID
- `public_api_url` - Public API Gateway URL
- `private_api_url` - Private API Gateway URL
- `admin_api_url` - Admin API Gateway URL
- `lambda_arns` - Map of Lambda function ARNs
