module github.com/dashpole/dashpole_demos/tracing_app/cmd

go 1.15

require (
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.12.0
	go.opentelemetry.io/otel v0.12.0
	go.opentelemetry.io/otel/exporters/otlp v0.12.0
	go.opentelemetry.io/otel/sdk v0.12.0
	google.golang.org/grpc v1.32.0
)
