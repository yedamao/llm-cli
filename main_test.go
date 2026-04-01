package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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
	if err := streamSSE(strings.NewReader(input), func(chunk string) error {
		output.WriteString(chunk)
		return nil
	}); err != nil {
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
	reply, err := streamChatCompletion(context.Background(), cfg, []chatMessage{
		{Role: "user", Content: "hello"},
	}, func(chunk string) error {
		output.WriteString(chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("streamChatCompletion() error = %v", err)
	}

	if reply != "Hi there" {
		t.Fatalf("reply = %q", reply)
	}
	if output.String() != "Hi there" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestShouldStartLoop(t *testing.T) {
	if shouldStartLoop([]string{"hello"}) {
		t.Fatalf("shouldStartLoop() = true with args")
	}
}

func TestChatModelMaintainsConversation(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(chatModel)

	m.textarea.SetValue("hello")
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(chatModel)
	model, _ = m.Update(streamChunkMsg{content: "First"})
	m = model.(chatModel)
	model, _ = m.Update(streamChunkMsg{content: " reply"})
	m = model.(chatModel)
	model, _ = m.Update(streamDoneMsg{reply: "First reply"})
	m = model.(chatModel)

	if got := m.conversation; !reflect.DeepEqual(got, []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "First reply"},
	}) {
		t.Fatalf("conversation after first reply = %#v", got)
	}

	m.textarea.SetValue("follow up")
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(chatModel)
	model, _ = m.Update(streamChunkMsg{content: "Second"})
	m = model.(chatModel)
	model, _ = m.Update(streamChunkMsg{content: " reply"})
	m = model.(chatModel)
	model, _ = m.Update(streamDoneMsg{reply: "Second reply"})
	m = model.(chatModel)

	wantConversation := []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "First reply"},
		{Role: "user", Content: "follow up"},
		{Role: "assistant", Content: "Second reply"},
	}
	if !reflect.DeepEqual(m.conversation, wantConversation) {
		t.Fatalf("conversation = %#v, want %#v", m.conversation, wantConversation)
	}

	if got := m.transcript[len(m.transcript)-1].content; got != "Second reply" {
		t.Fatalf("final assistant transcript = %q", got)
	}

	if !strings.Contains(m.viewport.View(), "Second reply") {
		t.Fatalf("viewport = %q", m.viewport.View())
	}
}

func TestChatModelAltEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(chatModel)

	m.textarea.SetValue("hello")
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = model.(chatModel)

	if got := m.textarea.Value(); got != "hello\n" {
		t.Fatalf("textarea value = %q", got)
	}
	if m.inFlight {
		t.Fatalf("expected prompt not to submit")
	}
	if len(m.conversation) != 0 {
		t.Fatalf("conversation = %#v", m.conversation)
	}
}

func TestChatModelCtrlDQuits(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if _, ok := model.(chatModel); !ok {
		t.Fatalf("model type = %T", model)
	}
	if cmd == nil {
		t.Fatalf("expected quit command")
	}
	if msg := cmd(); msg != tea.Quit() {
		t.Fatalf("cmd() = %#v", msg)
	}
}

func TestChatModelCtrlTTogglesFullScreenTranscript(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(chatModel)
	normalHeight := m.viewport.Height

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = model.(chatModel)

	if cmd != nil {
		t.Fatalf("expected no command")
	}
	if !m.fullScreenTranscript {
		t.Fatalf("expected fullScreenTranscript to be enabled")
	}
	if m.viewport.Height <= normalHeight {
		t.Fatalf("viewport height = %d, want > %d", m.viewport.Height, normalHeight)
	}
	if !strings.Contains(m.View(), "Ctrl+T exit transcript") {
		t.Fatalf("view = %q", m.View())
	}
}

func TestChatModelEscCancelsInFlightWithoutQuitting(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})

	called := false
	m.inFlight = true
	m.cancel = func() {
		called = true
	}

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next := model.(chatModel)

	if !called {
		t.Fatalf("expected cancel to be called")
	}
	if cmd != nil {
		t.Fatalf("expected no quit command")
	}
	if !next.inFlight {
		t.Fatalf("expected model to remain in flight until stream finishes")
	}
}

func TestChatModelEscRestoresPreviousUserPrompt(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})
	m.conversation = []chatMessage{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "follow up"},
	}
	m.textarea.SetValue("draft")

	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next := model.(chatModel)

	if cmd != nil {
		t.Fatalf("expected no command")
	}
	if got := next.textarea.Value(); got != "follow up" {
		t.Fatalf("textarea value = %q", got)
	}
}

func TestChatModelCanceledStreamDoesNotShowError(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})
	m.inFlight = true
	m.errMsg = "old error"

	model, _ := m.Update(streamErrMsg{err: context.Canceled})
	next := model.(chatModel)

	if next.errMsg != "" {
		t.Fatalf("errMsg = %q", next.errMsg)
	}
	if next.inFlight {
		t.Fatalf("expected inFlight to be false")
	}
}

func TestChatModelFullScreenTranscriptScrollsViewport(t *testing.T) {
	m := newChatModel(Config{Model: "test-model"}, func(context.Context, Config, []chatMessage, func(string) error) (string, error) {
		return "", nil
	})

	for i := 0; i < 40; i++ {
		m.transcript = append(m.transcript, transcriptEntry{role: "assistant", content: strings.Repeat("line ", 4)})
	}

	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	m = model.(chatModel)
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = model.(chatModel)

	start := m.viewport.YOffset
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = model.(chatModel)

	if m.viewport.YOffset >= start {
		t.Fatalf("viewport YOffset = %d, want < %d", m.viewport.YOffset, start)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
