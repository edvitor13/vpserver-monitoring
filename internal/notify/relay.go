package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WhatsApp emprestado entre painéis conectados.
//
// Num servidor conectado a um painel central (com o token liberado para
// isso), os avisos daqui saem pelo WhatsApp do central: este painel decide o
// quê e quando (aba Notificações, silêncio, limite por hora) e o central
// manda para os destinos DELE. No central, Relay recebe esses avisos.

// Relay é o caminho até o WhatsApp do painel central.
type Relay interface {
	// Active: os avisos daqui saem pelo central (ligado e o central pronto).
	Active() bool
	// Central é o nome do painel central (para a tela).
	Central() string
	Send(ctx context.Context, text string) error
}

const maxRelayText = 4000

// SetRelay liga o caminho até o WhatsApp do central (nil = só o daqui).
func (s *Service) SetRelay(r Relay) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relay = r
}

// relayActiveLocked: os avisos daqui saem pelo WhatsApp de um painel central.
func (s *Service) relayActiveLocked() bool { return s.relay != nil && s.relay.Active() }

// RelayReady diz se este painel pode mandar avisos de outros servidores pelo
// WhatsApp dele: notificações ligadas, WhatsApp conectado e alguém em "Para
// quem enviar".
func (s *Service) RelayReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Enabled && s.wa.Configured() && s.status.Connected() && len(s.cfg.Recipients) > 0
}

var ErrRelayNotReady = errors.New("o WhatsApp do painel central não está pronto (desconectado, desligado ou sem destinos)")

// Relay manda, pelo WhatsApp daqui, um aviso de um servidor conectado. Vai só
// para os destinos deste painel (o outro servidor não escolhe números), entra
// no limite de mensagens por hora daqui e leva o nome do servidor no fim, para
// ninguém se passar por este painel.
func (s *Service) Relay(ctx context.Context, from, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("aviso vazio")
	}
	if r := []rune(text); len(r) > maxRelayText {
		text = string(r[:maxRelayText]) + "…"
	}
	text += "\n\n📡 _Servidor conectado: " + from + "_"
	now := s.now()
	s.mu.Lock()
	if !(s.cfg.Enabled && s.wa.Configured() && s.status.Connected() && len(s.cfg.Recipients) > 0) {
		s.mu.Unlock()
		return ErrRelayNotReady
	}
	keep := s.st.Sends[:0]
	for _, t := range s.st.Sends {
		if now.Sub(time.Unix(t, 0)) < time.Hour {
			keep = append(keep, t)
		}
	}
	s.st.Sends = keep
	if len(s.st.Sends) >= maxPerHour {
		s.mu.Unlock()
		return fmt.Errorf("o painel central passou de %d mensagens na última hora", maxPerHour)
	}
	s.st.Sends = append(s.st.Sends, now.Unix())
	to := s.cfg.Recipients
	s.mu.Unlock()

	okN, errs := s.sendAll(ctx, to, text)
	e := LogEntry{T: now.Unix(), Kind: "relay", Title: "Aviso do servidor " + from, Text: clipText(text), Status: "sent"}
	switch {
	case okN == 0:
		e.Status, e.Error = "failed", strings.Join(errs, "; ")
	case len(errs) > 0:
		e.Status, e.Error = "partial", strings.Join(errs, "; ")
	}
	s.logEntry(e)
	if okN == 0 {
		return errors.New("o WhatsApp do painel central não conseguiu enviar")
	}
	return nil
}

// sendVia manda pelo central (servidor conectado) ou pelo WhatsApp daqui.
func (s *Service) sendVia(ctx context.Context, relay bool, to []Recipient, text string) (int, []string) {
	if !relay {
		return s.sendAll(ctx, to, text)
	}
	s.mu.Lock()
	r := s.relay
	s.mu.Unlock()
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := r.Send(c, text); err != nil {
		return 0, []string{"painel central: " + err.Error()}
	}
	return 1, nil
}
