package main

import (
	"context"
	"fmt"
	"github.com/ethereum-optimism/infra/proxyd/metrics/trace"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/ethereum/go-ethereum/log"
	"go.opentelemetry.io/otel/attribute"

	"github.com/ethereum-optimism/infra/proxyd"
)

var (
	GitVersion = ""
	GitCommit  = ""
	GitDate    = ""
)

const (
	podIpEnv = "MY_POD_IP"
)

func main() {
	// Set up logger with a default INFO level in case we fail to parse flags.
	// Otherwise the final critical log won't show what the parsing error was.
	proxyd.SetLogLevel(slog.LevelInfo)

	log.Info("starting proxyd", "version", GitVersion, "commit", GitCommit, "date", GitDate)

	if len(os.Args) < 2 {
		log.Crit("must specify a config file on the command line")
	}

	config := new(proxyd.Config)
	// create the default openTelemetry trace config
	proxydVersion := GitCommit + "-" + GitDate
	config.OpenTelemetryTrace = trace.NewTraceConfig(proxydVersion)
	if _, err := toml.DecodeFile(os.Args[1], config); err != nil {
		log.Crit("error reading config file", "err", err)
	}
	trace.InitTraceConfig(&config.OpenTelemetryTrace)
	trace.GlobalTraceConfig = &config.OpenTelemetryTrace

	// update log level from config
	logLevel, err := LevelFromString(config.Server.LogLevel)
	if err != nil {
		logLevel = log.LevelInfo
		if config.Server.LogLevel != "" {
			log.Warn("invalid server.log_level set: " + config.Server.LogLevel)
		}
	}
	proxyd.SetLogLevel(logLevel)

	if config.Server.EnablePprof {
		log.Info("starting pprof", "addr", "0.0.0.0", "port", "6060")
		pprofSrv := StartPProf("0.0.0.0", 6060)
		log.Info("started pprof server", "addr", pprofSrv.Addr)
		defer func() {
			if err := pprofSrv.Close(); err != nil {
				log.Error("failed to stop pprof server", "err", err)
			}
		}()
	}

	// Initialize OTEL client if enabled
	var metricsClient *proxyd.MetricsClient
	if config.OTel.Enabled {
		serviceName := config.OTel.ServiceName
		if serviceName == "" {
			serviceName = "proxyd"
		}

		if config.OTel.MetricsURL == "" {
			log.Crit("otel.metrics_url must be specified when otel.enabled is true")
		}

		ctx := context.Background()

		// Prepare common OTEL labels from environment variables
		commonLabels := []attribute.KeyValue{}

		// Add instance label from environment variable
		if instance := os.Getenv("MY_POD_IP"); instance != "" {
			commonLabels = append(commonLabels, attribute.String("instance", instance))
		}

		// Add host label from environment variable
		if host := os.Getenv("MY_POD_IP"); host != "" {
			commonLabels = append(commonLabels, attribute.String("host", host))
		}

		// Add job label from environment variable
		if job := os.Getenv("MY_SERVICE_NAME"); job != "" {
			commonLabels = append(commonLabels, attribute.String("job", job))
		}

		// Add deploy_version label from environment variable
		if deployVersion := os.Getenv("OKONE_DEPLOY_VERSION"); deployVersion != "" {
			commonLabels = append(commonLabels, attribute.String("deploy_version", deployVersion))
		}

		// Use configured namespace or default to "proxyd"
		namespace := config.OTel.Namespace
		if namespace == "" {
			namespace = "proxyd"
		}

		// Use configured export interval or default to 5 seconds
		exportInterval := time.Duration(config.OTel.ExportInterval)
		if exportInterval <= 0 {
			exportInterval = 5 * time.Second
		}

		metricsClient, err = proxyd.NewMetricsClient(ctx, serviceName, config.OTel.MetricsURL, namespace, commonLabels, exportInterval)
		if err != nil {
			log.Crit("error initializing OTEL metrics client", "err", err)
		}
		log.Info("initialized OTEL metrics client", "service_name", serviceName, "metrics_url", config.OTel.MetricsURL)

		// Set the global OTEL client for metrics
		proxyd.SetOTelClient(metricsClient)
	}

	// init the trace
	traceProvider, err := trace.InitTracer(&config.OpenTelemetryTrace)
	if err != nil {
		log.Error("Failed to initialize OpenTelemetry tracer", "err", err)
		return
	}
	defer traceProvider.Shutdown(context.Background())

	// non-blocking
	_, shutdown, err := proxyd.Start(config)
	if err != nil {
		log.Crit("error starting proxyd", "err", err)
	}

	// Register after start.
	if len(config.Nacos.URLs) > 0 {
		externalIP := config.Nacos.ExternalIP
		if os.Getenv(podIpEnv) != "" {
			externalIP = os.Getenv(podIpEnv)
			log.Warn("External IP replaced by env `MY_POD_IP`", "ExternalIP", externalIP)
		}

		proxyd.StartNacosClient(
			config.Nacos.URLs,
			config.Nacos.NamespaceId,
			config.Nacos.ApplicationName,
			externalIP,
			config.Nacos.ExternalPorts,
		)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	recvSig := <-sig
	log.Info("caught signal, shutting down", "signal", recvSig)

	// Shutdown OTEL client first
	if metricsClient != nil {
		ctx := context.Background()
		if err := metricsClient.Close(ctx); err != nil {
			log.Error("failed to close OTEL metrics client", "err", err)
		}
	}

	shutdown()
}

// LevelFromString returns the appropriate Level from a string name.
// Useful for parsing command line args and configuration files.
// It also converts strings to lowercase.
// Note: copied from op-service/log to avoid monorepo dependency
func LevelFromString(lvlString string) (slog.Level, error) {
	lvlString = strings.ToLower(lvlString) // ignore case
	switch lvlString {
	case "trace", "trce":
		return log.LevelTrace, nil
	case "debug", "dbug":
		return log.LevelDebug, nil
	case "info":
		return log.LevelInfo, nil
	case "warn":
		return log.LevelWarn, nil
	case "error", "eror":
		return log.LevelError, nil
	case "crit":
		return log.LevelCrit, nil
	default:
		return log.LevelDebug, fmt.Errorf("unknown level: %v", lvlString)
	}
}

func StartPProf(hostname string, port int) *http.Server {
	mux := http.NewServeMux()

	// have to do below to support multiple servers, since the
	// pprof import only uses DefaultServeMux
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))

	addr := net.JoinHostPort(hostname, strconv.Itoa(port))
	srv := &http.Server{
		Handler: mux,
		Addr:    addr,
	}

	// nolint:errcheck
	go srv.ListenAndServe()

	return srv
}
