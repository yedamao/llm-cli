package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultConfigFileName = ".llm-cli.json"
	defaultBaseURL        = "https://api.openai.com/v1"
)

var httpClient = http.DefaultClient

type Config struct {
	BaseURL string `json:"BASE_URL"`
	APIKey  string `json:"API_KEY"`
	Model   string `json:"MODEL"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

type streamResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type streamRunner func(context.Context, Config, []chatMessage, func(string) error) (string, error)

type transcriptEntry struct {
	role    string
	content string
}

type streamChunkMsg struct {
	content string
}

type streamDoneMsg struct {
	reply string
}

type streamErrMsg struct {
	err error
}

type chatModel struct {
	cfg          Config
	width        int
	height       int
	ready        bool
	inFlight     bool
	errMsg       string
	conversation []chatMessage
	transcript   []transcriptEntry
	viewport     viewport.Model
	textarea     textarea.Model
	spinner      spinner.Model
	cancel       context.CancelFunc
	streamCh     <-chan tea.Msg
	streamRunner streamRunner
}

var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86")).
			MarginBottom(1)

	userStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39"))

	assistantStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			MarginTop(1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			printUsage()
			return nil
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if shouldStartLoop(os.Args[1:]) {
		return runInteractiveLoop(cfg)
	}

	prompt, err := readPrompt(os.Args[1:], os.Stdin)
	if err != nil {
		return err
	}

	_, err = streamChatCompletion(context.Background(), cfg, []chatMessage{
		{Role: "user", Content: prompt},
	}, func(chunk string) error {
		_, writeErr := io.WriteString(os.Stdout, chunk)
		return writeErr
	})
	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(os.Stdout)
	return err
}

func printUsage() {
	fmt.Println(`llm-cli - call an OpenAI-compatible chat API

Usage:
  llm-cli "your prompt"
  echo "your prompt" | llm-cli
  llm-cli

Configuration:
  Env vars:
    BASE_URL
    API_KEY
    MODEL

  Home config file:
    ~/.llm-cli.json

Environment variables override values from the config file.`)
}

func shouldStartLoop(args []string) bool {
	if len(args) > 0 {
		return false
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func loadConfig() (Config, error) {
	cfg := Config{}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home dir: %w", err)
	}

	configPath := filepath.Join(homeDir, defaultConfigFileName)
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", configPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read config file %s: %w", configPath, err)
	}

	if value := strings.TrimSpace(os.Getenv("BASE_URL")); value != "" {
		cfg.BaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("API_KEY")); value != "" {
		cfg.APIKey = value
	}
	if value := strings.TrimSpace(os.Getenv("MODEL")); value != "" {
		cfg.Model = value
	}

	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	switch {
	case cfg.APIKey == "":
		return Config{}, fmt.Errorf("missing API_KEY in env or %s", configPath)
	case cfg.Model == "":
		return Config{}, fmt.Errorf("missing MODEL in env or %s", configPath)
	}

	return cfg, nil
}

func readPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if prompt != "" {
			return prompt, nil
		}
	}

	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errors.New("missing prompt: pass text as arguments or pipe stdin")
	}

	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", errors.New("prompt is empty")
	}

	return prompt, nil
}

func streamChatCompletion(ctx context.Context, cfg Config, messages []chatMessage, onChunk func(string) error) (string, error) {
	reqBody := chatRequest{
		Model:    cfg.Model,
		Messages: messages,
		Stream:   true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	url := cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read error response: %w", err)
		}

		var payload chatResponse
		if err := json.Unmarshal(respBody, &payload); err == nil {
			if payload.Error != nil && payload.Error.Message != "" {
				return "", fmt.Errorf("api error (%d): %s", resp.StatusCode, payload.Error.Message)
			}
		}

		return "", fmt.Errorf("api error (%d)", resp.StatusCode)
	}

	var captured strings.Builder
	if err := streamSSE(resp.Body, func(chunk string) error {
		captured.WriteString(chunk)
		return onChunk(chunk)
	}); err != nil {
		return "", err
	}

	return captured.String(), nil
}

func streamSSE(input io.Reader, onChunk func(string) error) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var wroteContent bool

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			if !wroteContent {
				return errors.New("api returned no streamed content")
			}
			return nil
		}

		var chunk streamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}

		if chunk.Error != nil && chunk.Error.Message != "" {
			return fmt.Errorf("api error: %s", chunk.Error.Message)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}

		if err := onChunk(content); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		wroteContent = true
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read stream: %w", err)
	}

	if !wroteContent {
		return errors.New("api returned no streamed content")
	}

	return nil
}

func runInteractiveLoop(cfg Config) error {
	p := tea.NewProgram(newChatModel(cfg, streamChatCompletion), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func newChatModel(cfg Config, runner streamRunner) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Ask something..."
	ta.Focus()
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("alt+enter")

	vp := viewport.New(0, 0)
	vp.SetContent("")

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := chatModel{
		cfg:          cfg,
		textarea:     ta,
		viewport:     vp,
		spinner:      sp,
		streamRunner: runner,
		transcript: []transcriptEntry{
			{role: "assistant", content: "Ready. Type a prompt and press Enter. Use Alt+Enter for a newline."},
		},
	}
	m.refreshViewport()
	return m
}

func (m chatModel) Init() tea.Cmd {
	return textarea.Blink
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg), nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "esc":
			if m.inFlight && m.cancel != nil {
				m.cancel()
				return m, nil
			}
			return m.restorePreviousPrompt(), nil
		case "enter":
			if m.inFlight {
				return m, nil
			}
			return m.submitPrompt()
		}
	case spinner.TickMsg:
		if m.inFlight {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	case streamChunkMsg:
		m.errMsg = ""
		m.appendAssistantChunk(msg.content)
		m.refreshViewport()
		return m, waitForStreamMsg(m.streamCh)
	case streamDoneMsg:
		m.inFlight = false
		m.conversation = append(m.conversation, chatMessage{Role: "assistant", Content: msg.reply})
		m.cancel = nil
		m.streamCh = nil
		m.errMsg = ""
		m.refreshViewport()
		return m, nil
	case streamErrMsg:
		m.inFlight = false
		m.cancel = nil
		m.streamCh = nil
		if errors.Is(msg.err, context.Canceled) {
			m.errMsg = ""
		} else {
			m.errMsg = msg.err.Error()
		}
		m.refreshViewport()
		return m, nil
	}

	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m chatModel) View() string {
	if !m.ready {
		return "\n  Loading..."
	}

	header := headerStyle.Render("llm-cli")
	status := statusStyle.Render("Model: " + m.cfg.Model)
	if m.inFlight {
		status = statusStyle.Render(m.spinner.View() + " Streaming from " + m.cfg.Model)
	}

	input := m.textarea.View()
	help := helpStyle.Render("Enter send • Alt+Enter newline • Esc cancel/edit previous • Ctrl+D quit")

	parts := []string{
		header,
		status,
		m.viewport.View(),
		input,
		help,
	}
	if m.errMsg != "" {
		parts = append(parts, errorStyle.Render("Error: "+m.errMsg))
	}

	return appStyle.Width(m.width).Height(m.height).Render(strings.Join(parts, "\n"))
}

func (m chatModel) handleWindowSize(msg tea.WindowSizeMsg) chatModel {
	m.width = msg.Width
	m.height = msg.Height
	m.ready = true

	contentWidth := max(20, msg.Width-4)
	headerHeight := 1
	statusHeight := 1
	inputHeight := 4
	helpHeight := 1
	errorHeight := 0
	if m.errMsg != "" {
		errorHeight = 2
	}

	viewportHeight := msg.Height - (headerHeight + statusHeight + inputHeight + helpHeight + errorHeight + 4)
	if viewportHeight < 5 {
		viewportHeight = 5
	}

	m.viewport.Width = contentWidth
	m.viewport.Height = viewportHeight
	m.textarea.SetWidth(contentWidth)
	m.refreshViewport()
	return m
}

func (m chatModel) submitPrompt() (chatModel, tea.Cmd) {
	prompt := strings.TrimSpace(m.textarea.Value())
	if prompt == "" {
		return m, nil
	}

	m.errMsg = ""
	m.inFlight = true
	m.transcript = append(m.transcript,
		transcriptEntry{role: "user", content: prompt},
		transcriptEntry{role: "assistant", content: ""},
	)
	m.conversation = append(m.conversation, chatMessage{Role: "user", Content: prompt})
	m.textarea.Reset()
	m.refreshViewport()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	chatCopy := append([]chatMessage(nil), m.conversation...)
	ch := m.startStream(ctx, chatCopy)
	m.streamCh = ch

	return m, tea.Batch(m.spinner.Tick, waitForStreamMsg(ch))
}

func (m chatModel) restorePreviousPrompt() chatModel {
	for i := len(m.conversation) - 1; i >= 0; i-- {
		if m.conversation[i].Role != "user" {
			continue
		}

		m.textarea.SetValue(m.conversation[i].Content)
		m.textarea.CursorEnd()
		return m
	}

	return m
}

func (m *chatModel) appendAssistantChunk(chunk string) {
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if m.transcript[i].role == "assistant" {
			m.transcript[i].content += chunk
			return
		}
	}
	m.transcript = append(m.transcript, transcriptEntry{role: "assistant", content: chunk})
}

func (m *chatModel) refreshViewport() {
	var sections []string
	for _, entry := range m.transcript {
		if strings.TrimSpace(entry.content) == "" && entry.role == "assistant" && m.inFlight {
			sections = append(sections, assistantStyle.Render("Assistant"), "")
			continue
		}

		label := assistantStyle.Render("Assistant")
		if entry.role == "user" {
			label = userStyle.Render("You")
		}

		sections = append(sections, label, entry.content)
	}

	m.viewport.SetContent(strings.Join(sections, "\n\n"))
	m.viewport.GotoBottom()
}

func (m chatModel) startStream(ctx context.Context, messages []chatMessage) <-chan tea.Msg {
	ch := make(chan tea.Msg, 32)

	go func() {
		reply, err := m.streamRunner(ctx, m.cfg, messages, func(chunk string) error {
			ch <- streamChunkMsg{content: chunk}
			return nil
		})
		if err != nil {
			ch <- streamErrMsg{err: err}
			return
		}
		ch <- streamDoneMsg{reply: reply}
	}()

	return ch
}

func waitForStreamMsg(ch <-chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}

	return func() tea.Msg {
		return <-ch
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
