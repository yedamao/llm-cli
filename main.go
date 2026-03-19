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

	prompt, err := readPrompt(os.Args[1:], os.Stdin)
	if err != nil {
		return err
	}

	return streamChatCompletion(context.Background(), cfg, prompt, os.Stdout)
}

func printUsage() {
	fmt.Println(`llm-cli - call an OpenAI-compatible chat API

Usage:
  llm-cli "your prompt"
  echo "your prompt" | llm-cli

Configuration:
  Env vars:
    BASE_URL
    API_KEY
    MODEL

  Home config file:
    ~/.llm-cli.json

Environment variables override values from the config file.`)
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

func streamChatCompletion(ctx context.Context, cfg Config, prompt string, output io.Writer) error {
	reqBody := chatRequest{
		Model: cfg.Model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		Stream: true,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	url := cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read error response: %w", err)
		}

		var payload chatResponse
		if err := json.Unmarshal(respBody, &payload); err == nil {
			if payload.Error != nil && payload.Error.Message != "" {
				return fmt.Errorf("api error (%d): %s", resp.StatusCode, payload.Error.Message)
			}
		}

		return fmt.Errorf("api error (%d)", resp.StatusCode)
	}

	if err := streamSSE(resp.Body, output); err != nil {
		return err
	}

	_, err = fmt.Fprintln(output)
	return err
}

func streamSSE(input io.Reader, output io.Writer) error {
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

		if _, err := io.WriteString(output, content); err != nil {
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
