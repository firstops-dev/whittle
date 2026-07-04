# API Gateway Service Documentation

## Overview

The API Gateway Service acts as the primary entry point for all external requests to the FirstOps platform. It provides request routing, authentication, rate limiting, and response transformation for all downstream microservices. The service handles millions of requests per day and is designed to be horizontally scalable.

This document describes the architecture, deployment, and operational procedures for the API Gateway Service. It is intended for platform engineers, SREs, and developers who maintain or extend the service.

## Architecture

### Request Flow

All incoming requests pass through the API Gateway in the following order:

1. Client establishes TLS connection to the gateway endpoint
2. Request is parsed and validated against the schema
3. Authentication middleware verifies client identity and authorization
4. Rate limiting middleware checks if request is within quota
5. Request is routed to the appropriate backend service based on the path and method
6. Backend service processes the request and returns a response
7. Response is transformed and returned to the client

The gateway maintains persistent connections to backend services using HTTP/2 multiplexing to reduce latency and improve throughput. Each backend service is accessed through a dedicated connection pool with configurable size and timeout parameters.

### Configuration

The service is configured via YAML files in the `config/` directory. Configuration can be overridden at runtime using environment variables. The following sections describe the key configuration options.

#### TLS Configuration

The gateway requires TLS certificates for all client connections. Certificates can be provided in two ways:

1. Static files: Certificates are stored in the filesystem and loaded at startup
2. Dynamic provider: Certificates are fetched from a certificate management service like HashiCorp Vault

For production deployments, the dynamic provider is recommended to enable certificate rotation without restarting the service.

#### Upstream Configuration

Upstream services are configured in the `upstreams` section of the configuration file. Each upstream has the following properties:

- `name`: A unique identifier for the upstream (e.g., `customer-service`)
- `host`: The hostname or IP address of the service
- `port`: The port on which the service listens
- `protocol`: The protocol to use (http or http2)
- `pool_size`: The number of connections to maintain in the connection pool
- `timeout`: The timeout for requests in milliseconds
- `health_check`: Configuration for periodic health checks

## Performance

### Benchmarks

The API Gateway is designed to handle high throughput with minimal latency. The following benchmarks were measured on a t3.xlarge AWS EC2 instance with a single upstream service.

- Throughput: 50,000 requests per second
- Latency (p50): 1.2ms
- Latency (p99): 8.5ms
- Latency (p999): 42ms

These benchmarks represent requests for 1KB JSON payloads routed to a backend service. Latency increases linearly with payload size and number of hops to the backend service.

### Capacity Planning

The service can be scaled horizontally by deploying additional instances behind a load balancer. Each instance can handle approximately 50,000 requests per second, so a deployment with 10 instances can handle 500,000 requests per second.

The primary bottleneck is typically the upstream service, not the gateway itself. If you are seeing high latency or high error rates, check the health and performance of the upstream services first.

## Deployment

### Prerequisites

Before deploying the API Gateway Service, ensure you have the following:

1. A Kubernetes cluster with at least 3 nodes
2. TLS certificates for the gateway endpoint
3. Access to the configuration management system
4. An external load balancer or ingress controller

### Deployment Process

The service is deployed using Kubernetes manifests stored in the `deploy/` directory. To deploy:

1. Update the configuration files in `config/` with your environment-specific settings
2. Apply the Kubernetes manifests: `kubectl apply -f deploy/`
3. Wait for all replicas to be ready: `kubectl rollout status deployment/api-gateway`
4. Verify connectivity by sending a test request to the gateway endpoint

### Scaling

To scale the service horizontally, update the `replicas` field in the Kubernetes deployment manifest and apply the changes. The load balancer will automatically distribute incoming requests across all healthy instances.

For automatic scaling based on metrics, configure the Horizontal Pod Autoscaler (HPA) with the following metrics:

- CPU utilization target: 70%
- Memory utilization target: 80%
- Request rate: 40,000 requests per second per instance

## Monitoring

### Metrics

The service exports metrics in Prometheus format on the `/metrics` endpoint. The following metrics are available:

- `api_gateway_requests_total`: Total number of requests processed
- `api_gateway_request_duration_seconds`: Request latency histogram
- `api_gateway_upstream_requests_total`: Requests to upstream services
- `api_gateway_upstream_errors_total`: Errors from upstream services
- `api_gateway_rate_limit_exceeded_total`: Requests rejected due to rate limiting

### Logging

All requests are logged to stdout in JSON format. Logs include the following fields:

- `timestamp`: Time when the request was received
- `method`: HTTP method (GET, POST, etc.)
- `path`: Request path
- `status_code`: HTTP status code of the response
- `duration_ms`: Time taken to process the request
- `error`: Error message if the request failed

Logs can be aggregated using any standard log aggregation tool like ELK Stack, Datadog, or Splunk.

### Alerts

Configure alerts on the following conditions:

1. Error rate > 1%
2. Latency (p99) > 100ms
3. Upstream service down (health check failures)
4. Rate limiter rejecting > 10% of requests

## Troubleshooting

### High Latency

If you are experiencing high latency:

1. Check the latency of the upstream services: `kubectl logs -l app=api-gateway | grep upstream_latency`
2. Verify network connectivity between the gateway and upstream services
3. Check if rate limiting is being triggered: `curl http://localhost:8080/metrics | grep rate_limit_exceeded`
4. Increase the connection pool size in the configuration
5. Consider scaling out the service to additional nodes

### High Error Rate

If you are seeing a high error rate:

1. Check upstream service health: `kubectl get pods -l app=<upstream-service>`
2. Review the logs for specific error messages: `kubectl logs -l app=api-gateway | grep ERROR`
3. Verify client authentication credentials
4. Check if rate limits are being exceeded
5. Inspect the upstream service logs for internal errors

### Certificate Issues

If clients cannot connect due to certificate errors:

1. Verify the certificate is valid: `openssl x509 -in cert.pem -text -noout`
2. Check the expiration date: `openssl x509 -in cert.pem -noout -dates`
3. Verify the certificate is installed correctly: `kubectl get secrets -o name | grep tls`
4. If using the dynamic provider, check the provider service is healthy

## Advanced Configuration

### Custom Headers

The gateway can add or modify headers on requests to upstream services. This is useful for injecting authentication tokens or tracing information.

```yaml
upstreams:
  - name: backend-service
    headers:
      X-Forwarded-By: api-gateway
      X-Request-ID: ${request_id}
```

### Request Rewriting

Requests can be rewritten before being sent to upstream services using regular expressions.

```yaml
routes:
  - path: /api/v1/(.*)
    upstream: backend-service
    rewrite: /v2/$1
```

### Rate Limiting

Rate limiting can be configured at the global level or per-route. The following example limits each client to 100 requests per second:

```yaml
rate_limits:
  - name: global
    requests_per_second: 100
    burst_size: 10
```

### Authentication

The gateway supports multiple authentication methods including API keys, OAuth2, and mTLS. Authentication can be required globally or on a per-route basis.

```yaml
auth:
  global:
    type: oauth2
    provider_url: https://auth.example.com
```

## Troubleshooting Guide

### Debugging Requests

To enable debug logging:

```bash
kubectl set env deployment/api-gateway LOG_LEVEL=debug
```

To trace a specific request:

```bash
kubectl logs -l app=api-gateway | grep "${REQUEST_ID}"
```

### Performance Profiling

To capture CPU and memory profiles:

```bash
curl http://localhost:8080/debug/pprof/profile > cpu.prof
go tool pprof cpu.prof
```

### Code Examples

The following examples show how to deploy and configure the API Gateway Service.

```bash
#!/bin/bash
# Deploy the API Gateway Service

kubectl create namespace api-gateway
kubectl apply -f config/tls-secret.yaml -n api-gateway
kubectl apply -f deploy/deployment.yaml -n api-gateway
kubectl wait --for=condition=ready pod -l app=api-gateway -n api-gateway --timeout=300s
```

```go
// Example client code
package main

import (
    "fmt"
    "net/http"
)

func main() {
    client := &http.Client{}
    req, _ := http.NewRequest("GET", "http://api-gateway:8080/health", nil)
    req.Header.Add("Authorization", "Bearer token")
    resp, _ := client.Do(req)
    fmt.Println(resp.Status)
}
```

## Support

For issues or questions regarding the API Gateway Service:

1. Check the troubleshooting guide section above
2. Review the service logs: `kubectl logs -l app=api-gateway`
3. Open an issue in the internal issue tracking system
4. Contact the platform team on Slack (#infrastructure)

## References

- [Kubernetes Ingress Controller Documentation](https://kubernetes.io/docs/concepts/services-networking/ingress/)
- [Prometheus Metrics Format](https://prometheus.io/docs/concepts/data_model/)
- [OpenTelemetry Tracing Specification](https://opentelemetry.io/docs/reference/specification/protocol/exporter/)
