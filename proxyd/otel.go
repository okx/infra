package proxyd

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
)

var otelEnabled bool

func IsOTelEnabled() bool { return otelEnabled }

// InitOpenTelemetry initializes a minimal OpenTelemetry tracer provider.
// It returns a shutdown function that should be called on process exit.
func InitOpenTelemetry(ctx context.Context, cfg OTelConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		otelEnabled = false
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracegrpc.Option{}
	if cfg.Endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	attrs := []attribute.KeyValue{}
	if cfg.ServiceName != "" {
		attrs = append(attrs, attribute.String("service.name", cfg.ServiceName))
	} else {
		attrs = append(attrs, attribute.String("service.name", "proxyd"))
	}
	if cfg.ServiceNamespace != "" {
		attrs = append(attrs, attribute.String("service.namespace", cfg.ServiceNamespace))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment", cfg.Environment))
	}

	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return nil, err
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp,
			trace.WithMaxExportBatchSize(512),
			trace.WithBatchTimeout(2*time.Second),
		),
		trace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	otelEnabled = true
	log.Info("OpenTelemetry tracing initialized")
	return tp.Shutdown, nil
}


