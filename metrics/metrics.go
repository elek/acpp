package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/elek/acpp/acp"
	"github.com/elek/acpp/config"
)

// StatusProvider returns all current session StatusInfo values.
type StatusProvider func() []acp.StatusInfo

// shortAgent returns the base name of the agent binary (no path, no arguments).
func shortAgent(agent string) string {
	fields := strings.Fields(agent)
	if len(fields) == 0 {
		return agent
	}
	return filepath.Base(fields[0])
}

// shortDir returns the base name of a directory path.
func shortDir(dir string) string {
	return filepath.Base(dir)
}

// Start creates an OTel MeterProvider with Prometheus and OTLP exporters,
// registers callback-based instruments, and starts an HTTP server for /metrics.
// Returns a cleanup function that shuts down the server and meter provider.
func Start(ctx context.Context, addr string, otlpCfg config.OTLPConfig, provider StatusProvider) (func(), error) {
	// Prometheus exporter
	promExporter, err := otelprom.New()
	if err != nil {
		return nil, fmt.Errorf("creating prometheus exporter: %w", err)
	}

	opts := []sdkmetric.Option{
		sdkmetric.WithReader(promExporter),
	}

	// OTLP gRPC push exporter
	if otlpCfg.Endpoint != "" {
		grpcOpts := []otlpmetricgrpc.Option{
			otlpmetricgrpc.WithEndpoint(otlpCfg.Endpoint),
		}
		if otlpCfg.TLS.Insecure {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithInsecure())
		}
		if len(otlpCfg.Headers) > 0 {
			grpcOpts = append(grpcOpts, otlpmetricgrpc.WithHeaders(otlpCfg.Headers))
		}
		otlpExporter, err := otlpmetricgrpc.New(ctx, grpcOpts...)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP gRPC exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(otlpExporter, sdkmetric.WithInterval(15*time.Second)),
		))
	}

	mp := sdkmetric.NewMeterProvider(opts...)
	meter := mp.Meter("acpp")

	// Register callback-based instruments
	if err := registerInstruments(meter, provider); err != nil {
		mp.Shutdown(ctx)
		return nil, fmt.Errorf("registering instruments: %w", err)
	}

	// HTTP server for Prometheus /metrics and API endpoints
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/sessions.json", sessionsHandler(provider))
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		slog.Info("metrics server starting", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics server error", "error", err)
		}
	}()

	cleanup := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		mp.Shutdown(shutdownCtx)
	}

	return cleanup, nil
}

func registerInstruments(meter metric.Meter, provider StatusProvider) error {
	promptCount, err := meter.Int64ObservableCounter("acpp_prompt_count",
		metric.WithDescription("Total prompts sent"))
	if err != nil {
		return err
	}

	inputTokens, err := meter.Int64ObservableCounter("acpp_input_tokens_total",
		metric.WithDescription("Total input tokens"))
	if err != nil {
		return err
	}

	outputTokens, err := meter.Int64ObservableCounter("acpp_output_tokens_total",
		metric.WithDescription("Total output tokens"))
	if err != nil {
		return err
	}

	cacheCreationTokens, err := meter.Int64ObservableCounter("acpp_cache_creation_tokens_total",
		metric.WithDescription("Total cache creation input tokens"))
	if err != nil {
		return err
	}

	cacheReadTokens, err := meter.Int64ObservableCounter("acpp_cache_read_tokens_total",
		metric.WithDescription("Total cache read input tokens"))
	if err != nil {
		return err
	}

	webSearchRequests, err := meter.Int64ObservableCounter("acpp_web_search_requests_total",
		metric.WithDescription("Total web search requests"))
	if err != nil {
		return err
	}

	costUSD, err := meter.Float64ObservableCounter("acpp_cost_usd_total",
		metric.WithDescription("Total cost in USD"))
	if err != nil {
		return err
	}

	sessionStatus, err := meter.Int64ObservableGauge("acpp_session_status",
		metric.WithDescription("Session status (1 if active in this state)"))
	if err != nil {
		return err
	}

	contextWindow, err := meter.Int64ObservableGauge("acpp_context_window",
		metric.WithDescription("Context window size"))
	if err != nil {
		return err
	}

	maxOutputTokens, err := meter.Int64ObservableGauge("acpp_max_output_tokens",
		metric.WithDescription("Max output tokens limit"))
	if err != nil {
		return err
	}

	openSessions, err := meter.Int64ObservableGauge("acpp_open_sessions",
		metric.WithDescription("Number of currently open sessions"))
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		infos := provider()
		var running int64
		for _, info := range infos {
			if info.Status == acp.StatusRunning || info.Status == acp.StatusPending {
				running++
			}
		}
		o.ObserveInt64(openSessions, running)

		for _, info := range infos {
			attrs := metric.WithAttributes(
				attribute.String("agent", shortAgent(info.Agent)),
				attribute.String("dir", shortDir(info.CWD)),
			)

			o.ObserveInt64(promptCount, info.Usage.PromptCount, attrs)
			o.ObserveInt64(inputTokens, info.Usage.InputTokens, attrs)
			o.ObserveInt64(outputTokens, info.Usage.OutputTokens, attrs)
			o.ObserveInt64(cacheCreationTokens, info.Usage.CacheCreationInputTokens, attrs)
			o.ObserveInt64(cacheReadTokens, info.Usage.CacheReadInputTokens, attrs)
			o.ObserveInt64(webSearchRequests, info.Usage.WebSearchRequests, attrs)
			o.ObserveFloat64(costUSD, info.Usage.CostUSD, attrs)
			o.ObserveInt64(contextWindow, info.Usage.ContextWindow, attrs)
			o.ObserveInt64(maxOutputTokens, info.Usage.MaxOutputTokens, attrs)

			// Emit session status as labeled gauge (1 for current status, 0 for others)
			for _, st := range []acp.Status{acp.StatusPending, acp.StatusRunning, acp.StatusComplete, acp.StatusError} {
				var val int64
				if info.Status == st {
					val = 1
				}
				o.ObserveInt64(sessionStatus, val, metric.WithAttributes(
					attribute.String("agent", shortAgent(info.Agent)),
					attribute.String("dir", shortDir(info.CWD)),
					attribute.String("status", string(st)),
				))
			}
		}
		return nil
	},
		promptCount, inputTokens, outputTokens,
		cacheCreationTokens, cacheReadTokens, webSearchRequests,
		costUSD, sessionStatus, contextWindow, maxOutputTokens,
		openSessions,
	)
	return err
}

// jsonSession is the JSON representation of a session for /api/sessions.json.
type jsonSession struct {
	Source     string     `json:"source"`
	Agent      string     `json:"agent"`
	Dir        string     `json:"dir"`
	Status     string     `json:"status"`
	PID        int        `json:"pid,omitempty"`
	Model      string     `json:"model,omitempty"`
	SDKVersion string     `json:"sdk_version,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Usage      *jsonUsage `json:"usage,omitempty"`
}

// jsonUsage is the JSON representation of usage stats.
type jsonUsage struct {
	PromptCount              int64   `json:"prompt_count"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens,omitempty"`
	CostUSD                  float64 `json:"cost_usd,omitempty"`
}

// sessionsHandler returns an HTTP handler that serves /api/sessions.json.
func sessionsHandler(provider StatusProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		infos := provider()
		sessions := make([]jsonSession, 0, len(infos))
		for _, info := range infos {
			s := jsonSession{
				Source:     info.Source,
				Agent:      info.Agent,
				Dir:        info.CWD,
				Status:     string(info.Status),
				PID:        info.PID,
				Model:      info.Model,
				SDKVersion: info.SDKVersion,
				CreatedAt:  info.CreatedAt,
			}
			if info.HasUsage {
				s.Usage = &jsonUsage{
					PromptCount:              info.Usage.PromptCount,
					InputTokens:              info.Usage.InputTokens,
					OutputTokens:             info.Usage.OutputTokens,
					CacheCreationInputTokens: info.Usage.CacheCreationInputTokens,
					CacheReadInputTokens:     info.Usage.CacheReadInputTokens,
					CostUSD:                  info.Usage.CostUSD,
				}
			}
			sessions = append(sessions, s)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)
	}
}
