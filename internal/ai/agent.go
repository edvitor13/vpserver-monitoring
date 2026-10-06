package ai

import "context"

// Executor é quem sabe responder às ferramentas (o monitor). Só leitura.
type Executor interface {
	Tools() []Tool
	// Label descreve a chamada para a tela ("histórico de CPU · 7 dias").
	Label(name, args string) string
	// Run executa e devolve o texto que volta para a IA.
	Run(ctx context.Context, name, args string) string
}

// Event é o que vai para o navegador durante a resposta.
type Event struct {
	Type  string `json:"type"` // delta | tool
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
}

// MaxSteps limita as rodadas de ferramenta por pergunta (a última é forçada a responder).
const MaxSteps = 8

// Converse roda a conversa: manda o histórico, executa as ferramentas que a IA
// pedir e repete até ela responder em texto. emit recebe o texto aos pedaços e
// cada ferramenta usada.
func Converse(ctx context.Context, cli *Client, system string, history []Message, ex Executor, emit func(Event)) (Usage, error) {
	msgs := append([]Message{Text("system", system)}, history...)
	var total Usage
	for step := 0; step < MaxSteps; step++ {
		allow := step < MaxSteps-1
		res, err := cli.Stream(ctx, msgs, ex.Tools(), allow, func(t string) { emit(Event{Type: "delta", Text: t}) })
		total.Add(res.Usage)
		if err != nil {
			return total, err
		}
		if len(res.ToolCalls) == 0 {
			return total, nil
		}
		var content *string
		if res.Content != "" {
			s := res.Content
			content = &s
		}
		msgs = append(msgs, Message{Role: "assistant", Content: content, ToolCalls: res.ToolCalls})
		for _, tc := range res.ToolCalls {
			emit(Event{Type: "tool", Label: ex.Label(tc.Function.Name, tc.Function.Arguments)})
			out := ex.Run(ctx, tc.Function.Name, tc.Function.Arguments)
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, Content: &out})
		}
	}
	return total, nil
}
