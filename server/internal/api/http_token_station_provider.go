package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const tokenStationProviderTimeout = 15 * time.Second

type ompModelsDocument struct {
	Providers map[string]ompProviderEntry `yaml:"providers"`
}

type ompProviderEntry struct {
	BaseURL string `yaml:"baseUrl"`
	APIKey  string `yaml:"apiKey"`
}

type ompProviderUsage struct {
	Balance   *float64 `json:"balance"`
	Remaining *float64 `json:"remaining"`
	Unit      string   `json:"unit"`
	PlanName  string   `json:"planName"`
	IsValid   *bool    `json:"isValid"`
	Usage     struct {
		Total struct {
			Cost       *float64 `json:"cost"`
			ActualCost *float64 `json:"actual_cost"`
		} `json:"total"`
	} `json:"usage"`
}

func (h *HTTPHandler) tokenStationProvider() string {
	if h == nil || h.AppContext == nil {
		return ""
	}
	pool := h.AppContext.GetAgentPool()
	if pool == nil {
		return ""
	}
	return strings.TrimSpace(pool.Config().TokenStationProvider)
}

// handleTokenStationProviderUserInfo serves the bottom-left balance card from a
// provider configured in OMP's models.yml. Hualong (and compatible gateways)
// expose the remaining wallet balance on GET /v1/usage using the provider key.
func (h *HTTPHandler) handleTokenStationProviderUserInfo(w http.ResponseWriter, r *http.Request, providerName string) {
	data := map[string]any{
		"provider_mode":   true,
		"provider_name":   providerName,
		"apply_supported": false,
	}
	provider, err := readOMPProvider(providerName)
	if err == nil {
		if topUpURL := providerTopUpURL(provider.BaseURL); topUpURL != "" {
			data["topup_url"] = topUpURL
		}
	}
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
			"data":    data,
		})
		return
	}

	usage, err := fetchOMPProviderUsage(r.Context(), provider)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": err.Error(),
			"data":    data,
		})
		return
	}
	balance, ok := usage.availableBalance()
	if !ok {
		respondJSON(w, http.StatusOK, map[string]any{
			"success": false,
			"message": "provider usage response has no balance",
			"data":    data,
		})
		return
	}
	unit := strings.TrimSpace(usage.Unit)
	balanceText := formatTokenStationMoney(balance, unit)
	data["balance"] = balance
	data["balance_text"] = balanceText
	data["quota_display_text"] = balanceText
	if unit != "" {
		data["unit"] = unit
	}
	if usage.PlanName != "" {
		data["plan_name"] = usage.PlanName
	}
	if usage.IsValid != nil {
		data["is_valid"] = *usage.IsValid
	}
	if usage.Usage.Total.ActualCost != nil {
		data["used_quota"] = *usage.Usage.Total.ActualCost
		data["used_quota_text"] = formatTokenStationMoney(*usage.Usage.Total.ActualCost, unit)
	} else if usage.Usage.Total.Cost != nil {
		data["used_quota"] = *usage.Usage.Total.Cost
		data["used_quota_text"] = formatTokenStationMoney(*usage.Usage.Total.Cost, unit)
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    data,
	})
}

func readOMPProvider(name string) (ompProviderEntry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ompProviderEntry{}, errors.New("token station provider not configured")
	}
	path, err := homePath(".omp", "agent", "models.yml")
	if err != nil {
		return ompProviderEntry{}, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ompProviderEntry{}, fmt.Errorf("OMP models.yml not found")
		}
		return ompProviderEntry{}, err
	}
	var doc ompModelsDocument
	if err := yaml.Unmarshal(payload, &doc); err != nil {
		return ompProviderEntry{}, fmt.Errorf("parse OMP models.yml: %w", err)
	}
	for key, provider := range doc.Providers {
		if !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		provider.APIKey = strings.TrimSpace(provider.APIKey)
		if provider.BaseURL == "" {
			return ompProviderEntry{}, fmt.Errorf("OMP provider %q has no baseUrl", name)
		}
		if provider.APIKey == "" {
			return ompProviderEntry{}, fmt.Errorf("OMP provider %q has no apiKey", name)
		}
		return provider, nil
	}
	return ompProviderEntry{}, fmt.Errorf("OMP provider %q not found", name)
}

func fetchOMPProviderUsage(ctx context.Context, provider ompProviderEntry) (*ompProviderUsage, error) {
	endpoint := openAIModelsBaseURL(provider.BaseURL) + "/usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: tokenStationProviderTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		var payload struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(body, &payload) == nil {
			if payload.Message != "" {
				message = payload.Message
			} else if payload.Error != "" {
				message = payload.Error
			}
		}
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("provider usage failed: %s", message)
	}
	var usage ompProviderUsage
	if err := json.Unmarshal(body, &usage); err != nil {
		return nil, fmt.Errorf("provider usage returned invalid payload")
	}
	return &usage, nil
}

func (u *ompProviderUsage) availableBalance() (float64, bool) {
	if u == nil {
		return 0, false
	}
	if u.Balance != nil {
		return *u.Balance, true
	}
	if u.Remaining != nil {
		return *u.Remaining, true
	}
	return 0, false
}

func providerTopUpURL(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/purchase"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func formatTokenStationMoney(value float64, unit string) string {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "USD", "US$", "$":
		return fmt.Sprintf("$%.2f", value)
	case "CNY", "RMB", "¥":
		return fmt.Sprintf("¥%.2f", value)
	default:
		if unit == "" {
			return fmt.Sprintf("%.2f", value)
		}
		return fmt.Sprintf("%.2f %s", value, unit)
	}
}
