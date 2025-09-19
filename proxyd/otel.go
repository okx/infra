package proxyd

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
)

type MetricsClient struct {
	meter        metric.Meter
	provider     *sdkmetric.MeterProvider
	counters     sync.Map
	gauges       sync.Map
	histogram    sync.Map
	namespace    string
	commonLabels []attribute.KeyValue
}

func NewMetricsClient(ctx context.Context, serviceName string, metricsURL string, namespace string, commonLabels []attribute.KeyValue, exportInterval time.Duration) (*MetricsClient, error) {
	// Parse URL to get endpoint and path
	u, err := url.Parse(metricsURL)
	if err != nil {
		return nil, fmt.Errorf("parse metrics URL fail: %w", err)
	}
	options := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(u.Host),
		otlpmetrichttp.WithURLPath(u.Path),
	}
	if !strings.Contains(metricsURL, "https") {
		options = append(options, otlpmetrichttp.WithInsecure())
	}
	logrus.Infof("init metrics client host:%v path:%v", u.Host, u.Path)
	exporter, err := otlpmetrichttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter fail: %w", err)
	}
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(exportInterval))),
	)
	otel.SetMeterProvider(provider)
	meter := provider.Meter(serviceName)
	return &MetricsClient{
		meter:        meter,
		provider:     provider,
		namespace:    namespace,
		commonLabels: commonLabels,
	}, nil
}

func (c *MetricsClient) Counter(name, description string) (metric.Int64Counter, error) {
	if counter, loaded := c.counters.Load(name); loaded {
		return counter.(metric.Int64Counter), nil
	}
	counter, err := c.meter.Int64Counter(
		name,
		metric.WithDescription(description),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create counter %q: %w", name, err)
	}
	actual, loaded := c.counters.LoadOrStore(name, counter)
	if loaded {
		return actual.(metric.Int64Counter), nil
	}
	return counter, nil
}

func (c *MetricsClient) Gauge(name, description string) (metric.Float64Gauge, error) {
	if gauge, loaded := c.gauges.Load(name); loaded {
		return gauge.(metric.Float64Gauge), nil
	}
	gauge, err := c.meter.Float64Gauge(
		name,
		metric.WithDescription(description),
		metric.WithUnit("1"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gauge %q: %w", name, err)
	}
	actual, loaded := c.gauges.LoadOrStore(name, gauge)
	if loaded {
		return actual.(metric.Float64Gauge), nil
	}
	return gauge, nil
}

// addNamespaceAndCommonLabels adds namespace prefix to metric name and combines common labels with specific labels
func (c *MetricsClient) addNamespaceAndCommonLabels(name string, specificLabels ...attribute.KeyValue) (string, []attribute.KeyValue) {
	// Add namespace prefix
	fullName := name
	if c.namespace != "" {
		fullName = c.namespace + "_" + name
	}

	// Combine common labels with specific labels
	allLabels := make([]attribute.KeyValue, 0, len(c.commonLabels)+len(specificLabels))
	allLabels = append(allLabels, c.commonLabels...)
	allLabels = append(allLabels, specificLabels...)

	return fullName, allLabels
}

func (c *MetricsClient) GaugeRecord(ctx context.Context, name string, description string, value float64, attrs ...attribute.KeyValue) error {
	fullName, allLabels := c.addNamespaceAndCommonLabels(name, attrs...)
	gauge, err := c.Gauge(fullName, description)
	if err != nil {
		return err
	}
	recordOpts := []metric.RecordOption{
		metric.WithAttributes(allLabels...),
	}
	gauge.Record(ctx, value, recordOpts...)
	return nil
}

func (c *MetricsClient) Histogram(name, description string) (metric.Float64Histogram, error) {
	if histogram, loaded := c.histogram.Load(name); loaded {
		return histogram.(metric.Float64Histogram), nil
	}
	histogram, err := c.meter.Float64Histogram(
		name,
		metric.WithDescription(description),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create histogram %q: %w", name, err)
	}
	actual, loaded := c.histogram.LoadOrStore(name, histogram)
	if loaded {
		return actual.(metric.Float64Histogram), nil
	}
	return histogram, nil
}

func (c *MetricsClient) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.provider.Shutdown(ctx); err != nil {
		logrus.Errorf("Error shutting down metrics provider: %v", err)
		return err
	}
	return nil
}

func (c *MetricsClient) CounterAdd(ctx context.Context, name string, description string, value int64, attrs ...attribute.KeyValue) error {
	fullName, allLabels := c.addNamespaceAndCommonLabels(name, attrs...)
	counter, err := c.Counter(fullName, description)
	if err != nil {
		return err
	}
	recordOpts := []metric.AddOption{
		metric.WithAttributes(allLabels...),
	}
	counter.Add(ctx, value, recordOpts...)
	return nil
}

func (c *MetricsClient) Upgrade() metric.Meter {
	return c.meter
}
