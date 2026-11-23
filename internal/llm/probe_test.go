package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hayami/internal/config"
)

func TestProbeProviderOpenAIAzureBaidu(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			if r.Header.Get("Authorization") == "Bearer bad-key" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o-mini"}]}`)
		case r.URL.Path == "/v1/chat/completions":
			if r.Header.Get("Authorization") == "Bearer bad-key" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
		case r.URL.Path == "/translate":
			if r.Header.Get("Ocp-Apim-Subscription-Key") != "azure-ok" {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":{"message":"invalid subscription key"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"translations":[{"text":"ok"}]}]`)
		case r.URL.Path == "/baidu":
			w.Header().Set("Content-Type", "application/json")
			if err := r.ParseForm(); err == nil && r.Form.Get("appid") == "bad-app" {
				fmt.Fprint(w, `{"error_code":"54001","error_msg":"Invalid Sign"}`)
				return
			}
			fmt.Fprint(w, `{"trans_result":[{"dst":"ok"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := config.Default()

	// 1. OpenAI success with Vision enabled.
	rep := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:    config.ProviderOpenAI,
		BaseURL: srv.URL + "/v1",
		Model:   "gpt-4o-mini",
		Vision:  true,
	}, "good-key")
	if !rep.OK || !strings.Contains(rep.Summary, "视觉") {
		t.Fatalf("expected OpenAI probe success with vision, got %+v", rep)
	}

	// 2. OpenAI model missing from /models list -> 404 report.
	repMissing := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:    config.ProviderOpenAI,
		BaseURL: srv.URL + "/v1",
		Model:   "missing-model",
	}, "good-key")
	if repMissing.OK || !strings.Contains(repMissing.ErrTitle, "404") {
		t.Fatalf("expected model 404 probe failure, got %+v", repMissing)
	}

	// 3. OpenAI 401 auth failure.
	rep401 := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:    config.ProviderOpenAI,
		BaseURL: srv.URL + "/v1",
		Model:   "gpt-4o-mini",
	}, "bad-key")
	if rep401.OK || !strings.Contains(rep401.ErrTitle, "鉴权") {
		t.Fatalf("expected 401 auth probe failure, got %+v", rep401)
	}

	// 4. Azure success and 401 failure.
	repAz := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:     config.ProviderAzure,
		Endpoint: srv.URL + "/translate",
	}, "azure-ok")
	if !repAz.OK {
		t.Fatalf("expected Azure probe success, got %+v", repAz)
	}
	repAzFail := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:     config.ProviderAzure,
		Endpoint: srv.URL + "/translate",
	}, "wrong")
	if repAzFail.OK || !strings.Contains(repAzFail.ErrTitle, "401") {
		t.Fatalf("expected Azure 401 failure, got %+v", repAzFail)
	}

	// 5. Baidu success and 54001 failure.
	repBd := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:     config.ProviderBaidu,
		Endpoint: srv.URL + "/baidu",
		AppID:    "good-app",
	}, "sec")
	if !repBd.OK {
		t.Fatalf("expected Baidu probe success, got %+v", repBd)
	}
	repBdFail := ProbeProvider(context.Background(), cfg, config.Provider{
		Kind:     config.ProviderBaidu,
		Endpoint: srv.URL + "/baidu",
		AppID:    "bad-app",
	}, "sec")
	if repBdFail.OK || !strings.Contains(repBdFail.ErrTitle, "54001") {
		t.Fatalf("expected Baidu 54001 failure, got %+v", repBdFail)
	}
}
