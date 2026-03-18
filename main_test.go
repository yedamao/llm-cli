package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

import "net/http"

func TestLoadConfigPrefersEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, defaultConfigFileName)
	if err := os.WriteFile(configPath, []byte(`{"BASE_URL":"https://file.example/v1","API_KEY":"file-key","MODEL":"file-model"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("BASE_URL", "https://env.example/v1")
	t.Setenv("API_KEY", "env-key")
	t.Setenv("MODEL", "env-model")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if cfg.BaseURL != "https://env.example/v1" || cfg.APIKey != "env-key" || cfg.Model != "env-model" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestReadPromptFromArgs(t *testing.T) {
	got, err := readPrompt([]string{"hello", "world"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("readPrompt() error = %v", err)
	}
	if got != "hello world" {
		t.Fatalf("readPrompt() = %q", got)
	}
}

func TestStreamSSE(t *testing.T) {
	input := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var output strings.Builder
	if err := streamSSE(strings.NewReader(input), &output); err != nil {
		t.Fatalf("streamSSE() error = %v", err)
	}

	if output.String() != "Hello" {
		t.Fatalf("streamSSE() output = %q", output.String())
	}
}

func TestStreamChatCompletion(t *testing.T) {
	t.Cleanup(func() {
		httpClient = http.DefaultClient
	})

	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}

		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatalf("expected stream=true")
		}
		if req.Model != "test-model" {
			t.Fatalf("model = %q", req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
			t.Fatalf("messages = %+v", req.Messages)
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
					"data: {\"choices\":[{\"delta\":{\"content\":\" there\"}}]}\n\n" +
					"data: [DONE]\n\n",
			)),
		}, nil
	})}

	cfg := Config{
		BaseURL: "https://example.test/v1",
		APIKey:  "test-key",
		Model:   "test-model",
	}

	var output strings.Builder
	if err := streamChatCompletion(context.Background(), cfg, "hello", &output); err != nil {
		t.Fatalf("streamChatCompletion() error = %v", err)
	}

	if output.String() != "Hi there\n" {
		t.Fatalf("output = %q", output.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
