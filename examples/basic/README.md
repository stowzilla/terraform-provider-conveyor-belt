# Basic Example

This example demonstrates how to use the `conveyor_belt_lambda` and `conveyor_belt_gateway` resources to create a simple API.

## Resources Created

- 2 Lambda functions (`customer`, `orders`)
- 1 API Gateway with 3 routes
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

## API Endpoints

After applying, the API Gateway will expose:

- `GET /customer` - Get customer information
- `GET /orders` - List orders
- `POST /orders` - Create a new order

## Outputs

- `api_url` - The API Gateway invoke URL
- `lambda_arns` - Map of Lambda function ARNs
