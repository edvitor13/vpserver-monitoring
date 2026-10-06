package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Evolution fala com a Evolution API v2 (contêiner vpserver-whatsapp), o
// HTTP com o header "apikey". Ela só existe na rede
// interna do painel; não publica porta.
//
// Formatos conferidos na v2.3.7:
//   - connectionState: {"instance":{"state":"open|connecting|close"}}; 404 se a instância não existe;
//   - create/connect: {"pairingCode", "code", "base64":"data:image/png;base64,...", "count"}
//     (no create, dentro de "qrcode");
//   - o código de pareamento só sai com a instância em "close": com o QR ativo,
//     é preciso fazer logout antes;
//   - sendText com o WhatsApp desconectado trava a chamada: confira o estado antes.
type Evolution struct {
	base, key, instance string
	hc                  *http.Client
}

func NewEvolution(base, key, instance string) *Evolution {
	return &Evolution{base: strings.TrimRight(base, "/"), key: key, instance: instance, hc: &http.Client{Timeout: 30 * time.Second}}
}

// Configured diz se há URL e chave (sem isso, o WhatsApp não foi instalado).
func (e *Evolution) Configured() bool { return e != nil && e.base != "" && e.key != "" }

// Status é a situação da conexão com o WhatsApp.
type Status struct {
	Service bool   `json:"service"` // a Evolution respondeu
	Error   string `json:"error,omitempty"`
	Exists  bool   `json:"exists"`
	State   string `json:"state"` // open | connecting | close | ""
	Number  string `json:"number,omitempty"`
	Name    string `json:"name,omitempty"`
}

func (s Status) Connected() bool { return s.State == "open" }

// QR é o que a tela mostra para parear o aparelho.
type QR struct {
	Connected   bool   `json:"connected"`
	Image       string `json:"image,omitempty"` // data:image/png;base64,...
	PairingCode string `json:"pairingCode,omitempty"`
	Count       int    `json:"count,omitempty"`
}

type Group struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Size    int    `json:"size"`
}

// apiError é a resposta de erro da Evolution: {"status":404,"response":{"message":[...]}}.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return e.Message }

func (e *Evolution) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("apikey", e.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		ae := &apiError{Status: resp.StatusCode, Message: fmt.Sprintf("a Evolution respondeu %d", resp.StatusCode)}
		var eb struct {
			Response struct {
				Message any `json:"message"`
			} `json:"response"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			switch m := eb.Response.Message.(type) {
			case string:
				ae.Message = m
			case []any:
				var parts []string
				for _, x := range m {
					parts = append(parts, fmt.Sprint(x))
				}
				if len(parts) > 0 {
					ae.Message = strings.Join(parts, "; ")
				}
			}
		}
		if resp.StatusCode == http.StatusUnauthorized {
			ae.Message = "a chave da Evolution não confere (VPMON_WA_KEY)"
		}
		return ae
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func isNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Status consulta a conexão (e, conectado, o número e o nome do perfil).
func (e *Evolution) Status(ctx context.Context) Status {
	var r struct {
		Instance struct {
			State string `json:"state"`
		} `json:"instance"`
	}
	err := e.do(ctx, http.MethodGet, "/instance/connectionState/"+url.PathEscape(e.instance), nil, &r)
	switch {
	case isNotFound(err):
		return Status{Service: true}
	case err != nil:
		var ae *apiError
		if errors.As(err, &ae) {
			return Status{Service: true, Error: ae.Message}
		}
		return Status{Error: "o serviço de WhatsApp não respondeu"}
	}
	st := Status{Service: true, Exists: true, State: r.Instance.State}
	if st.Connected() {
		var list []struct {
			Name        string `json:"name"`
			OwnerJid    string `json:"ownerJid"`
			ProfileName string `json:"profileName"`
		}
		if e.do(ctx, http.MethodGet, "/instance/fetchInstances?instanceName="+url.QueryEscape(e.instance), nil, &list) == nil {
			for _, it := range list {
				if it.Name == e.instance {
					st.Number, _, _ = strings.Cut(it.OwnerJid, "@")
					st.Name = it.ProfileName
				}
			}
		}
	}
	return st
}

type qrBody struct {
	PairingCode string `json:"pairingCode"`
	Base64      string `json:"base64"`
	Count       int    `json:"count"`
	Instance    *struct {
		State string `json:"state"`
	} `json:"instance"`
}

func (b qrBody) qr() QR {
	return QR{Image: b.Base64, PairingCode: b.PairingCode, Count: b.Count, Connected: b.Instance != nil && b.Instance.State == "open"}
}

// Connect devolve o QR (e, com número, o código de pareamento) para conectar.
// Cria a instância se ainda não existir.
func (e *Evolution) Connect(ctx context.Context, number string) (QR, error) {
	st := e.Status(ctx)
	if !st.Service {
		return QR{}, errors.New(st.Error)
	}
	if st.Connected() {
		return QR{Connected: true}, nil
	}
	if !st.Exists {
		body := map[string]any{
			"instanceName": e.instance, "qrcode": true, "integration": "WHATSAPP-BAILEYS",
			// o painel só manda mensagem: nada de histórico, de marcar como lida, nem
			// de ficar "online" (online, o celular para de tocar notificação)
			"syncFullHistory": false, "readMessages": false, "readStatus": false,
			"groupsIgnore": true, "alwaysOnline": false, "rejectCall": false,
		}
		if number != "" {
			body["number"] = number
		}
		var r struct {
			QRCode qrBody `json:"qrcode"`
		}
		if err := e.do(ctx, http.MethodPost, "/instance/create", body, &r); err != nil {
			return QR{}, err
		}
		return r.QRCode.qr(), nil
	}
	if number != "" && st.State == "connecting" {
		// o código de pareamento só sai a partir de "close"
		e.do(ctx, http.MethodDelete, "/instance/logout/"+url.PathEscape(e.instance), nil, nil)
		time.Sleep(1500 * time.Millisecond)
	}
	path := "/instance/connect/" + url.PathEscape(e.instance)
	if number != "" {
		path += "?number=" + url.QueryEscape(number)
	}
	var r qrBody
	if err := e.do(ctx, http.MethodGet, path, nil, &r); err != nil {
		return QR{}, err
	}
	return r.qr(), nil
}

// Logout desconecta o aparelho (a instância continua, pronta para outro QR).
func (e *Evolution) Logout(ctx context.Context) error {
	err := e.do(ctx, http.MethodDelete, "/instance/logout/"+url.PathEscape(e.instance), nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Send manda um texto para um número (só dígitos, com DDI) ou grupo (...@g.us).
func (e *Evolution) Send(ctx context.Context, to, text string) error {
	return e.do(ctx, http.MethodPost, "/message/sendText/"+url.PathEscape(e.instance),
		map[string]any{"number": to, "text": text, "linkPreview": false}, nil)
}

// Groups lista os grupos de que o número conectado participa.
func (e *Evolution) Groups(ctx context.Context) ([]Group, error) {
	var list []Group
	err := e.do(ctx, http.MethodGet, "/group/fetchAllGroups/"+url.PathEscape(e.instance)+"?getParticipants=false", nil, &list)
	return list, err
}
