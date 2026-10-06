package store

import (
	"sort"
	"strings"
)

// RxTx são bytes recebidos (Rx) e enviados (Tx).
type RxTx struct {
	Rx uint64 `json:"rx"`
	Tx uint64 `json:"tx"`
}

// Counter é a última leitura de um contador acumulado (placa ou contêiner).
// ID muda quando o contêiner é recriado ou o host reinicia: aí o contador
// recomeçou do zero.
type Counter struct {
	ID string
	Rx uint64
	Tx uint64
}

// Traffic soma bytes por dia, por chave ("host", "c:<contêiner>", "app:<app>").
type Traffic struct {
	Days map[string]map[string]*RxTx // chave → "2006-01-02" → total
	Last map[string]Counter
}

func NewTraffic() *Traffic {
	return &Traffic{Days: map[string]map[string]*RxTx{}, Last: map[string]Counter{}}
}

// Observe recebe o contador acumulado atual e devolve quanto passou desde a
// última leitura. A primeira leitura de uma chave só marca a base (não conta
// o histórico anterior ao monitor como se fosse de hoje).
func (t *Traffic) Observe(key, id string, rx, tx uint64) (drx, dtx uint64) {
	last, ok := t.Last[key]
	t.Last[key] = Counter{ID: id, Rx: rx, Tx: tx}
	if !ok {
		return 0, 0
	}
	if last.ID != id { // recriado/reiniciado: contador recomeçou
		return rx, tx
	}
	drx, dtx = rx, tx
	if rx >= last.Rx {
		drx = rx - last.Rx
	}
	if tx >= last.Tx {
		dtx = tx - last.Tx
	}
	return
}

// Add soma bytes no dia.
func (t *Traffic) Add(key, day string, rx, tx uint64) {
	if rx == 0 && tx == 0 {
		return
	}
	m := t.Days[key]
	if m == nil {
		m = map[string]*RxTx{}
		t.Days[key] = m
	}
	d := m[day]
	if d == nil {
		d = &RxTx{}
		m[day] = d
	}
	d.Rx += rx
	d.Tx += tx
}

// Day devolve o total de um dia.
func (t *Traffic) Day(key, day string) RxTx {
	if d := t.Days[key][day]; d != nil {
		return *d
	}
	return RxTx{}
}

// Month soma os dias que começam com o prefixo "2006-01".
func (t *Traffic) Month(key, month string) RxTx {
	var out RxTx
	for day, v := range t.Days[key] {
		if strings.HasPrefix(day, month) {
			out.Rx += v.Rx
			out.Tx += v.Tx
		}
	}
	return out
}

// Total soma tudo que foi registrado para a chave.
func (t *Traffic) Total(key string) RxTx {
	var out RxTx
	for _, v := range t.Days[key] {
		out.Rx += v.Rx
		out.Tx += v.Tx
	}
	return out
}

// Keys devolve as chaves com um prefixo ("app:", "c:").
func (t *Traffic) Keys(prefix string) []string {
	var out []string
	for k := range t.Days {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Prune apaga dias anteriores a `keepFrom` ("2006-01-02"); o host guarda mais.
func (t *Traffic) Prune(keepFrom, keepFromHost string) {
	for k, m := range t.Days {
		limit := keepFrom
		if k == "host" {
			limit = keepFromHost
		}
		for day := range m {
			if day < limit {
				delete(m, day)
			}
		}
		if len(m) == 0 {
			delete(t.Days, k)
		}
	}
}
