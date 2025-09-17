package proxyd

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	metricsdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"net/url"
	"strings"
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
	mopts := []otlpmetrichttp.Option{}
	if cfg.Endpoint != "" {
		// Support full URL like http://host/path/to/metrics or https://host/path
		if strings.HasPrefix(strings.ToLower(cfg.Endpoint), "http://") || strings.HasPrefix(strings.ToLower(cfg.Endpoint), "https://") {
			u, err := url.Parse(cfg.Endpoint)
			if err == nil {
				if u.Host != "" {
					mopts = append(mopts, otlpmetrichttp.WithEndpoint(u.Host))
				}
				if u.Path != "" {
					mopts = append(mopts, otlpmetrichttp.WithURLPath(u.Path))
				}
				if u.Scheme == "http" {
					mopts = append(mopts, otlpmetrichttp.WithInsecure())
				}
			} else {
				// Fallback to raw endpoint if parse fails
				mopts = append(mopts, otlpmetrichttp.WithEndpoint(cfg.Endpoint))
			}
		} else {
			// Raw host[:port]
			mopts = append(mopts, otlpmetrichttp.WithEndpoint(cfg.Endpoint))
		}
	}
	if cfg.Insecure {
		mopts = append(mopts, otlpmetrichttp.WithInsecure())
	}
	mexp, err := otlpmetrichttp.New(ctx, mopts...)
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
