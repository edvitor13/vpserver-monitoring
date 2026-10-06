package store

import (
	"math"
	"path/filepath"
	"testing"
)

func TestTierAveragesAndQuery(t *testing.T) {
	s := NewSeries([]string{"cpu"}, []TierSpec{{Step: 5, Cap: 10}, {Step: 60, Cap: 10}})
	for i := int64(0); i < 24; i++ { // 2 min de amostras a cada 5 s
		s.Add(1200+i*5, []float64{float64(i)})
	}
	r := s.Query(1200, 1200+200, 0, "cpu")
	if r.Step != 60 { // o nível de 5 s só guarda 50 s; a consulta de 200 s vai para o de 1 min
		t.Fatalf("passo = %d", r.Step)
	}
	// minuto 1200..1255 tem i=0..11 → média 5,5; o seguinte está em acumulação (12..23 → 17,5)
	if len(r.T) != 2 || r.Cols["cpu"][0] != 5.5 || r.Cols["cpu"][1] != 17.5 {
		t.Fatalf("resultado: %+v", r)
	}
}

func TestQueryInsertsGapAndDownsamples(t *testing.T) {
	s := NewSeries([]string{"v"}, []TierSpec{{Step: 5, Cap: 100}})
	for i := int64(0); i < 10; i++ {
		s.Add(i*5, []float64{1})
	}
	for i := int64(30); i < 40; i++ { // buraco de 100 s
		s.Add(i*5, []float64{1})
	}
	r := s.Query(0, 1000, 0, "v")
	nulls := 0
	for _, v := range r.Cols["v"] {
		if math.IsNaN(v) {
			nulls++
		}
	}
	if nulls != 1 {
		t.Fatalf("esperava 1 ponto nulo no buraco, veio %d (%v)", nulls, r.Cols["v"])
	}
	if d := s.Query(0, 1000, 5, "v"); len(d.T) > 7 {
		t.Fatalf("redução de pontos falhou: %d pontos", len(d.T))
	}
}

func TestRingWraps(t *testing.T) {
	s := NewSeries([]string{"v"}, []TierSpec{{Step: 1, Cap: 3}})
	for i := int64(0); i < 10; i++ {
		s.Add(i, []float64{float64(i)})
	}
	r := s.Query(0, 100, 0, "v")
	// 3 no anel (6, 7, 8) + o 9 em acumulação
	if len(r.T) != 4 || r.T[0] != 6 || r.Cols["v"][3] != 9 {
		t.Fatalf("anel: %+v", r)
	}
}

func TestTrafficObserve(t *testing.T) {
	tr := NewTraffic()
	if rx, tx := tr.Observe("c:x", "id1", 1000, 5000); rx != 0 || tx != 0 {
		t.Fatal("a primeira leitura só marca a base")
	}
	if rx, tx := tr.Observe("c:x", "id1", 1500, 5200); rx != 500 || tx != 200 {
		t.Fatalf("delta: %d %d", rx, tx)
	}
	if rx, tx := tr.Observe("c:x", "id2", 30, 40); rx != 30 || tx != 40 {
		t.Fatalf("recriado deveria contar do zero: %d %d", rx, tx)
	}
	if rx, _ := tr.Observe("c:x", "id2", 10, 50); rx != 10 {
		t.Fatalf("contador que voltou deveria contar do zero: %d", rx)
	}
	tr.Add("host", "2026-10-01", 1, 10)
	tr.Add("host", "2026-10-02", 2, 20)
	tr.Add("host", "2026-09-30", 4, 40)
	if m := tr.Month("host", "2026-10"); m.Rx != 3 || m.Tx != 30 {
		t.Fatalf("mês: %+v", m)
	}
	tr.Prune("2026-10-01", "2026-10-01")
	if tr.Total("host").Tx != 30 {
		t.Fatalf("prune: %+v", tr.Total("host"))
	}
}

func TestStateRoundTrip(t *testing.T) {
	st := NewState(42)
	st.Host = NewSeries([]string{"cpu"}, []TierSpec{{Step: 5, Cap: 4}})
	st.Host.Add(10, []float64{3})
	st.Traffic.Add("host", "2026-10-05", 1, 2)
	st.Colors["loja"] = 3
	p := filepath.Join(t.TempDir(), "state.gob")
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Created != 42 || got.Colors["loja"] != 3 || got.Traffic.Day("host", "2026-10-05").Tx != 2 || got.Host.Tiers[0].AccN != 1 {
		t.Fatalf("estado voltou diferente: %+v", got)
	}
}

func TestNumsJSON(t *testing.T) {
	b, _ := Nums{1.234, math.NaN(), 12345.6, 0}.MarshalJSON()
	if string(b) != "[1.23,null,12346,0]" {
		t.Fatalf("json: %s", b)
	}
}
