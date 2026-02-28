package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"gopkg.in/yaml.v3"
)

// Account holds credentials for a single Claude.ai account.
type Account struct {
	Name       string `yaml:"name"`
	OrgID      string `yaml:"orgId"`
	SessionKey string `yaml:"sessionKey"`
}

// Config is the top-level configuration.
type Config struct {
	Accounts         []Account     `yaml:"accounts"`
	ActiveInterval   time.Duration `yaml:"activeInterval"`
	IdleInterval     time.Duration `yaml:"idleInterval"`
	IdleThreshold    int           `yaml:"idleThreshold"`
	ResetBurstWindow time.Duration `yaml:"resetBurstWindow"`
	ListenAddr       string        `yaml:"listenAddr"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{
		ActiveInterval:   30 * time.Second,
		IdleInterval:     5 * time.Minute,
		IdleThreshold:    3,
		ResetBurstWindow: 2 * time.Minute,
		ListenAddr:       ":9091",
	}
	return cfg, yaml.NewDecoder(f).Decode(cfg)
}

func setupOTel(ctx context.Context) (*metric.MeterProvider, error) {
	var readers []metric.Reader

	// Prometheus pull exporter — always on.
	promExporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}
	readers = append(readers, promExporter)

	// OTLP push exporter — enabled when OTEL_EXPORTER_OTLP_ENDPOINT is set.
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		otlpExporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return nil, err
		}
		readers = append(readers, metric.NewPeriodicReader(otlpExporter))
		log.Printf("OTLP exporter enabled: %s", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}

	opts := make([]metric.Option, len(readers))
	for i, r := range readers {
		opts[i] = metric.WithReader(r)
	}
	provider := metric.NewMeterProvider(opts...)
	otel.SetMeterProvider(provider)
	return provider, nil
}

func main() {
	configPath := os.Getenv("ACCOUNTS_FILE")
	if configPath == "" {
		configPath = "accounts.yaml"
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if len(cfg.Accounts) == 0 {
		log.Fatal("no accounts configured")
	}

	ctx := context.Background()
	provider, err := setupOTel(ctx)
	if err != nil {
		log.Fatalf("setup otel: %v", err)
	}
	defer func() { _ = provider.Shutdown(ctx) }()

	meter := provider.Meter("claude-usage-exporter")

	pollerCfg := pollerConfig{
		ActiveInterval:   cfg.ActiveInterval,
		IdleInterval:     cfg.IdleInterval,
		IdleThreshold:    cfg.IdleThreshold,
		ResetBurstWindow: cfg.ResetBurstWindow,
	}

	for _, acc := range cfg.Accounts {
		p, err := newAccountPoller(acc, pollerCfg, meter)
		if err != nil {
			log.Fatalf("create poller for %s: %v", acc.Name, err)
		}
		go p.run()
	}

	listenAddr := cfg.ListenAddr
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		listenAddr = v
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("listening on %s with %d account(s)", listenAddr, len(cfg.Accounts))
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
