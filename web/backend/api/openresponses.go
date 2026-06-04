package api

import (
	"fmt"
	"net/http"
	"net/http/httputil"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// registerOpenResponsesRoutes binds OpenResponses proxy endpoints to the ServeMux.
func (h *Handler) registerOpenResponsesRoutes(mux *http.ServeMux) {
	proxy := h.openResponsesProxyHandler()
	mux.Handle("POST /v1/responses", proxy)
	mux.Handle("POST /v1/responses/chat", proxy)
	mux.Handle("GET /v1/responses/sessions", proxy)
	mux.Handle("GET /v1/responses/sessions/{id}", proxy)
	mux.Handle("DELETE /v1/responses/sessions/{id}", proxy)
}

// decodeOpenResponsesSettings extracts the OpenResponses channel config and token.
func decodeOpenResponsesSettings(cfg *config.Config) (config.OpenResponsesSettings, bool) {
	if cfg == nil {
		return config.OpenResponsesSettings{}, false
	}

	bc := cfg.Channels.GetByType(config.ChannelOpenResponses)
	if bc == nil {
		return config.OpenResponsesSettings{}, false
	}

	var orCfg config.OpenResponsesSettings
	if err := bc.Decode(&orCfg); err != nil {
		return config.OpenResponsesSettings{}, false
	}

	return orCfg, bc.Enabled
}

func (h *Handler) createOpenResponsesProxy(token string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			r.SetURL(target)
			if token != "" {
				r.Out.Header.Set("Authorization", "Bearer "+token)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Errorf("Failed to proxy OpenResponses request: %v", err)
			http.Error(w, "Gateway unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
}

// openResponsesProxyHandler returns an http.Handler that forwards OpenResponses
// requests to the gateway.
func (h *Handler) openResponsesProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.gatewayAvailableForProxy() {
			logger.Warnf("Gateway not available for OpenResponses proxy")
			http.Error(w, "Gateway not available", http.StatusServiceUnavailable)
			return
		}

		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			logger.Warnf("Failed to load config for OpenResponses proxy: %v", err)
			http.Error(w, "Configuration error", http.StatusInternalServerError)
			return
		}

		orCfg, enabled := decodeOpenResponsesSettings(cfg)
		if !enabled {
			http.Error(w, "OpenResponses channel not enabled", http.StatusServiceUnavailable)
			return
		}

		h.createOpenResponsesProxy(orCfg.Token.String()).ServeHTTP(w, r)
	})
}

// EnsureOpenResponsesChannel enables the OpenResponses channel with sane defaults
// if it isn't already configured. Returns true when the config was modified.
func (h *Handler) EnsureOpenResponsesChannel() (bool, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return false, fmt.Errorf("failed to load config: %w", err)
	}

	changed := false

	bc := cfg.Channels.GetByType(config.ChannelOpenResponses)
	if bc == nil {
		bc = &config.Channel{Type: config.ChannelOpenResponses}
		cfg.Channels["openresponses"] = bc
	}

	if !bc.Enabled {
		bc.Enabled = true
		changed = true
	}

	if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
		if orCfg, ok := decoded.(*config.OpenResponsesSettings); ok {
			if orCfg.Token.String() == "" {
				orCfg.Token = *config.NewSecureString(generateSecureToken())
				changed = true
			}
		}
	}

	if changed {
		if err := config.SaveConfig(h.configPath, cfg); err != nil {
			return false, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return changed, nil
}
