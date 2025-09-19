package trace

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/log"
	"github.com/nacos-group/nacos-sdk-go/common/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"net"
	"strconv"
	"time"
)

// the global traceConfig
var GlobalTraceConfig *TraceConfig

type TraceConfig struct {
	Enabled            bool                `toml:"enabled"`
	ServiceName        string              `toml:"service_name"`
	Environment        string              `toml:"environment"`
	OTELEndpoint       string              `toml:"otel_endpoint"`
	SampleRate         float64             `toml:"sample_rate"`
	MethodWhiteListStr string              `toml:"trace_methods_whitelist"` // define the method need to be traced
	ServiceVersion     string              `toml:"-"`                       // ignore, this field is determined in runtime
	MethodWhiteList    map[string]struct{} `toml:"-"`                       // ignore, this field is determined by MethodWhiteListStr
}

var DefaultTraceConfig = TraceConfig{
	Enabled:            false,
	ServiceName:        "xlayer",
	Environment:        "PROD",
	SampleRate:         0.1,
	MethodWhiteListStr: DefaultMethodsWhiteListToTrace,
}

func NewTraceConfig(v string) TraceConfig {
	ret := DefaultTraceConfig
	ret.ServiceVersion = v
	return ret
}

func InitTraceConfig(traceConfig *TraceConfig) {
	traceConfig.MethodWhiteList = ParseMethodsWhiteListToTrace(traceConfig.MethodWhiteListStr)
}

// init the opentelemetry trace
func InitTracer(cfg *TraceConfig) (*sdktrace.TracerProvider, error) {
	if !cfg.Enabled {
		log.Info("Skip InitTracer for disabled")
		return nil, nil
	}
	logger.Info("Begin InitTracer",
		"url", cfg.OTELEndpoint,
		"serviceName", cfg.ServiceName, "serviceVersion",
		cfg.ServiceVersion, "environment", cfg.Environment,
		"sampleRate", cfg.SampleRate,
		"methodWhiteList", cfg.MethodWhiteList)

	ctx := context.Background()

	// create the otlp-http exporter
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.OTELEndpoint),
		otlptracehttp.WithTimeout(5*time.Second),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: 10 * time.Second, //the initial retry interval
			MaxInterval:     20 * time.Second, // the retry interval
			MaxElapsedTime:  20 * time.Second, // the time that drop the data
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	// create the resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
			semconv.HostIP(getLocalIP()),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// create the TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(createSampler(cfg.SampleRate)),
	)

	// set the global TracerProvider
	otel.SetTracerProvider(tp)

	// set the global propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	logger.Info("InitTracer Success",
		"url", cfg.OTELEndpoint,
		"serviceName", cfg.ServiceName, "serviceVersion",
		cfg.ServiceVersion, "environment", cfg.Environment,
		"sampleRate", cfg.SampleRate)
	return tp, nil
}

func createSampler(fraction float64) sdktrace.Sampler {
	return sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(fraction),
		sdktrace.WithRemoteParentSampled(sdktrace.AlwaysSample()),
		sdktrace.WithRemoteParentNotSampled(sdktrace.TraceIDRatioBased(fraction)),
		sdktrace.WithLocalParentSampled(sdktrace.AlwaysSample()),
		sdktrace.WithLocalParentNotSampled(sdktrace.TraceIDRatioBased(fraction)),
	)
}

// Shutdown the TracerProvider
func Shutdown(tp *sdktrace.TracerProvider) {
	if tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := tp.Shutdown(ctx); err != nil {
		logger.Warn("Error shutting down tracer provider, ", "error: ", err)
	}
}

func RecordError(span trace.Span, err error) {
	if span == nil || err == nil || !span.IsRecording() {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.SetAttributes(attribute.String("error", err.Error()))
}
func RecordErrors(span trace.Span, errs []error) {
	if span == nil || len(errs) == 0 || !span.IsRecording() {
		return
	}
	span.RecordError(errs[0])
	for i, e := range errs {
		if errs[i] == nil {
			continue
		}
		span.SetAttributes(attribute.String("error_"+strconv.Itoa(i), e.Error()))
	}
}

func RecordAttributes(span trace.Span, attributeKey string, attrs []string) {
	if span == nil || len(attrs) == 0 || !span.IsRecording() {
		return
	}
	for i, attr := range attrs {
		span.SetAttributes(attribute.String(attributeKey+strconv.Itoa(i), attr))
	}
}

func SetSpanAttribute(span trace.Span, attrs ...attribute.KeyValue) {
	if span == nil || !span.IsRecording() {
		return
	}
	span.SetAttributes(attrs...)
}

func RecordSingleSpanWithoutFilter(tracer trace.Tracer, ctx context.Context, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return recordSingleSpanImpl(tracer, ctx, "", false, spanName, attrs...)
}

func RecordSingleSpan(tracer trace.Tracer, ctx context.Context, method string, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return recordSingleSpanImpl(tracer, ctx, method, true, spanName, attrs...)
}

func CloseSpan(span trace.Span) {
	if span == nil {
		return
	}
	span.End()
}

func recordSingleSpanImpl(tracer trace.Tracer, ctx context.Context, method string, filter bool, spanName string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	// the trace is not enabled
	if tracer == nil || ctx == nil {
		return ctx, nil
	}
	if !GlobalTraceConfig.Enabled {
		return ctx, nil
	}
	// filter by the whitelist
	if filter {
		_, exists := GlobalTraceConfig.MethodWhiteList[AllowAll]
		// check the method if not specified allowAll
		if !exists {
			_, exists := GlobalTraceConfig.MethodWhiteList[method]
			if !exists {
				return ctx, nil
			}
		}
	}
	// the trace is enabled
	spanCtx, span := tracer.Start(ctx, spanName)
	if span.IsRecording() {
		span.SetAttributes(attrs...)
		remoteAddr, ok := ctx.Value(RemoteAddrKey).(string)
		if ok && remoteAddr != "" {
			span.SetAttributes(attribute.String(RemoteAddrKey, remoteAddr))
		}
		localAddr, ok := ctx.Value(LocalAddrKey).(string)
		if ok && localAddr != "" {
			span.SetAttributes(attribute.String(LocalAddrKey, localAddr))
		}
	}
	return spanCtx, span
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		logger.Warn("Failed to get local IP addresses, return empty ip address directly", "error:", err)
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	logger.Warn("No ipv4 address found, return empty ip address directly")
	return ""
}
