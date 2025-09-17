package proxyd

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	MetricsNamespace = "proxyd"

	RPCRequestSourceHTTP = "http"
	RPCRequestSourceWS   = "ws"

	BackendProxyd = "proxyd"
	SourceClient  = "client"
	SourceBackend = "backend"
	MethodUnknown = "unknown"
)

var PayloadSizeBuckets = []float64{10, 50, 100, 500, 1000, 5000, 10000, 100000, 1000000}
var MillisecondDurationBuckets = []float64{1, 10, 50, 100, 500, 1000, 5000, 10000, 100000}

var (
	rpcRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_requests_total",
		Help:      "Count of total client RPC requests.",
	})

	rpcForwardsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_forwards_total",
		Help:      "Count of total RPC requests forwarded to each backend.",
	}, []string{
		"auth",
		"backend_name",
		"method_name",
		"source",
	})

	rpcBackendHTTPResponseCodesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_backend_http_response_codes_total",
		Help:      "Count of total backend responses by HTTP status code.",
	}, []string{
		"auth",
		"backend_name",
		"method_name",
		"status_code",
		"batched",
	})

	rpcSupervisorChecksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_supervisor_checks_total",
		Help:      "Count of total supervisor checks.",
	}, []string{
		"supervisor_url",
		"http_code",
		"rpc_error_code",
		"strategy",
	})

	rpcErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_errors_total",
		Help:      "Count of total RPC errors.",
	}, []string{
		"auth",
		"backend_name",
		"method_name",
		"error_code",
	})

	rpcSpecialErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rpc_special_errors_total",
		Help:      "Count of total special RPC errors.",
	}, []string{
		"auth",
		"backend_name",
		"method_name",
		"error_type",
	})

	rpcBackendRequestDurationSumm = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace:  MetricsNamespace,
		Name:       "rpc_backend_request_duration_seconds",
		Help:       "Summary of backend response times broken down by backend and method name.",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.95: 0.005, 0.99: 0.001},
	}, []string{
		"backend_name",
		"method_name",
		"batched",
	})

	activeClientWsConnsGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "active_client_ws_conns",
		Help:      "Gauge of active client WS connections.",
	}, []string{
		"auth",
	})

	activeBackendWsConnsGauge = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "active_backend_ws_conns",
		Help:      "Gauge of active backend WS connections.",
	}, []string{
		"backend_name",
	})

	unserviceableRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "unserviceable_requests_total",
		Help:      "Count of total requests that were rejected due to no backends being available.",
	}, []string{
		"auth",
		"request_source",
	})

	httpResponseCodesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "http_response_codes_total",
		Help:      "Count of total HTTP response codes.",
	}, []string{
		"status_code",
	})

	httpRequestDurationSumm = promauto.NewSummary(prometheus.SummaryOpts{
		Namespace:  MetricsNamespace,
		Name:       "http_request_duration_seconds",
		Help:       "Summary of HTTP request durations, in seconds.",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.95: 0.005, 0.99: 0.001},
	})

	wsMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "ws_messages_total",
		Help:      "Count of total websocket messages including protocol control.",
	}, []string{
		"auth",
		"backend_name",
		"source",
	})

	redisErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "redis_errors_total",
		Help:      "Count of total Redis errors.",
	}, []string{
		"source",
	})

	requestPayloadSizesGauge = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: MetricsNamespace,
		Name:      "request_payload_sizes",
		Help:      "Histogram of client request payload sizes.",
		Buckets:   PayloadSizeBuckets,
	}, []string{
		"auth",
	})

	responsePayloadSizesGauge = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: MetricsNamespace,
		Name:      "response_payload_sizes",
		Help:      "Histogram of client response payload sizes.",
		Buckets:   PayloadSizeBuckets,
	}, []string{
		"auth",
	})

	cacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "cache_hits_total",
		Help:      "Number of cache hits.",
	}, []string{
		"method",
	})

	cacheMissesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "cache_misses_total",
		Help:      "Number of cache misses.",
	}, []string{
		"method",
	})

	cacheErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "cache_errors_total",
		Help:      "Number of cache errors.",
	}, []string{
		"method",
	})

	batchRPCShortCircuitsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "batch_rpc_short_circuits_total",
		Help:      "Count of total batch RPC short-circuits.",
	})

	rpcSpecialErrors = []string{
		"nonce too low",
		"gas price too high",
		"gas price too low",
		"invalid parameters",
		"txpool is full",
		"pool is full",
		"pool overflow",
		"transaction pool is full",
		"insufficient funds",
		"replacement underpriced",
		"replacement transaction underpriced",
		"already known",
		"max initcode size exceeded",
		"insufficient balance",
		"gas limit exceeded",
		"intrinsic gas too low",
	}

	redisCacheDurationSumm = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: MetricsNamespace,
		Name:      "redis_cache_duration_milliseconds",
		Help:      "Histogram of Redis command durations, in milliseconds.",
		Buckets:   MillisecondDurationBuckets,
	}, []string{"command"})

	tooManyRequestErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "too_many_request_errors_total",
		Help:      "Count of request timeouts due to too many concurrent RPCs.",
	}, []string{
		"backend_name",
	})

	batchSizeHistogram = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: MetricsNamespace,
		Name:      "batch_size_summary",
		Help:      "Summary of batch sizes",
		Buckets: []float64{
			1,
			5,
			10,
			25,
			50,
			100,
		},
	})

	frontendRateLimitTakeErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "rate_limit_take_errors",
		Help:      "Count of errors taking frontend rate limits",
	})

	// OTel: block-related metrics moved to OTel observable gauges

	consensusHAError = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "group_consensus_ha_error",
		Help:      "Consensus HA error count",
	}, []string{
		"error",
	})

	// OTel: HA block metrics moved to OTel observable gauges

	// OTel: backend block metrics moved to OTel observable gauges

	backendUnexpectedBlockTagsBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_unexpected_block_tags",
		Help:      "Bool gauge for unexpected block tags",
	}, []string{
		"backend_name",
	})

	consensusGroupCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "group_consensus_count",
		Help:      "Consensus group serving traffic count",
	}, []string{
		"backend_group_name",
	})

	consensusGroupFilteredCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "group_consensus_filtered_count",
		Help:      "Consensus group filtered out from serving traffic count",
	}, []string{
		"backend_group_name",
	})

	consensusGroupTotalCount = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "group_consensus_total_count",
		Help:      "Total count of candidates to be part of consensus group",
	}, []string{
		"backend_group_name",
	})

	consensusBannedBackends = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "consensus_backend_banned",
		Help:      "Bool gauge for banned backends",
	}, []string{
		"backend_name",
	})

	consensusPeerCountBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "consensus_backend_peer_count",
		Help:      "Peer count",
	}, []string{
		"backend_name",
	})

	consensusInSyncBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "consensus_backend_in_sync",
		Help:      "Bool gauge for backends in sync",
	}, []string{
		"backend_name",
	})

	consensusUpdateDelayBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "consensus_backend_update_delay",
		Help:      "Delay (ms) for backend update",
	}, []string{
		"backend_name",
	})

	avgLatencyBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_avg_latency",
		Help:      "Average latency per backend",
	}, []string{
		"backend_name",
	})

	degradedBackends = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_degraded",
		Help:      "Bool gauge for degraded backends",
	}, []string{
		"backend_name",
	})

	networkErrorRateBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_error_rate",
		Help:      "Request error rate per backend",
	}, []string{
		"backend_name",
	})

	healthyPrimaryCandidates = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "healthy_candidates",
		Help:      "Record the number of healthy primary candidates",
	}, []string{
		"backend_group_name",
	})

	backendGroupFallbackBackend = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_group_fallback_backenend",
		Help:      "Bool gauge for if a backend is a fallback for a backend group",
	}, []string{
		"backend_group",
		"backend_name",
		"fallback",
	})

	backendGroupMulticallCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_group_multicall_request_counter",
		Help:      "Record the amount of multicall requests",
	}, []string{
		"backend_group",
		"backend_name",
	})

	backendGroupMulticallCompletionCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: MetricsNamespace,
		Name:      "backend_group_multicall_completion_counter",
		Help:      "Record the amount of completed multicall requests",
	}, []string{
		"backend_group",
		"backend_name",
		"error",
	})
)

// =====================
// OpenTelemetry metrics
// =====================

var (
	otelOnce sync.Once

	// state stores the latest values; OTel callbacks read from here
	otelBlockState = struct {
		mu sync.RWMutex
		// group-level
		groupLatest    map[string]float64
		groupSafe      map[string]float64
		groupFinalized map[string]float64
		// HA group-level (key: group|leader)
		haLatest    map[string]float64
		haSafe      map[string]float64
		haFinalized map[string]float64
		// backend-level
		backendLatest    map[string]float64
		backendSafe      map[string]float64
		backendFinalized map[string]float64
	}{
		groupLatest:      map[string]float64{},
		groupSafe:        map[string]float64{},
		groupFinalized:   map[string]float64{},
		haLatest:         map[string]float64{},
		haSafe:           map[string]float64{},
		haFinalized:      map[string]float64{},
		backendLatest:    map[string]float64{},
		backendSafe:      map[string]float64{},
		backendFinalized: map[string]float64{},
	}
)

func initOTelBlockMetrics() {
	otelOnce.Do(func() {
		if !IsOTelEnabled() {
			return
		}
		m := otel.Meter("proxyd")

		// group level meters
		mustRegisterGaugeCallback(m, "proxyd.group_consensus_latest_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.groupLatest))
			for group, v := range otelBlockState.groupLatest {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_group_name", group))})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.group_consensus_safe_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.groupSafe))
			for group, v := range otelBlockState.groupSafe {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_group_name", group))})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.group_consensus_finalized_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.groupFinalized))
			for group, v := range otelBlockState.groupFinalized {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_group_name", group))})
			}
			return out
		})

		// HA group level (group + leader)
		mustRegisterGaugeCallback(m, "proxyd.group_consensus_ha_latest_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.haLatest))
			for key, v := range otelBlockState.haLatest {
				parts := strings.SplitN(key, "|", 2)
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(
					attribute.String("backend_group_name", parts[0]),
					attribute.String("leader", parts[1]),
				)})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.group_consensus_ha_safe_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.haSafe))
			for key, v := range otelBlockState.haSafe {
				parts := strings.SplitN(key, "|", 2)
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(
					attribute.String("backend_group_name", parts[0]),
					attribute.String("leader", parts[1]),
				)})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.group_consensus_ha_finalized_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.haFinalized))
			for key, v := range otelBlockState.haFinalized {
				parts := strings.SplitN(key, "|", 2)
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(
					attribute.String("backend_group_name", parts[0]),
					attribute.String("leader", parts[1]),
				)})
			}
			return out
		})

		// backend level
		mustRegisterGaugeCallback(m, "proxyd.backend_latest_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.backendLatest))
			for be, v := range otelBlockState.backendLatest {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_name", be))})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.backend_safe_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.backendSafe))
			for be, v := range otelBlockState.backendSafe {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_name", be))})
			}
			return out
		})

		mustRegisterGaugeCallback(m, "proxyd.backend_finalized_block", func() []Record {
			otelBlockState.mu.RLock()
			defer otelBlockState.mu.RUnlock()
			out := make([]Record, 0, len(otelBlockState.backendFinalized))
			for be, v := range otelBlockState.backendFinalized {
				out = append(out, Record{Number: v, Attributes: attribute.NewSet(attribute.String("backend_name", be))})
			}
			return out
		})
	})
}

// helper to register a gauge callback; panics on error in init
func mustRegisterGaugeCallback(m metric.Meter, name string, collect func() []Record) {
	g, err := m.Float64ObservableGauge(name)
	if err != nil {
		panic(err)
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		for _, r := range collect() {
			o.ObserveFloat64(g, r.Number, metric.WithAttributeSet(r.Attributes))
		}
		return nil
	}, g)
	if err != nil {
		panic(err)
	}
}

// lightweight record model for callback collections
type metricRecord struct {
	Number     float64
	Attributes attribute.Set
}

// alias name to avoid collision with package metric
type Record = metricRecord

func RecordRedisError(source string) {
	redisErrorsTotal.WithLabelValues(source).Inc()
}

func RecordRPCError(ctx context.Context, backendName, method string, err error) {
	rpcErr, ok := err.(*RPCErr)
	var code int
	if ok {
		MaybeRecordSpecialRPCError(ctx, backendName, method, rpcErr)
		code = rpcErr.Code
	} else {
		code = -1
	}

	rpcErrorsTotal.WithLabelValues(GetAuthCtx(ctx), backendName, method, strconv.Itoa(code)).Inc()
}

func RecordWSMessage(ctx context.Context, backendName, source string) {
	wsMessagesTotal.WithLabelValues(GetAuthCtx(ctx), backendName, source).Inc()
}

func RecordUnserviceableRequest(ctx context.Context, source string) {
	unserviceableRequestsTotal.WithLabelValues(GetAuthCtx(ctx), source).Inc()
}

func RecordRPCForward(ctx context.Context, backendName, method, source string) {
	rpcForwardsTotal.WithLabelValues(GetAuthCtx(ctx), backendName, method, source).Inc()
}

func MaybeRecordSpecialRPCError(ctx context.Context, backendName, method string, rpcErr *RPCErr) {
	errMsg := strings.ToLower(rpcErr.Message)
	for _, errStr := range rpcSpecialErrors {
		if strings.Contains(errMsg, errStr) {
			rpcSpecialErrorsTotal.WithLabelValues(GetAuthCtx(ctx), backendName, method, errStr).Inc()
			return
		}
	}
}

func RecordRequestPayloadSize(ctx context.Context, payloadSize int) {
	requestPayloadSizesGauge.WithLabelValues(GetAuthCtx(ctx)).Observe(float64(payloadSize))
}

func RecordResponsePayloadSize(ctx context.Context, payloadSize int) {
	responsePayloadSizesGauge.WithLabelValues(GetAuthCtx(ctx)).Observe(float64(payloadSize))
}

func RecordCacheHit(method string) {
	cacheHitsTotal.WithLabelValues(method).Inc()
}

func RecordCacheMiss(method string) {
	cacheMissesTotal.WithLabelValues(method).Inc()
}

func RecordCacheError(method string) {
	cacheErrorsTotal.WithLabelValues(method).Inc()
}

func RecordBatchSize(size int) {
	batchSizeHistogram.Observe(float64(size))
}

var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z ]+`)

func RecordGroupConsensusError(group *BackendGroup, label string, err error) {
	errClean := nonAlphanumericRegex.ReplaceAllString(err.Error(), "")
	errClean = strings.ReplaceAll(errClean, " ", "_")
	errClean = strings.ReplaceAll(errClean, "__", "_")
	label = fmt.Sprintf("%s.%s", label, errClean)
	consensusHAError.WithLabelValues(label).Inc()
}

func RecordGroupConsensusHALatestBlock(group *BackendGroup, leader string, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	key := fmt.Sprintf("%s|%s", group.Name, leader)
	otelBlockState.mu.Lock()
	otelBlockState.haLatest[key] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusHASafeBlock(group *BackendGroup, leader string, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	key := fmt.Sprintf("%s|%s", group.Name, leader)
	otelBlockState.mu.Lock()
	otelBlockState.haSafe[key] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusHAFinalizedBlock(group *BackendGroup, leader string, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	key := fmt.Sprintf("%s|%s", group.Name, leader)
	otelBlockState.mu.Lock()
	otelBlockState.haFinalized[key] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusLatestBlock(group *BackendGroup, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.groupLatest[group.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusSafeBlock(group *BackendGroup, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.groupSafe[group.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusFinalizedBlock(group *BackendGroup, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.groupFinalized[group.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordGroupConsensusCount(group *BackendGroup, count int) {
	consensusGroupCount.WithLabelValues(group.Name).Set(float64(count))
}

func RecordGroupConsensusFilteredCount(group *BackendGroup, count int) {
	consensusGroupFilteredCount.WithLabelValues(group.Name).Set(float64(count))
}

func RecordGroupTotalCount(group *BackendGroup, count int) {
	consensusGroupTotalCount.WithLabelValues(group.Name).Set(float64(count))
}

func RecordBackendLatestBlock(b *Backend, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.backendLatest[b.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordBackendSafeBlock(b *Backend, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.backendSafe[b.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordBackendFinalizedBlock(b *Backend, blockNumber hexutil.Uint64) {
	initOTelBlockMetrics()
	otelBlockState.mu.Lock()
	otelBlockState.backendFinalized[b.Name] = float64(blockNumber)
	otelBlockState.mu.Unlock()
}

func RecordBackendUnexpectedBlockTags(b *Backend, unexpected bool) {
	backendUnexpectedBlockTagsBackend.WithLabelValues(b.Name).Set(boolToFloat64(unexpected))
}

func RecordConsensusBackendBanned(b *Backend, banned bool) {
	consensusBannedBackends.WithLabelValues(b.Name).Set(boolToFloat64(banned))
}

func RecordHealthyCandidates(b *BackendGroup, candidates int) {
	healthyPrimaryCandidates.WithLabelValues(b.Name).Set(float64(candidates))
}

func RecordConsensusBackendPeerCount(b *Backend, peerCount uint64) {
	consensusPeerCountBackend.WithLabelValues(b.Name).Set(float64(peerCount))
}

func RecordConsensusBackendInSync(b *Backend, inSync bool) {
	consensusInSyncBackend.WithLabelValues(b.Name).Set(boolToFloat64(inSync))
}

func RecordConsensusBackendUpdateDelay(b *Backend, lastUpdate time.Time) {
	// avoid recording the delay for the first update
	if lastUpdate.IsZero() {
		return
	}
	delay := time.Since(lastUpdate)
	consensusUpdateDelayBackend.WithLabelValues(b.Name).Set(float64(delay.Milliseconds()))
}

func RecordBackendNetworkLatencyAverageSlidingWindow(b *Backend, avgLatency time.Duration) {
	avgLatencyBackend.WithLabelValues(b.Name).Set(float64(avgLatency.Milliseconds()))
	degradedBackends.WithLabelValues(b.Name).Set(boolToFloat64(b.IsDegraded()))
}

func RecordBackendNetworkErrorRateSlidingWindow(b *Backend, rate float64) {
	networkErrorRateBackend.WithLabelValues(b.Name).Set(rate)
}

func RecordBackendGroupFallbacks(bg *BackendGroup, name string, fallback bool) {
	backendGroupFallbackBackend.WithLabelValues(bg.Name, name, strconv.FormatBool(fallback)).Set(boolToFloat64(fallback))
}

func RecordBackendGroupMulticallRequest(bg *BackendGroup, backendName string) {
	backendGroupMulticallCounter.WithLabelValues(bg.Name, backendName).Inc()
}

func RecordBackendGroupMulticallCompletion(bg *BackendGroup, backendName string, error string) {
	backendGroupMulticallCompletionCounter.WithLabelValues(bg.Name, backendName, error).Inc()
}

func boolToFloat64(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
