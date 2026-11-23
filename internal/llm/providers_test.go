package llm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hayami/internal/config"
)

func TestAzureProvider(t *testing.T) {
	var gotTo, gotKey, gotRegion, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTo = r.URL.Query().Get("to")
		if r.URL.Query().Get("api-version") != "3.0" {
			t.Errorf("api-version = %q", r.URL.Query().Get("api-version"))
		}
		gotKey = r.Header.Get("Ocp-Apim-Subscription-Key")
		gotRegion = r.Header.Get("Ocp-Apim-Subscription-Region")
		body, _ := io.ReadAll(r.Body)
		var in []struct {
			Text string `json:"Text"`
		}
		if err := json.Unmarshal(body, &in); err != nil || len(in) != 1 {
			t.Errorf("body %s err %v", body, err)
		} else {
			gotText = in[0].Text
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"translations":[{"text":"你好","to":"zh-Hans"}]}]`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderAzure, Endpoint: srv.URL, Region: "eastasia"}
	var out strings.Builder
	err := translateAzure(context.Background(), "简体中文", p, "azure-key", "hello", func(s string) {
		out.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "你好" {
		t.Errorf("result %q", out.String())
	}
	if gotTo != "zh-Hans" || gotKey != "azure-key" || gotRegion != "eastasia" || gotText != "hello" {
		t.Errorf("request to=%q key=%q region=%q text=%q", gotTo, gotKey, gotRegion, gotText)
	}
}

func TestBaiduProvider(t *testing.T) {
	var form map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		form = map[string]string{}
		for k := range r.PostForm {
			form[k] = r.PostForm.Get(k)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"trans_result":[{"src":"hello","dst":"你好"}]}`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderBaidu, Endpoint: srv.URL, AppID: "appid"}
	var out strings.Builder
	err := translateBaidu(context.Background(), "简体中文", p, "secret", "hello", func(s string) {
		out.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "你好" {
		t.Errorf("result %q", out.String())
	}
	sum := md5.Sum([]byte("appid" + "hello" + form["salt"] + "secret"))
	wantSign := hex.EncodeToString(sum[:])
	if form["q"] != "hello" || form["from"] != "auto" || form["to"] != "zh" ||
		form["appid"] != "appid" || form["salt"] == "" || form["sign"] != wantSign {
		t.Errorf("form %#v sign want %s", form, wantSign)
	}
}

func TestBaiduProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error_code":"54001","error_msg":"Invalid Sign"}`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderBaidu, Endpoint: srv.URL, AppID: "appid"}
	err := translateBaidu(context.Background(), "English", p, "secret", "hello", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "54001") || !strings.Contains(err.Error(), "Invalid Sign") {
		t.Fatalf("error %v", err)
	}
}

func TestBaiduNumericErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error_code":54001,"error_msg":"Invalid Sign"}`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderBaidu, Endpoint: srv.URL, AppID: "appid"}
	err := translateBaidu(context.Background(), "English", p, "secret", "hello", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "54001") || !strings.Contains(err.Error(), "Invalid Sign") {
		t.Fatalf("error %v", err)
	}
}

func TestBaiduSuccessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error_code":52000,"error_msg":"Success","trans_result":[{"src":"hello","dst":"你好"}]}`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderBaidu, Endpoint: srv.URL, AppID: "appid"}
	var out strings.Builder
	err := translateBaidu(context.Background(), "简体中文", p, "secret", "hello", func(s string) {
		out.WriteString(s)
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "你好" {
		t.Fatalf("result %q", out.String())
	}
}

func TestAzureAndBaiduExplicitSourceLanguage(t *testing.T) {
	var azureFrom string
	azureSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		azureFrom = r.URL.Query().Get("from")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"translations":[{"text":"你好"}]}]`)
	}))
	defer azureSrv.Close()

	err := translateAzureWithSource(context.Background(), "English", "简体中文", config.Provider{Kind: config.ProviderAzure, Endpoint: azureSrv.URL}, "k", "hello", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if azureFrom != "en" {
		t.Fatalf("azure from = %q, want en", azureFrom)
	}

	var baiduFrom string
	baiduSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		baiduFrom = r.PostForm.Get("from")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"trans_result":[{"dst":"你好"}]}`)
	}))
	defer baiduSrv.Close()

	err = translateBaiduWithSource(context.Background(), "English", "简体中文", config.Provider{Kind: config.ProviderBaidu, Endpoint: baiduSrv.URL, AppID: "app"}, "k", "hello", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if baiduFrom != "en" {
		t.Fatalf("baidu from = %q, want en", baiduFrom)
	}
}

func TestBaiduProviderTrimsAppIDAndSecret(t *testing.T) {
	var gotAppID, gotSalt, gotSign string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotAppID = r.PostForm.Get("appid")
		gotSalt = r.PostForm.Get("salt")
		gotSign = r.PostForm.Get("sign")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"trans_result":[{"dst":"你好"}]}`)
	}))
	defer srv.Close()

	p := config.Provider{Kind: config.ProviderBaidu, Endpoint: srv.URL, AppID: "  appid-trim \n"}
	var out strings.Builder
	if err := translateBaiduWithSource(context.Background(), "", "简体中文", p, "  secret-trim\t", "hello", func(s string) {
		out.WriteString(s)
	}); err != nil {
		t.Fatal(err)
	}
	if gotAppID != "appid-trim" {
		t.Fatalf("appid = %q, want trimmed", gotAppID)
	}
	sum := md5.Sum([]byte("appid-trim" + "hello" + gotSalt + "secret-trim"))
	if want := hex.EncodeToString(sum[:]); gotSign != want {
		t.Fatalf("sign = %q, want %q (trimmed credentials)", gotSign, want)
	}
}

func TestRedirectStripsAuthorizationOnCrossHostAndDowngrade(t *testing.T) {
	// Cross-host redirect must strip Authorization header.
	var targetAuth string
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "TEXT/EVENT-STREAM; charset=utf-8")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer targetSrv.Close()

	redirSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetSrv.URL+"/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer redirSrv.Close()

	cfg := config.Default()
	p := config.Provider{Kind: config.ProviderOpenAI, BaseURL: redirSrv.URL, Model: "gpt-4o-mini"}
	var out strings.Builder
	if err := StreamOpenAI(context.Background(), cfg, p, "secret-token", []Message{{Role: "user", Content: "hi"}}, func(s string) {
		out.WriteString(s)
	}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "ok" {
		t.Fatalf("streamed = %q, want ok", out.String())
	}
	if targetAuth != "" {
		t.Fatalf("Authorization leaked across redirect hosts: %q", targetAuth)
	}

	// Verify HTTPS-to-HTTP downgrade on the same host strips sensitive headers.
	prevReq, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	nextReq, _ := http.NewRequest(http.MethodPost, "http://api.example.com/v1/chat/completions", nil)
	nextReq.Header.Set("Authorization", "Bearer top-secret")
	nextReq.Header.Set("Ocp-Apim-Subscription-Key", "azure-secret")
	if err := httpClient.CheckRedirect(nextReq, []*http.Request{prevReq}); err != nil {
		t.Fatal(err)
	}
	if nextReq.Header.Get("Authorization") != "" || nextReq.Header.Get("Ocp-Apim-Subscription-Key") != "" {
		t.Fatalf("credentials were not stripped on HTTPS->HTTP downgrade: %v", nextReq.Header)
	}
}
