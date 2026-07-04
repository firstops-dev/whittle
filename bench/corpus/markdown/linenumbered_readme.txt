1	# API Gateway Service Documentation
2	
3	## Overview
4	
5	The API Gateway Service acts as the primary entry point for all external requests to the FirstOps platform. It provides request routing, authentication, rate limiting, and response transformation for all downstream microservices. The service handles millions of requests per day and is designed to be horizontally scalable.
6	
7	This document describes the architecture, deployment, and operational procedures for the API Gateway Service. It is intended for platform engineers, SREs, and developers who maintain or extend the service.
8	
9	## Architecture
10	
11	### Request Flow
12	
13	All incoming requests pass through the API Gateway in the following order:
14	
15	1. Client establishes TLS connection to the gateway endpoint
16	2. Request is parsed and validated against the schema
17	3. Authentication middleware verifies client identity and authorization
18	4. Rate limiting middleware checks if request is within quota
19	5. Request is routed to the appropriate backend service based on the path and method
20	6. Backend service processes the request and returns a response
21	7. Response is transformed and returned to the client
22	
23	The gateway maintains persistent connections to backend services using HTTP/2 multiplexing to reduce latency and improve throughput. Each backend service is accessed through a dedicated connection pool with configurable size and timeout parameters.
24	
25	### Configuration
26	
27	The service is configured via YAML files in the `config/` directory. Configuration can be overridden at runtime using environment variables. The following sections describe the key configuration options.
28	
29	#### TLS Configuration
30	
31	The gateway requires TLS certificates for all client connections. Certificates can be provided in two ways:
32	
33	1. Static files: Certificates are stored in the filesystem and loaded at startup
34	2. Dynamic provider: Certificates are fetched from a certificate management service like HashiCorp Vault
35	
36	For production deployments, the dynamic provider is recommended to enable certificate rotation without restarting the service.
37	
38	#### Upstream Configuration
39	
40	Upstream services are configured in the `upstreams` section of the configuration file. Each upstream has the following properties:
41	
42	- `name`: A unique identifier for the upstream (e.g., `customer-service`)
43	- `host`: The hostname or IP address of the service
44	- `port`: The port on which the service listens
45	- `protocol`: The protocol to use (http or http2)
46	- `pool_size`: The number of connections to maintain in the connection pool
47	- `timeout`: The timeout for requests in milliseconds
48	- `health_check`: Configuration for periodic health checks
49	
50	## Performance
51	
52	### Benchmarks
53	
54	The API Gateway is designed to handle high throughput with minimal latency. The following benchmarks were measured on a t3.xlarge AWS EC2 instance with a single upstream service.
55	
56	- Throughput: 50,000 requests per second
57	- Latency (p50): 1.2ms
58	- Latency (p99): 8.5ms
59	- Latency (p999): 42ms
60	
61	These benchmarks represent requests for 1KB JSON payloads routed to a backend service. Latency increases linearly with payload size and number of hops to the backend service.
62	
63	### Capacity Planning
64	
65	The service can be scaled horizontally by deploying additional instances behind a load balancer. Each instance can handle approximately 50,000 requests per second, so a deployment with 10 instances can handle 500,000 requests per second.
66	
67	The primary bottleneck is typically the upstream service, not the gateway itself. If you are seeing high latency or high error rates, check the health and performance of the upstream services first.
68	
69	## Deployment
70	
71	### Prerequisites
72	
73	Before deploying the API Gateway Service, ensure you have the following:
74	
75	1. A Kubernetes cluster with at least 3 nodes
76	2. TLS certificates for the gateway endpoint
77	3. Access to the configuration management system
78	4. An external load balancer or ingress controller
79	
80	### Deployment Process
81	
82	The service is deployed using Kubernetes manifests stored in the `deploy/` directory. To deploy:
83	
84	1. Update the configuration files in `config/` with your environment-specific settings
85	2. Apply the Kubernetes manifests: `kubectl apply -f deploy/`
86	3. Wait for all replicas to be ready: `kubectl rollout status deployment/api-gateway`
87	4. Verify connectivity by sending a test request to the gateway endpoint
88	
89	### Scaling
90	
91	To scale the service horizontally, update the `replicas` field in the Kubernetes deployment manifest and apply the changes. The load balancer will automatically distribute incoming requests across all healthy instances.
92	
93	For automatic scaling based on metrics, configure the Horizontal Pod Autoscaler (HPA) with the following metrics:
94	
95	- CPU utilization target: 70%
96	- Memory utilization target: 80%
97	- Request rate: 40,000 requests per second per instance
98	
99	## Monitoring
100	
101	### Metrics
102	
103	The service exports metrics in Prometheus format on the `/metrics` endpoint. The following metrics are available:
104	
105	- `api_gateway_requests_total`: Total number of requests processed
106	- `api_gateway_request_duration_seconds`: Request latency histogram
107	- `api_gateway_upstream_requests_total`: Requests to upstream services
108	- `api_gateway_upstream_errors_total`: Errors from upstream services
109	- `api_gateway_rate_limit_exceeded_total`: Requests rejected due to rate limiting
110	
111	### Logging
112	
113	All requests are logged to stdout in JSON format. Logs include the following fields:
114	
115	- `timestamp`: Time when the request was received
116	- `method`: HTTP method (GET, POST, etc.)
117	- `path`: Request path
118	- `status_code`: HTTP status code of the response
119	- `duration_ms`: Time taken to process the request
120	- `error`: Error message if the request failed
121	
122	Logs can be aggregated using any standard log aggregation tool like ELK Stack, Datadog, or Splunk.
123	
124	### Alerts
125	
126	Configure alerts on the following conditions:
127	
128	1. Error rate > 1%
129	2. Latency (p99) > 100ms
130	3. Upstream service down (health check failures)
131	4. Rate limiter rejecting > 10% of requests
132	
133	## Troubleshooting
134	
135	### High Latency
136	
137	If you are experiencing high latency:
138	
139	1. Check the latency of the upstream services: `kubectl logs -l app=api-gateway | grep upstream_latency`
140	2. Verify network connectivity between the gateway and upstream services
141	3. Check if rate limiting is being triggered: `curl http://localhost:8080/metrics | grep rate_limit_exceeded`
142	4. Increase the connection pool size in the configuration
143	5. Consider scaling out the service to additional nodes
144	
145	### High Error Rate
146	
147	If you are seeing a high error rate:
148	
149	1. Check upstream service health: `kubectl get pods -l app=<upstream-service>`
150	2. Review the logs for specific error messages: `kubectl logs -l app=api-gateway | grep ERROR`
151	3. Verify client authentication credentials
152	4. Check if rate limits are being exceeded
153	5. Inspect the upstream service logs for internal errors
154	
155	### Certificate Issues
156	
157	If clients cannot connect due to certificate errors:
158	
159	1. Verify the certificate is valid: `openssl x509 -in cert.pem -text -noout`
160	2. Check the expiration date: `openssl x509 -in cert.pem -noout -dates`
161	3. Verify the certificate is installed correctly: `kubectl get secrets -o name | grep tls`
162	4. If using the dynamic provider, check the provider service is healthy
163	
164	## Advanced Configuration
165	
166	### Custom Headers
167	
168	The gateway can add or modify headers on requests to upstream services. This is useful for injecting authentication tokens or tracing information.
169	
170	```yaml
171	upstreams:
172	  - name: backend-service
173	    headers:
174	      X-Forwarded-By: api-gateway
175	      X-Request-ID: ${request_id}
176	```
177	
178	### Request Rewriting
179	
180	Requests can be rewritten before being sent to upstream services using regular expressions.
181	
182	```yaml
183	routes:
184	  - path: /api/v1/(.*)
185	    upstream: backend-service
186	    rewrite: /v2/$1
187	```
188	
189	### Rate Limiting
190	
191	Rate limiting can be configured at the global level or per-route. The following example limits each client to 100 requests per second:
192	
193	```yaml
194	rate_limits:
195	  - name: global
196	    requests_per_second: 100
197	    burst_size: 10
198	```
199	
200	### Authentication
201	
202	The gateway supports multiple authentication methods including API keys, OAuth2, and mTLS. Authentication can be required globally or on a per-route basis.
203	
204	```yaml
205	auth:
206	  global:
207	    type: oauth2
208	    provider_url: https://auth.example.com
209	```
210	
211	## Troubleshooting Guide
212	
213	### Debugging Requests
214	
215	To enable debug logging:
216	
217	```bash
218	kubectl set env deployment/api-gateway LOG_LEVEL=debug
219	```
220	
221	To trace a specific request:
222	
223	```bash
224	kubectl logs -l app=api-gateway | grep "${REQUEST_ID}"
225	```
226	
227	### Performance Profiling
228	
229	To capture CPU and memory profiles:
230	
231	```bash
232	curl http://localhost:8080/debug/pprof/profile > cpu.prof
233	go tool pprof cpu.prof
234	```
235	
236	### Code Examples
237	
238	The following examples show how to deploy and configure the API Gateway Service.
239	
240	```bash
241	#!/bin/bash
242	# Deploy the API Gateway Service
243	
244	kubectl create namespace api-gateway
245	kubectl apply -f config/tls-secret.yaml -n api-gateway
246	kubectl apply -f deploy/deployment.yaml -n api-gateway
247	kubectl wait --for=condition=ready pod -l app=api-gateway -n api-gateway --timeout=300s
248	```
249	
250	```go
251	// Example client code
252	package main
253	
254	import (
255	    "fmt"
256	    "net/http"
257	)
258	
259	func main() {
260	    client := &http.Client{}
261	    req, _ := http.NewRequest("GET", "http://api-gateway:8080/health", nil)
262	    req.Header.Add("Authorization", "Bearer token")
263	    resp, _ := client.Do(req)
264	    fmt.Println(resp.Status)
265	}
266	```
267	
268	## Support
269	
270	For issues or questions regarding the API Gateway Service:
271	
272	1. Check the troubleshooting guide section above
273	2. Review the service logs: `kubectl logs -l app=api-gateway`
274	3. Open an issue in the internal issue tracking system
275	4. Contact the platform team on Slack (#infrastructure)
276	
277	## References
278	
279	- [Kubernetes Ingress Controller Documentation](https://kubernetes.io/docs/concepts/services-networking/ingress/)
280	- [Prometheus Metrics Format](https://prometheus.io/docs/concepts/data_model/)
281	- [OpenTelemetry Tracing Specification](https://opentelemetry.io/docs/reference/specification/protocol/exporter/)
