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
llm-cli
```

Running `llm-cli` with no arguments in a terminal starts an interactive chat loop with line editing and history. Each new prompt includes the earlier conversation, so follow-up questions work as expected.

Example:

```text
$ llm-cli
> Explain Go interfaces simply
Interfaces define behavior through method sets.
> Show a short example
type Reader interface {
    Read(p []byte) (n int, err error)
}
```

Press `Ctrl+D` to exit. `Ctrl+C` aborts the current prompt.

For OpenAI-compatible providers, `BASE_URL` should point at the provider's `/v1` root. The CLI sends requests to:

```text
{BASE_URL}/chat/completions
```
