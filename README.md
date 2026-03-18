# llm-cli

Small command line client for OpenAI-compatible APIs.

## Install

```bash
go install github.com/yedamao/llm-cli@latest
```

## Config

`llm-cli` reads configuration from either environment variables or a dot config file in your home directory:

- `BASE_URL`
- `API_KEY`
- `MODEL`

Environment variables take precedence over file values.

### Config file

Create `~/.llm-cli.json`:

```json
{
  "BASE_URL": "https://api.openai.com/v1",
  "API_KEY": "your-api-key",
  "MODEL": "gpt-4o-mini"
}
```

## Usage

```bash
llm-cli "Write a haiku about Go"
echo "Summarize this text" | llm-cli
```

For OpenAI-compatible providers, `BASE_URL` should point at the provider's `/v1` root. The CLI sends requests to:

```text
{BASE_URL}/chat/completions
```
