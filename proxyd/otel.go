package proxyd

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	metricsdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
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

	// Metrics exporter
	mopts := []otlpmetricgrpc.Option{}
	if cfg.Endpoint != "" {
		mopts = append(mopts, otlpmetricgrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		mopts = append(mopts, otlpmetricgrpc.WithInsecure())
	}
	mexp, err := otlpmetricgrpc.New(ctx, mopts...)
	if err != nil {
		return nil, err
	}
	mp := metricsdk.NewMeterProvider(
		metricsdk.WithResource(res),
		metricsdk.WithReader(metricsdk.NewPeriodicReader(
			mexp,
			metricsdk.WithInterval(10*time.Second),
		)),
	)
	otel.SetMeterProvider(mp)

	otelEnabled = true
	log.Info("OpenTelemetry metrics initialized")
	return mp.Shutdown, nil
}
