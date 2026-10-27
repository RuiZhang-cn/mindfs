package api

import (
	"net/http"
	"strings"
)

func (h *HTTPHandler) handleTokenStationUserInfo(w http.ResponseWriter, r *http.Request) {
	if providerName := h.tokenStationProvider(); providerName != "" {
		h.handleTokenStationProviderUserInfo(w, r, providerName)
		return
	}
	manager := h.AppContext.GetRelayManager()
	if manager == nil {
		respondError(w, http.StatusServiceUnavailable, errServiceUnavailable("relay manager not configured"))
		return
	}
	purpose := strings.TrimSpace(r.URL.Query().Get("purpose"))
	userInfo, err := manager.TokenStationUserInfo(r.Context(), purpose)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
			"data": map[string]any{
				"topup_url": h.tokenStationURL(),
			},
		})
		return
	}
	data, ok := userInfo["data"].(map[string]any)
	if !ok {
		data = map[string]any{}
		userInfo["data"] = data
	}
	data["topup_url"] = h.tokenStationURL()
	if purpose != "apply" {
		delete(data, "api_keys")
	}
	respondJSON(w, http.StatusOK, userInfo)
}

func (h *HTTPHandler) handleTokenStationBindStart(w http.ResponseWriter, _ *http.Request) {
	if providerName := h.tokenStationProvider(); providerName != "" {
		topUpURL := ""
		if provider, err := readOMPProvider(providerName); err == nil {
			topUpURL = providerTopUpURL(provider.BaseURL)
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"bound":     true,
			"topup_url": topUpURL,
		})
		return
	}
	manager := h.AppContext.GetRelayManager()
	if manager == nil {
		respondError(w, http.StatusServiceUnavailable, errServiceUnavailable("relay manager not configured"))
		return
	}
	status, err := manager.StartTokenStationBinding()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, errServiceUnavailable(err.Error()))
		return
	}
	status.TopUpURL = h.tokenStationURL()
	respondJSON(w, http.StatusOK, status)
}

func (h *HTTPHandler) tokenStationURL() string {
	if h != nil && h.AppContext != nil && h.AppContext.GetAgentPool() != nil {
		cfg := h.AppContext.GetAgentPool().Config()
		if value := strings.TrimSpace(cfg.TokenStationURL); value != "" {
			return value
		}
	}
	return "http://localhost:3000"
}
