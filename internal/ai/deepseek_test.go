package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigURL(t *testing.T) {
	c := Config{Host: "api.deepseek.com", Endpoint: "v1/chat/completions"}
	if c.URL() != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatal(c.URL())
	}
	c = Config{Host: "http://localhost:9/", Endpoint: "/x"}
	if c.URL() != "http://localhost:9/x" {
		t.Fatal(c.URL())
	}
}

// pedidos de ferramenta chegam picados em vários pedaços (formato OpenAI)
const toolStream = `data: {"choices":[{"delta":{"content":"Vou ver. "}}]}

: keep-alive

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"historico_servidor","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"campos\":[\"cpu\"],"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"periodo\":\"7d\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":64}}

data: [DONE]
`

func TestParseStreamToolCalls(t *testing.T) {
	var got strings.Builder
	res, err := parseStream(strings.NewReader(toolStream), func(s string) { got.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "Vou ver. " || res.Content != "Vou ver. " || res.Finish != "tool_calls" {
		t.Fatalf("texto: %q %+v", got.String(), res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_1" || res.ToolCalls[0].Function.Name != "historico_servidor" ||
		res.ToolCalls[0].Function.Arguments != `{"campos":["cpu"],"periodo":"7d"}` {
		t.Fatalf("ferramenta: %+v", res.ToolCalls)
	}
	if res.Usage.PromptTokens != 100 || res.Usage.CacheHitTokens != 64 {
		t.Fatalf("uso: %+v", res.Usage)
	}
}

type fakeExec struct{ ran []string }

func (f *fakeExec) Tools() []Tool {
	return []Tool{{Type: "function", Function: ToolFunction{Name: "historico_servidor"}}}
}
func (f *fakeExec) Label(n, a string) string { return "label:" + n }
func (f *fakeExec) Run(_ context.Context, n, a string) string {
	f.ran = append(f.ran, n+" "+a)
	return `{"cpu":"12%"}`
}

func TestConverseRunsToolsThenAnswers(t *testing.T) {
	round := 0
	var seen [][]Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer chave" {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Messages []Message `json:"messages"`
			Stream   bool      `json:"stream"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		seen = append(seen, body.Messages)
		round++
		w.Header().Set("Content-Type", "text/event-stream")
		if round == 1 {
			io.WriteString(w, toolStream)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"A CPU ficou em 12%.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()

	cli := NewClient(Config{APIKey: "chave", Host: srv.URL, Endpoint: "/v1/chat/completions", Model: "deepseek-chat"})
	ex := &fakeExec{}
	var events []Event
	_, err := Converse(context.Background(), cli, "sistema", []Message{Text("user", "como está a CPU?")}, ex, func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.ran) != 1 || ex.ran[0] != `historico_servidor {"campos":["cpu"],"periodo":"7d"}` {
		t.Fatalf("ferramentas: %v", ex.ran)
	}
	// a 2ª rodada leva: sistema, pergunta, pedido da IA e o resultado da ferramenta
	if len(seen) != 2 || len(seen[1]) != 4 || seen[1][2].Role != "assistant" || seen[1][3].Role != "tool" || seen[1][3].ToolCallID != "call_1" {
		t.Fatalf("histórico da 2ª rodada: %+v", seen)
	}
	var text strings.Builder
	tools := 0
	for _, e := range events {
		if e.Type == "delta" {
			text.WriteString(e.Text)
		}
		if e.Type == "tool" {
			tools++
		}
	}
	if text.String() != "Vou ver. A CPU ficou em 12%." || tools != 1 {
		t.Fatalf("eventos: %q tools=%d", text.String(), tools)
	}
}

func TestStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(402) }))
	defer srv.Close()
	cli := NewClient(Config{APIKey: "x", Host: srv.URL, Endpoint: "/c", Model: "m"})
	_, err := cli.Stream(context.Background(), []Message{Text("user", "oi")}, nil, true, nil)
	if ae, ok := err.(*Error); !ok || !strings.Contains(ae.Message, "sem saldo") || ae.Retry {
		t.Fatalf("erro: %v", err)
	}
}
