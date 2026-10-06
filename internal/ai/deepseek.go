// Package ai conversa com a DeepSeek (API no formato da OpenAI), com
// ferramentas (function calling) e resposta em streaming.
//
// Configuração: DEEPSEEK_API_KEY, DEEPSEEK_API_HOST
// (sem esquema, ex.: api.deepseek.com), DEEPSEEK_API_ENDPOINT
// (/v1/chat/completions) e DEEPSEEK_API_MODEL (deepseek-chat). A chave só
// existe no servidor: nunca vai para o navegador.
package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	APIKey   string
	Host     string // api.deepseek.com (com ou sem https://)
	Endpoint string // /v1/chat/completions
	Model    string // deepseek-chat
}

func (c Config) Enabled() bool { return strings.TrimSpace(c.APIKey) != "" }

// URL monta host + endpoint, pondo https:// quando o host vem sem esquema.
func (c Config) URL() string {
	host := strings.TrimRight(strings.TrimSpace(c.Host), "/")
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	ep := strings.TrimSpace(c.Endpoint)
	if !strings.HasPrefix(ep, "/") {
		ep = "/" + ep
	}
	return host + ep
}

// --- mensagens e ferramentas (formato OpenAI) -------------------------------------

type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

func Text(role, content string) Message { return Message{Role: role, Content: &content} }

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CacheHitTokens   int `json:"prompt_cache_hit_tokens"`
}

func (u *Usage) Add(o Usage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
	u.CacheHitTokens += o.CacheHitTokens
}

// Result é o que uma rodada devolveu: texto e/ou pedidos de ferramenta.
type Result struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	Finish    string
}

// Error é um erro já com mensagem para a tela.
type Error struct {
	Status  int
	Message string
	Retry   bool
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	cfg Config
	hc  *http.Client
}

func NewClient(cfg Config) *Client {
	// sem Timeout global: o streaming pode durar; quem chama põe prazo no contexto
	return &Client{cfg: cfg, hc: &http.Client{Transport: &http.Transport{
		ResponseHeaderTimeout: 120 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 2,
	}}}
}

func (c *Client) Model() string { return c.cfg.Model }

// Check confere a chave com uma chamada que não gasta tokens (lista de modelos).
func (c *Client) Check(ctx context.Context) error {
	u := c.cfg.URL()
	base := u
	if i := strings.Index(u, "/chat/completions"); i >= 0 {
		base = u[:i]
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	resp, err := c.hc.Do(req)
	if err != nil {
		return &Error{Message: "Não consegui falar com a DeepSeek (" + c.cfg.URL() + ").", Retry: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return statusError(resp.StatusCode, b)
	}
	return nil
}

// Stream faz uma rodada com stream=true. onText recebe cada pedaço de texto
// assim que chega. Pedidos de ferramenta são montados (vêm picados) e
// devolvidos no Result.
func (c *Client) Stream(ctx context.Context, msgs []Message, tools []Tool, allowTools bool, onText func(string)) (Result, error) {
	body := map[string]any{
		"model": c.cfg.Model, "messages": msgs, "stream": true,
		"stream_options": map[string]bool{"include_usage": true},
		"temperature":    0.3, "max_tokens": 4096,
	}
	if len(tools) > 0 {
		body["tools"] = tools
		if !allowTools {
			body["tool_choice"] = "none"
		}
	}
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL(), bytes.NewReader(buf))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.hc.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, &Error{Message: "A IA demorou demais para responder. Tente de novo.", Retry: true}
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, &Error{Message: "Não consegui falar com a DeepSeek agora. Tente de novo.", Retry: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Result{}, statusError(resp.StatusCode, b)
	}
	return parseStream(resp.Body, onText)
}

func statusError(code int, body []byte) error {
	e := &Error{Status: code}
	switch {
	case code == 401 || code == 403:
		e.Message = "A DeepSeek recusou a chave (DEEPSEEK_API_KEY). Confira o .env do painel."
	case code == 402:
		e.Message = "A conta da DeepSeek está sem saldo."
	case code == 429:
		e.Message, e.Retry = "A DeepSeek está com limite de uso atingido. Tente em instantes.", true
	case code >= 500:
		e.Message, e.Retry = "A DeepSeek está indisponível no momento. Tente de novo.", true
	default:
		e.Message = fmt.Sprintf("A DeepSeek recusou a requisição (%d): %s", code, strings.TrimSpace(string(body)))
	}
	return e
}

// parseStream lê o SSE da API: linhas "data: {...}", comentários ": keep-alive"
// e o "data: [DONE]" final.
func parseStream(r io.Reader, onText func(string)) (Result, error) {
	var res Result
	var text strings.Builder
	calls := map[int]*ToolCall{}
	var order []int
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *Usage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			res.Usage = *chunk.Usage
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				text.WriteString(ch.Delta.Content)
				if onText != nil {
					onText(ch.Delta.Content)
				}
			}
			for _, tc := range ch.Delta.ToolCalls {
				c := calls[tc.Index]
				if c == nil {
					c = &ToolCall{Type: "function"}
					calls[tc.Index] = c
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				c.Function.Name += tc.Function.Name
				c.Function.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil {
				res.Finish = *ch.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, &Error{Message: "A resposta da DeepSeek foi cortada no meio. Tente de novo.", Retry: true}
	}
	res.Content = text.String()
	for _, i := range order {
		c := calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		res.ToolCalls = append(res.ToolCalls, *c)
	}
	return res, nil
}
