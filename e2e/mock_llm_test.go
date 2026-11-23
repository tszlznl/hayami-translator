//go:build e2e

package e2e_test

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type RecordedImage struct {
	DeclaredMIME string
	ActualMIME   string
	Width        int
	Height       int
	RawBytes     int
	DataURI      string
}

type MockMessage struct {
	Role       string
	Text       string
	RawContent json.RawMessage
}

type RecordedRequest struct {
	Method    string
	Path      string
	RawQuery  string
	Headers   http.Header
	Body      []byte
	Form      url.Values
	Model     string
	Stream    bool
	Messages  []MockMessage
	Images    []RecordedImage
	Timestamp time.Time
}

type MockResponse struct {
	Status         int
	ContentType    string
	Body           string
	RawBody        []byte
	Chunks         []string
	TruncateSSE    bool
	SSEError       string
	Delay          time.Duration
	BaiduErrorCode string
	BaiduErrorMsg  string
	Models         []string
}

type MockLLMServer struct {
	Server              *httptest.Server
	mu                  sync.Mutex
	requests            []RecordedRequest
	nextQueue           []MockResponse
	routeDefaults       map[string]MockResponse
	expectedBaiduSecret string
}

func newMockLLMServer(t *testing.T) *MockLLMServer {
	t.Helper()
	m := &MockLLMServer{
		routeDefaults: map[string]MockResponse{
			"chat": {
				Status: http.StatusOK,
				Body:   "mock translation",
			},
			"models": {
				Status: http.StatusOK,
				Models: []string{"gpt-4o-mini", "gpt-4o"},
			},
			"speech": {
				Status: http.StatusOK,
			},
			"azure": {
				Status: http.StatusOK,
				Body:   "azure mock translation",
			},
			"baidu": {
				Status: http.StatusOK,
				Body:   "baidu mock translation",
			},
		},
	}
	m.Server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *MockLLMServer) URL() string {
	return m.Server.URL
}

func (m *MockLLMServer) V1URL() string {
	return m.Server.URL + "/v1"
}

func (m *MockLLMServer) SetDefault(route string, resp MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routeDefaults[route] = resp
}

func (m *MockLLMServer) QueueResponse(resp ...MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextQueue = append(m.nextQueue, resp...)
}

func (m *MockLLMServer) SetExpectedBaiduSecret(secret string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expectedBaiduSecret = secret
}

func (m *MockLLMServer) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RecordedRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *MockLLMServer) ClearRequests() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = nil
}

func (m *MockLLMServer) popResponse(route string) (MockResponse, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var resp MockResponse
	if len(m.nextQueue) > 0 && route != "speech" && route != "models" {
		resp = m.nextQueue[0]
		m.nextQueue = m.nextQueue[1:]
	} else {
		resp = m.routeDefaults[route]
	}
	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	return resp, m.expectedBaiduSecret
}

func (m *MockLLMServer) record(req RecordedRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
}

func (m *MockLLMServer) handle(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	rec := RecordedRequest{
		Method:    r.Method,
		Path:      r.URL.Path,
		RawQuery:  r.URL.RawQuery,
		Headers:   r.Header.Clone(),
		Body:      bodyBytes,
		Timestamp: time.Now(),
	}

	route := classifyRoute(r.Method, r.URL.Path)
	switch route {
	case "chat":
		parseChatPayload(bodyBytes, &rec)
	case "baidu":
		if form, err := url.ParseQuery(string(bodyBytes)); err == nil {
			rec.Form = form
		}
	}
	m.record(rec)

	resp, baiduSecret := m.popResponse(route)
	if resp.Delay > 0 {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(resp.Delay):
		}
	}

	switch route {
	case "chat":
		m.writeChatResponse(w, rec, resp)
	case "models":
		m.writeModelsResponse(w, r.URL.Path, resp)
	case "speech":
		m.writeSpeechResponse(w, bodyBytes, resp)
	case "azure":
		m.writeAzureResponse(w, resp)
	case "baidu":
		m.writeBaiduResponse(w, rec.Form, resp, baiduSecret)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown mock endpoint: "+r.URL.Path)
	}
}

func classifyRoute(method, path string) string {
	switch {
	case path == "/v1/chat/completions" || path == "/chat/completions":
		return "chat"
	case path == "/v1/models" || path == "/models" ||
		strings.HasPrefix(path, "/v1/models/") || strings.HasPrefix(path, "/models/"):
		return "models"
	case path == "/v1/audio/speech" || path == "/audio/speech":
		return "speech"
	case path == "/translate" || strings.HasSuffix(path, "/translate") && !strings.Contains(path, "/vip/translate"):
		return "azure"
	case path == "/baidu" || path == "/api/trans/vip/translate":
		return "baidu"
	default:
		if method == http.MethodGet && strings.Contains(path, "/models") {
			return "models"
		}
		return "unknown"
	}
}

func parseChatPayload(body []byte, rec *RecordedRequest) {
	var payload struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	rec.Model = payload.Model
	rec.Stream = payload.Stream
	for _, msg := range payload.Messages {
		mm := MockMessage{
			Role:       msg.Role,
			RawContent: msg.Content,
		}
		var text string
		if err := json.Unmarshal(msg.Content, &text); err == nil {
			mm.Text = text
		} else {
			var parts []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if err := json.Unmarshal(msg.Content, &parts); err == nil {
				var texts []string
				for _, part := range parts {
					if part.Type == "text" && part.Text != "" {
						texts = append(texts, part.Text)
					}
					if part.Type == "image_url" && part.ImageURL != nil {
						if img, ok := decodeDataURIImage(part.ImageURL.URL); ok {
							rec.Images = append(rec.Images, img)
						}
					}
				}
				mm.Text = strings.Join(texts, "\n")
			}
		}
		rec.Messages = append(rec.Messages, mm)
	}
}

func decodeDataURIImage(uri string) (RecordedImage, bool) {
	if !strings.HasPrefix(uri, "data:") {
		return RecordedImage{}, false
	}
	rest := strings.TrimPrefix(uri, "data:")
	meta, b64, ok := strings.Cut(rest, ",")
	if !ok {
		return RecordedImage{}, false
	}
	declaredMIME := strings.TrimSuffix(meta, ";base64")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return RecordedImage{
			DeclaredMIME: declaredMIME,
			DataURI:      uri,
		}, true
	}
	actualMIME, w, h := inspectImageBytes(raw)
	return RecordedImage{
		DeclaredMIME: declaredMIME,
		ActualMIME:   actualMIME,
		Width:        w,
		Height:       h,
		RawBytes:     len(raw),
		DataURI:      uri,
	}, true
}

func inspectImageBytes(raw []byte) (mime string, width, height int) {
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		switch format {
		case "png":
			return "image/png", cfg.Width, cfg.Height
		case "jpeg":
			return "image/jpeg", cfg.Width, cfg.Height
		default:
			return "image/" + format, cfg.Width, cfg.Height
		}
	}
	if w, h, ok := parseWebPDimensions(raw); ok {
		return "image/webp", w, h
	}
	return "", 0, 0
}

func parseWebPDimensions(raw []byte) (int, int, bool) {
	if len(raw) < 25 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(raw[12:16])
	switch chunk {
	case "VP8 ":
		if len(raw) >= 30 && raw[23] == 0x9d && raw[24] == 0x01 && raw[25] == 0x2a {
			w := int(binary.LittleEndian.Uint16(raw[26:28]) & 0x3fff)
			h := int(binary.LittleEndian.Uint16(raw[28:30]) & 0x3fff)
			return w, h, true
		}
	case "VP8L":
		if len(raw) >= 25 && raw[20] == 0x2f {
			bits := binary.LittleEndian.Uint32(raw[21:25])
			w := int(bits&0x3fff) + 1
			h := int((bits>>14)&0x3fff) + 1
			return w, h, true
		}
	case "VP8X":
		if len(raw) >= 30 {
			w := int(uint32(raw[24])|uint32(raw[25])<<8|uint32(raw[26])<<16) + 1
			h := int(uint32(raw[27])|uint32(raw[28])<<8|uint32(raw[29])<<16) + 1
			return w, h, true
		}
	}
	return 0, 0, false
}

func (m *MockLLMServer) writeChatResponse(w http.ResponseWriter, rec RecordedRequest, resp MockResponse) {
	if resp.Status != http.StatusOK {
		msg := resp.Body
		if msg == "" {
			msg = http.StatusText(resp.Status)
		}
		writeJSONError(w, resp.Status, msg)
		return
	}

	if resp.RawBody != nil {
		ct := resp.ContentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.RawBody)
		return
	}

	forceJSON := strings.Contains(resp.ContentType, "application/json")
	if !rec.Stream || forceJSON {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "mock-chat-1",
			"object": "chat.completion",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": resp.Body,
					},
					"finish_reason": "stop",
				},
			},
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	if resp.SSEError != "" {
		payload, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": resp.SSEError},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	chunks := resp.Chunks
	if len(chunks) == 0 && resp.Body != "" {
		chunks = []string{resp.Body}
	}
	for _, chunk := range chunks {
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{
					"index": 0,
					"delta": map[string]any{"content": chunk},
				},
			},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if resp.TruncateSSE {
		// Deliberately close without finish_reason or [DONE].
		return
	}

	_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (m *MockLLMServer) writeModelsResponse(w http.ResponseWriter, path string, resp MockResponse) {
	if resp.Status != http.StatusOK {
		msg := resp.Body
		if msg == "" {
			msg = http.StatusText(resp.Status)
		}
		writeJSONError(w, resp.Status, msg)
		return
	}
	models := resp.Models
	if len(models) == 0 {
		models = []string{"gpt-4o-mini"}
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(path, "/v1/models/") || strings.HasPrefix(path, "/models/") {
		id := strings.TrimPrefix(path, "/v1/models/")
		id = strings.TrimPrefix(id, "/models/")
		found := false
		for _, mod := range models {
			if mod == id {
				found = true
				break
			}
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "model not found: "+id)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     id,
			"object": "model",
		})
		return
	}

	data := make([]map[string]any, 0, len(models))
	for _, mod := range models {
		data = append(data, map[string]any{
			"id":     mod,
			"object": "model",
		})
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (m *MockLLMServer) writeSpeechResponse(w http.ResponseWriter, bodyBytes []byte, resp MockResponse) {
	if resp.Status != http.StatusOK {
		msg := resp.Body
		if msg == "" {
			msg = http.StatusText(resp.Status)
		}
		writeJSONError(w, resp.Status, msg)
		return
	}
	if len(resp.RawBody) > 0 {
		ct := resp.ContentType
		if ct == "" {
			ct = "audio/mpeg"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.RawBody)
		return
	}
	var req struct {
		ResponseFormat string `json:"response_format"`
	}
	_ = json.Unmarshal(bodyBytes, &req)
	if strings.EqualFold(req.ResponseFormat, "wav") {
		w.Header().Set("Content-Type", "audio/wav")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(makeWAV(100 * time.Millisecond))
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(makeMP3(4))
}

func (m *MockLLMServer) writeAzureResponse(w http.ResponseWriter, resp MockResponse) {
	if resp.Status != http.StatusOK {
		msg := resp.Body
		if msg == "" {
			msg = http.StatusText(resp.Status)
		}
		writeJSONError(w, resp.Status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode([]map[string]any{
		{
			"translations": []map[string]any{
				{"text": resp.Body, "to": "en"},
			},
		},
	})
}

func (m *MockLLMServer) writeBaiduResponse(w http.ResponseWriter, form url.Values, resp MockResponse, secret string) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Status != http.StatusOK {
		w.WriteHeader(resp.Status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error_code": "52002",
			"error_msg":  resp.Body,
		})
		return
	}
	if resp.BaiduErrorCode != "" && resp.BaiduErrorCode != "0" && resp.BaiduErrorCode != "52000" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error_code": resp.BaiduErrorCode,
			"error_msg":  resp.BaiduErrorMsg,
		})
		return
	}
	if secret != "" {
		appID := form.Get("appid")
		q := form.Get("q")
		salt := form.Get("salt")
		gotSign := form.Get("sign")
		sum := md5.Sum([]byte(appID + q + salt + secret))
		wantSign := hex.EncodeToString(sum[:])
		if gotSign != wantSign {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error_code": "54001",
				"error_msg":  "Invalid Sign",
			})
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"from": form.Get("from"),
		"to":   form.Get("to"),
		"trans_result": []map[string]any{
			{"src": form.Get("q"), "dst": resp.Body},
		},
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
		},
	})
}

func TestHarness_MockLLM(t *testing.T) {
	m := newMockLLMServer(t)

	// 1. Inspect PNG, JPEG, and WebP VP8L dimensions via image data URI extractor.
	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(makePNG(t, 120, 80))
	var jpgBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 90, 45))
	img.SetRGBA(1, 1, color.RGBA{R: 255, A: 255})
	if err := jpeg.Encode(&jpgBuf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	jpgURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpgBuf.Bytes())

	// Minimal 25-byte RIFF WEBP VP8L header (width=320, height=240).
	webp := make([]byte, 25)
	copy(webp[0:4], "RIFF")
	binary.LittleEndian.PutUint32(webp[4:8], 17)
	copy(webp[8:12], "WEBP")
	copy(webp[12:16], "VP8L")
	binary.LittleEndian.PutUint32(webp[16:20], 5)
	webp[20] = 0x2f
	bits := uint32((320 - 1)) | (uint32(240-1) << 14)
	binary.LittleEndian.PutUint32(webp[21:25], bits)
	webpURI := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(webp)

	m.QueueResponse(MockResponse{Status: http.StatusOK, Chunks: []string{"part1 ", "part2"}})
	chatReq := fmt.Sprintf(`{
		"model": "gpt-4o",
		"stream": true,
		"messages": [
			{"role": "system", "content": "sys"},
			{"role": "user", "content": [
				{"type": "text", "text": "describe"},
				{"type": "image_url", "image_url": {"url": %q}},
				{"type": "image_url", "image_url": {"url": %q}},
				{"type": "image_url", "image_url": {"url": %q}}
			]}
		]
	}`, pngURI, jpgURI, webpURI)

	res, err := http.Post(m.V1URL()+"/chat/completions", "application/json", strings.NewReader(chatReq))
	if err != nil {
		t.Fatalf("POST chat/completions: %v", err)
	}
	sseBytes, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(sseBytes), "part1 ") || !strings.Contains(string(sseBytes), "data: [DONE]") {
		t.Fatalf("unexpected SSE response status=%d body=%q", res.StatusCode, string(sseBytes))
	}

	reqs := m.Requests()
	if len(reqs) != 1 || len(reqs[0].Images) != 3 {
		t.Fatalf("expected 1 recorded request with 3 images, got %#v", reqs)
	}
	if reqs[0].Images[0].ActualMIME != "image/png" || reqs[0].Images[0].Width != 120 || reqs[0].Images[0].Height != 80 {
		t.Fatalf("unexpected PNG image metadata: %+v", reqs[0].Images[0])
	}
	if reqs[0].Images[1].ActualMIME != "image/jpeg" || reqs[0].Images[1].Width != 90 || reqs[0].Images[1].Height != 45 {
		t.Fatalf("unexpected JPEG image metadata: %+v", reqs[0].Images[1])
	}
	if reqs[0].Images[2].ActualMIME != "image/webp" || reqs[0].Images[2].Width != 320 || reqs[0].Images[2].Height != 240 {
		t.Fatalf("unexpected WebP image metadata: %+v", reqs[0].Images[2])
	}

	// 2. Verify /v1/models and /v1/audio/speech endpoints.
	modelsRes, err := http.Get(m.V1URL() + "/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	modelsBody, _ := io.ReadAll(modelsRes.Body)
	_ = modelsRes.Body.Close()
	if modelsRes.StatusCode != http.StatusOK || !strings.Contains(string(modelsBody), "gpt-4o-mini") {
		t.Fatalf("unexpected /v1/models response: status=%d body=%q", modelsRes.StatusCode, string(modelsBody))
	}

	speechRes, err := http.Post(m.V1URL()+"/audio/speech", "application/json", strings.NewReader(`{"model":"tts-1","input":"hi","voice":"alloy","response_format":"mp3"}`))
	if err != nil {
		t.Fatalf("POST /v1/audio/speech: %v", err)
	}
	speechBody, _ := io.ReadAll(speechRes.Body)
	_ = speechRes.Body.Close()
	if speechRes.StatusCode != http.StatusOK || len(speechBody) < 104 || speechBody[0] != 0xff {
		t.Fatalf("unexpected /v1/audio/speech response: status=%d len=%d", speechRes.StatusCode, len(speechBody))
	}

	// 3. Verify Azure and Baidu (with MD5 signature) endpoints.
	azureRes, err := http.Post(m.URL()+"/translate?api-version=3.0&to=en", "application/json", strings.NewReader(`[{"Text":"你好"}]`))
	if err != nil {
		t.Fatalf("POST /translate: %v", err)
	}
	azureBody, _ := io.ReadAll(azureRes.Body)
	_ = azureRes.Body.Close()
	if azureRes.StatusCode != http.StatusOK || !strings.Contains(string(azureBody), "azure mock translation") {
		t.Fatalf("unexpected Azure response: status=%d body=%q", azureRes.StatusCode, string(azureBody))
	}

	m.SetExpectedBaiduSecret("secret123")
	sum := md5.Sum([]byte("app1" + "你好" + "42" + "secret123"))
	form := url.Values{
		"q":     {"你好"},
		"from":  {"auto"},
		"to":    {"en"},
		"appid": {"app1"},
		"salt":  {"42"},
		"sign":  {hex.EncodeToString(sum[:])},
	}
	baiduRes, err := http.PostForm(m.URL()+"/baidu", form)
	if err != nil {
		t.Fatalf("POST /baidu: %v", err)
	}
	baiduBody, _ := io.ReadAll(baiduRes.Body)
	_ = baiduRes.Body.Close()
	if baiduRes.StatusCode != http.StatusOK || !strings.Contains(string(baiduBody), "baidu mock translation") {
		t.Fatalf("unexpected Baidu response: status=%d body=%q", baiduRes.StatusCode, string(baiduBody))
	}
}
