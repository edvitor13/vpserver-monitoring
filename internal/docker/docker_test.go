package docker

import (
	"encoding/binary"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestParseLogsMultiplexed(t *testing.T) {
	var b []byte
	b = append(b, frame(1, "2026-10-05T18:51:28.712728123Z hello\n")...)
	b = append(b, frame(2, "2026-10-05T18:51:29.000000000Z \x1b[31mERROR\x1b[0m boom\n")...)
	// uma linha quebrada em dois quadros
	b = append(b, frame(1, "2026-10-05T18:51:30.000000000Z par")...)
	b = append(b, frame(1, "tida\n")...)
	ls := ParseLogs(b)
	if len(ls) != 3 {
		t.Fatalf("esperava 3 linhas, veio %d: %+v", len(ls), ls)
	}
	if ls[0].Msg != "hello" || ls[0].Stream != "out" || ls[0].T == 0 {
		t.Fatalf("linha 0: %+v", ls[0])
	}
	if ls[1].Msg != "ERROR boom" || ls[1].Stream != "err" {
		t.Fatalf("linha 1 (ANSI não removido?): %+v", ls[1])
	}
	if ls[2].Msg != "partida" {
		t.Fatalf("linha 2: %+v", ls[2])
	}
}

func TestParseLogsTTY(t *testing.T) {
	ls := ParseLogs([]byte("2026-10-05T18:51:28Z a\r\n2026-10-05T18:51:29Z b\n"))
	if len(ls) != 2 || ls[0].Msg != "a" || ls[1].Msg != "b" {
		t.Fatalf("tty: %+v", ls)
	}
}

func TestHealthFromStatus(t *testing.T) {
	cases := map[string]string{
		"Up 2 days (healthy)":            "healthy",
		"Up 1 second (health: starting)": "starting",
		"Up 3 hours (unhealthy)":         "unhealthy",
		"Up 4 weeks":                     "",
	}
	for in, want := range cases {
		if got := healthFromStatus(in); got != want {
			t.Errorf("%q → %q, queria %q", in, got, want)
		}
	}
}

func TestMarkRequested(t *testing.T) {
	evs := []Event{
		{T: 100, Action: "kill", Container: "app"}, // deploy: kill → die → stop
		{T: 101, Action: "die", Container: "app", ExitCode: "143"},
		{T: 101, Action: "stop", Container: "app"},
		{T: 103, Action: "start", Container: "app"},
		{T: 500, Action: "die", Container: "app", ExitCode: "1"}, // caiu sozinho
		{T: 501, Action: "start", Container: "app"},
	}
	got := MarkRequested(evs)
	if len(got) != 4 {
		t.Fatalf("kill/stop deveriam sair da lista: %+v", got)
	}
	if !got[0].Requested || got[2].Requested {
		t.Fatalf("parada pedida x queda: %+v", got)
	}
}
