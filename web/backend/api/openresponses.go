package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// registerOpenResponsesRoutes binds OpenResponses Channel management endpoints to the ServeMux.
func (h *Handler) registerOpenResponsesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/openresponses/token", h.handleGetOpenResponsesToken)
	mux.HandleFunc("POST /api/openresponses/token", h.handleRegenOpenResponsesToken)
	mux.HandleFunc("POST /api/openresponses/setup", h.handleOpenResponsesSetup)
	mux.HandleFunc("POST /api/openresponses/chat", h.handleOpenResponsesChat)
}

// createOpenResponsesProxy creates a reverse proxy to the gateway OpenResponses endpoint.
func (h *Handler) createOpenResponsesProxy(token string) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			target := h.gatewayProxyURL()
			// Force exact path to the OpenResponses endpoint.
			// Do NOT use original request URL path.
			target.Path = "/v1/responses"
			target.RawPath = ""
			r.SetURL(target)
			// httputil.ReverseProxy may append the original In URL path to the
			// rewritten Out URL; force-reset the path so the request is sent
			// exactly to /v1/responses regardless of the incoming path.
			if r.Out.URL != nil {
				r.Out.URL.Path = "/v1/responses"
				r.Out.URL.RawPath = ""
			}
			r.Out.RequestURI = ""
			r.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ModifyResponse: func(r *http.Response) error {
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Errorf("Failed to proxy OpenResponses chat: %v", err)
			http.Error(w, "Gateway unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	return proxy
}

// handleOpenResponsesChat proxies chat requests to the gateway OpenResponses endpoint.
//
//	POST /api/openresponses/chat
func (h *Handler) handleOpenResponsesChat(w http.ResponseWriter, r *http.Request) {
	// Always reload token from config on each request so that
	// changes to .security.yml are picked up without restarting.
	gateway.mu.Lock()
	refreshOpenResponsesTokensLocked(h.configPath)
	token := gateway.openResponsesToken
	gateway.mu.Unlock()

	if token == "" {
		http.Error(w, `{"error":"OpenResponses channel not configured"}`, http.StatusServiceUnavailable)
		return
	}

	// Clear any pre-set Content-Type (e.g. from JSONContentType middleware)
	// so ReverseProxy can set the correct upstream Content-Type.
	w.Header().Del("Content-Type")
	h.createOpenResponsesProxy(token).ServeHTTP(w, r)
}

// handleGetOpenResponsesToken returns the current token and endpoint URL for the frontend.
//
//	GET /api/openresponses/token
func (h *Handler) handleGetOpenResponsesToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	endpointURL := h.buildOpenResponsesURL(r)

	w.Header().Set("Content-Type", "application/json")
	bc := cfg.Channels.GetByType(config.ChannelOpenResponses)
	var orCfg config.OpenResponsesSettings
	if bc != nil {
		bc.Decode(&orCfg)
	}
	enabled := false
	if bc != nil {
		enabled = bc.Enabled
	}
	json.NewEncoder(w).Encode(map[string]any{
		"token":        orCfg.Token.String(),
		"endpoint_url": endpointURL,
		"enabled":      enabled,
	})
}

// handleRegenOpenResponsesToken generates a new OpenResponses token and saves it.
//
//	POST /api/openresponses/token
func (h *Handler) handleRegenOpenResponsesToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	token := generateSecureToken()
	if bc := cfg.Channels.GetByType(config.ChannelOpenResponses); bc != nil {
		decoded, err := bc.GetDecoded()
		if err == nil && decoded != nil {
			if settings, ok := decoded.(*config.OpenResponsesSettings); ok {
				settings.Token = *config.NewSecureString(token)
			}
		}
	}

	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	// Refresh cached token.
	gateway.mu.Lock()
	gateway.openResponsesToken = token
	gateway.mu.Unlock()

	endpointURL := h.buildOpenResponsesURL(r)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":        token,
		"endpoint_url": endpointURL,
	})
}

// EnsureOpenResponsesChannel enables the OpenResponses channel with sane defaults if it isn't
// already configured. Returns true when the config was modified.
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

// handleOpenResponsesSetup automatically configures everything needed for the OpenResponses Channel to work.
//
//	POST /api/openresponses/setup
func (h *Handler) handleOpenResponsesSetup(w http.ResponseWriter, r *http.Request) {
	changed, err := h.EnsureOpenResponsesChannel()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reload config (EnsureOpenResponsesChannel may have modified it) and refresh cache.
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if changed {
		refreshOpenResponsesToken(cfg)
	}

	endpointURL := h.buildOpenResponsesURL(r)

	var orCfg2 config.OpenResponsesSettings
	if bc := cfg.Channels.GetByType(config.ChannelOpenResponses); bc != nil {
		if decoded, err := bc.GetDecoded(); err == nil && decoded != nil {
			orCfg2 = *decoded.(*config.OpenResponsesSettings)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":        orCfg2.Token.String(),
		"endpoint_url": endpointURL,
		"enabled":      true,
		"changed":      changed,
	})
}

// buildOpenResponsesURL returns the client-visible URL for the OpenResponses endpoint.
func (h *Handler) buildOpenResponsesURL(r *http.Request) string {
	return requestHTTPScheme(r) + "://" + h.picoWebUIAddr(r) + "/api/openresponses/chat"
}
