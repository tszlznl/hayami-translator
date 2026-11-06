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
