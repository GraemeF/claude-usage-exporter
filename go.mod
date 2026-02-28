module github.com/GraemeF/claude-usage-exporter

go 1.23

require (
	github.com/prometheus/client_golang v1.20.5
	go.opentelemetry.io/otel v1.33.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.33.0
	go.opentelemetry.io/otel/exporters/prometheus v0.55.0
	go.opentelemetry.io/otel/metric v1.33.0
	go.opentelemetry.io/otel/sdk/metric v1.33.0
	gopkg.in/yaml.v3 v3.0.1
)
