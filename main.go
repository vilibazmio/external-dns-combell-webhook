package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/external-dns/provider/webhook/api"

	"github.com/vilibazmio/external-dns-combell-webhook/combell"
)

// external-dns expects the provider API on localhost only and the health
// endpoint on all interfaces, so the kubelet can probe it.
const (
	providerAddr = "127.0.0.1:8888"
	healthAddr   = ":8080"
)

func main() {
	apiKey := strings.TrimSpace(os.Getenv("COMBELL_API_KEY"))
	apiSecret := strings.TrimSpace(os.Getenv("COMBELL_API_SECRET"))
	if apiKey == "" || apiSecret == "" {
		slog.Error("COMBELL_API_KEY and COMBELL_API_SECRET must be set")
		os.Exit(1)
	}

	var domainFilter []string
	for _, d := range strings.Split(os.Getenv("DOMAIN_FILTER"), ",") {
		if d = strings.TrimSpace(d); d != "" {
			domainFilter = append(domainFilter, d)
		}
	}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ok"))
		})
		srv := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			slog.Error("health server stopped", "error", err)
			os.Exit(1)
		}
	}()

	slog.Info("starting", "provider", providerAddr, "health", healthAddr, "domainFilter", domainFilter)
	api.StartHTTPApi(api.ServerOptions{
		Provider:          newProvider(combell.NewClient(apiKey, apiSecret), domainFilter),
		ProviderPort:      providerAddr,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	})
}
