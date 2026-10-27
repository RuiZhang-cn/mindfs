package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mindfs/server/internal/testutil"
)

func TestReadOMPProvider(t *testing.T) {
	testutil.IsolateUserDirs(t, t.TempDir())
	path, err := homePath(".omp", "agent", "models.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("providers:\n  hualong:\n    baseUrl: https://api-fast.hualong.online/v1\n    apiKey: sk-test\n")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := readOMPProvider("hualong")
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://api-fast.hualong.online/v1" || provider.APIKey != "sk-test" {
		t.Fatalf("provider = %+v", provider)
	}
	if _, err := readOMPProvider("missing"); err == nil {
		t.Fatal("expected missing provider error")
	}
}

func TestFetchOMPProviderUsage(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":107.889,"remaining":107.889,"unit":"USD","planName":"钱包余额","isValid":true,"usage":{"total":{"actual_cost":5.11}}}`))
	}))
	defer server.Close()

	usage, err := fetchOMPProviderUsage(context.Background(), ompProviderEntry{
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", auth)
	}
	balance, ok := usage.availableBalance()
	if !ok || balance != 107.889 {
		t.Fatalf("balance = %v ok=%v", balance, ok)
	}
	if got := formatTokenStationMoney(balance, usage.Unit); got != "$107.89" {
		t.Fatalf("formatted = %q", got)
	}
}

func TestHandleTokenStationProviderUserInfo(t *testing.T) {
	testutil.IsolateUserDirs(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"balance":107.889,"unit":"USD","planName":"钱包余额","isValid":true}`))
	}))
	defer server.Close()

	path, err := homePath(".omp", "agent", "models.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("providers:\n  hualong:\n    baseUrl: " + server.URL + "/v1\n    apiKey: sk-test\n")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	handler := &HTTPHandler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/token-station/userinfo?purpose=balance", nil)
	handler.handleTokenStationProviderUserInfo(recorder, request, "hualong")

	var response struct {
		Success bool `json:"success"`
		Data    struct {
			BalanceText  string `json:"balance_text"`
			TopUpURL     string `json:"topup_url"`
			ProviderMode bool   `json:"provider_mode"`
			ProviderName string `json:"provider_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success {
		t.Fatalf("response = %s", recorder.Body.String())
	}
	if response.Data.BalanceText != "$107.89" {
		t.Fatalf("balance text = %q", response.Data.BalanceText)
	}
	if response.Data.TopUpURL != server.URL+"/purchase" {
		t.Fatalf("topup url = %q", response.Data.TopUpURL)
	}
	if !response.Data.ProviderMode || response.Data.ProviderName != "hualong" {
		t.Fatalf("provider fields = %+v", response.Data)
	}
}

func TestProviderTopUpURL(t *testing.T) {
	if got := providerTopUpURL("https://api-fast.hualong.online/v1"); got != "https://api-fast.hualong.online/purchase" {
		t.Fatalf("topup url = %q", got)
	}
	if got := providerTopUpURL(""); got != "" {
		t.Fatalf("empty topup url = %q", got)
	}
}
