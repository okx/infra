package main

import (
	"fmt"
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
	"github.com/nacos-group/nacos-sdk-go/clients"
	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/nacos-group/nacos-sdk-go/vo"
	"golang.org/x/exp/slog"

	"github.com/ethereum/go-ethereum/log"

	"github.com/ethereum-optimism/infra/proxyd"
)

var (
	GitVersion = ""
	GitCommit  = ""
	GitDate    = ""
)

const (
	nacosExternalListenAddr = "NACOS_EXTERNAL_LISTEN_ADDR"
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
	if _, err := toml.DecodeFile(os.Args[1], config); err != nil {
		log.Crit("error reading config file", "err", err)
	}

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

	// non-blocking
	_, shutdown, err := proxyd.Start(config)
	if err != nil {
		log.Crit("error starting proxyd", "err", err)
	}

	// Register after start.
	externalListenAddr := config.Nacos.ExternalListenAddr
	if os.Getenv(nacosExternalListenAddr) != "" {
		externalListenAddr = os.Getenv(nacosExternalListenAddr)
	}
	log.Info("registering with nacos:", "nacos", config.Nacos, "externalListenAddr", externalListenAddr)
	//	if len(config.Nacos.URLs) > 0 {
	log.Info("registering with nacos+++++++++:", "nacos", config.Nacos, "externalListenAddr", externalListenAddr)

	startNacosClient(
		config.Nacos.URLs,
		config.Nacos.NamespaceId,
		config.Nacos.ApplicationName,
		externalListenAddr,
	)

	log.Info("registering with ==+==++++:", "nacos", config.Nacos, "externalListenAddr", externalListenAddr)
	//	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	recvSig := <-sig
	log.Info("caught signal, shutting down", "signal", recvSig)
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

const (
	defaultPort           = 26659
	defaultTimeoutMs      = uint64(5000)
	defaultListenInterval = uint64(10000)
	defaultWeight         = float64(10)
)

// StartNacosClient start nacos client and register rest service in nacos
func startNacosClient(urls string, namespace string, name string, externalAddr string) {
	log.Info("start nacos client", "urls", urls, "namespace", namespace, "name", name, "externalAddr", externalAddr)
	ip, port, err := ResolveIPAndPort(externalAddr)
	if err != nil {
		log.Error(fmt.Sprintf("failed to resolve %s error: %s", externalAddr, err.Error()))
		return
	}

	serverConfigs, err := getServerConfigs(urls)
	if err != nil {
		log.Error(fmt.Sprintf("failed to resolve nacos server url %s: %s", urls, err.Error()))
		return
	}
	client, err := clients.CreateNamingClient(map[string]interface{}{
		"serverConfigs": serverConfigs,
		"clientConfig": constant.ClientConfig{
			TimeoutMs:           defaultTimeoutMs,
			ListenInterval:      defaultListenInterval,
			NotLoadCacheAtStart: true,
			NamespaceId:         namespace,
			LogDir:              "/dev/null",
			LogLevel:            "error",
		},
	})
	if err != nil {
		log.Error(fmt.Sprintf("failed to create nacos client. error: %s", err.Error()))
		return
	}

	_, err = client.RegisterInstance(vo.RegisterInstanceParam{
		Ip:          ip,
		Port:        port,
		ServiceName: name,
		Weight:      defaultWeight,
		ClusterName: "DEFAULT",
		Enable:      true,
		Healthy:     true,
		Ephemeral:   true,
		Metadata: map[string]string{
			"preserved.register.source": "GO",
			"app_registry_tag":          strconv.FormatInt(time.Now().Unix(), 10),
		},
	})
	if err != nil {
		log.Error(fmt.Sprintf("failed to register instance in nacos server. error: %s", err.Error()))
		return
	}
	log.Info("register application instance in nacos successfully")
}

// ResolveIPAndPort resolve ip and port from addr
func ResolveIPAndPort(addr string) (string, uint64, error) {
	laddr := strings.Split(addr, ":")
	ip := laddr[0]
	if ip == "127.0.0.1" {
		return GetLocalIP(), defaultPort, nil
	}

	port, err := strconv.ParseUint(laddr[1], 10, 64)
	if err != nil {
		return "", 0, err
	}
	return ip, port, nil
}

// GetLocalIP get local ip
func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

func getServerConfigs(urls string) ([]constant.ServerConfig, error) {
	// nolint
	var configs []constant.ServerConfig
	for _, url := range strings.Split(urls, ",") {
		laddr, serverPort, err := ResolveIPAndPort(url)
		if err != nil {
			return nil, fmt.Errorf("Err parsing host: %+w", err)
		}
		configs = append(configs, constant.ServerConfig{
			IpAddr: laddr,
			Port:   serverPort,
		})
	}
	return configs, nil
}
