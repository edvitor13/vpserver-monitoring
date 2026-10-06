// Package store guarda o histórico em memória (anéis de tamanho fixo, em
// várias resoluções) e salva tudo num arquivo para sobreviver a reinícios.
//
// Sem banco de dados: o volume é pequeno (dezenas de séries × alguns
// milhares de pontos) e um anel fixo nunca cresce.
package store

import (
	"math"
	"strconv"
)

// Tier é um anel de pontos com passo fixo; cada ponto é a média das amostras
// que caíram naquele intervalo.
type Tier struct {
	Step int64 // segundos
	Cap  int
	T    []int64
	V    [][]float32 // [campo][posição]
	Head int         // próxima posição a gravar
	N    int

	AccT   int64 // início do intervalo em acumulação
	AccSum []float64
	AccN   int
}

type TierSpec struct {
	Step int64
	Cap  int
}

func newTier(sp TierSpec, nf int) *Tier {
	t := &Tier{Step: sp.Step, Cap: sp.Cap, T: make([]int64, sp.Cap), V: make([][]float32, nf), AccSum: make([]float64, nf)}
	for i := range t.V {
		t.V[i] = make([]float32, sp.Cap)
	}
	return t
}

func (t *Tier) add(ts int64, vals []float64) {
	b := ts - ts%t.Step
	if t.AccN > 0 && b != t.AccT {
		t.flush()
	}
	if t.AccN == 0 {
		t.AccT = b
	}
	for i := range t.AccSum {
		if i < len(vals) {
			t.AccSum[i] += vals[i]
		}
	}
	t.AccN++
}

func (t *Tier) flush() {
	t.T[t.Head] = t.AccT
	for i := range t.V {
		t.V[i][t.Head] = float32(t.AccSum[i] / float64(t.AccN))
		t.AccSum[i] = 0
	}
	t.Head = (t.Head + 1) % t.Cap
	if t.N < t.Cap {
		t.N++
	}
	t.AccN = 0
}

// oldest devolve o timestamp mais antigo guardado (0 se vazio).
func (t *Tier) oldest() int64 {
	if t.N == 0 {
		return 0
	}
	return t.T[(t.Head-t.N+t.Cap)%t.Cap]
}

// Series é um conjunto de campos (cpu, mem, rx...) com vários níveis de resolução.
type Series struct {
	Fields []string
	Tiers  []*Tier
	Last   int64
}

func NewSeries(fields []string, specs []TierSpec) *Series {
	s := &Series{Fields: fields}
	for _, sp := range specs {
		s.Tiers = append(s.Tiers, newTier(sp, len(fields)))
	}
	return s
}

func (s *Series) Add(ts int64, vals []float64) {
	for _, t := range s.Tiers {
		t.add(ts, vals)
	}
	s.Last = ts
}

// Compatible diz se a série salva tem o mesmo formato que o código atual espera.
func (s *Series) Compatible(fields []string, specs []TierSpec) bool {
	if s == nil || len(s.Fields) != len(fields) || len(s.Tiers) != len(specs) {
		return false
	}
	for i := range fields {
		if s.Fields[i] != fields[i] {
			return false
		}
	}
	for i, t := range s.Tiers {
		if t.Step != specs[i].Step || t.Cap != specs[i].Cap || len(t.V) != len(fields) {
			return false
		}
	}
	return true
}

// Result é o que vai para o gráfico: tempos e uma coluna por campo (NaN = sem dado).
type Result struct {
	Step int64           `json:"step"`
	T    []int64         `json:"t"`
	Cols map[string]Nums `json:"s"`
}

// Query devolve os pontos entre from e to, no nível mais fino que cubra o
// período, com no máximo maxPts pontos (agrupando pela média se precisar).
func (s *Series) Query(from, to int64, maxPts int, fields ...string) Result {
	var tier *Tier
	for _, t := range s.Tiers {
		if t.N < t.Cap || t.oldest() <= from {
			tier = t
			break
		}
	}
	if tier == nil {
		tier = s.Tiers[len(s.Tiers)-1]
	}
	idx := map[string]int{}
	for i, f := range s.Fields {
		idx[f] = i
	}
	if len(fields) == 0 {
		fields = s.Fields
	}

	type pt struct {
		t int64
		v []float64
	}
	var pts []pt
	get := func(pos int) []float64 {
		v := make([]float64, len(fields))
		for j, f := range fields {
			if i, ok := idx[f]; ok {
				v[j] = float64(tier.V[i][pos])
			} else {
				v[j] = math.NaN()
			}
		}
		return v
	}
	for k := 0; k < tier.N; k++ {
		pos := (tier.Head - tier.N + k + tier.Cap) % tier.Cap
		if t := tier.T[pos]; t >= from && t <= to {
			pts = append(pts, pt{t, get(pos)})
		}
	}
	// o intervalo ainda em acumulação entra como último ponto (parcial)
	if tier.AccN > 0 && tier.AccT >= from && tier.AccT <= to {
		v := make([]float64, len(fields))
		for j, f := range fields {
			if i, ok := idx[f]; ok {
				v[j] = tier.AccSum[i] / float64(tier.AccN)
			} else {
				v[j] = math.NaN()
			}
		}
		pts = append(pts, pt{tier.AccT, v})
	}

	step := tier.Step
	if maxPts > 0 && len(pts) > maxPts {
		g := (len(pts) + maxPts - 1) / maxPts
		step *= int64(g)
		var merged []pt
		for i := 0; i < len(pts); i += g {
			end := min(i+g, len(pts))
			v := make([]float64, len(fields))
			for j := range fields {
				sum, n := 0.0, 0
				for _, p := range pts[i:end] {
					if !math.IsNaN(p.v[j]) {
						sum += p.v[j]
						n++
					}
				}
				v[j] = math.NaN()
				if n > 0 {
					v[j] = sum / float64(n)
				}
			}
			merged = append(merged, pt{pts[i].t, v})
		}
		pts = merged
	}

	res := Result{Step: step, Cols: map[string]Nums{}}
	cols := make([]Nums, len(fields))
	var prev int64
	for _, p := range pts {
		// buraco maior que 2,5 passos (monitor parado): ponto nulo para o gráfico não ligar as pontas
		if prev != 0 && p.t-prev > step*5/2 {
			res.T = append(res.T, prev+step)
			for j := range cols {
				cols[j] = append(cols[j], math.NaN())
			}
		}
		res.T = append(res.T, p.t)
		for j := range cols {
			cols[j] = append(cols[j], p.v[j])
		}
		prev = p.t
	}
	for j, f := range fields {
		if cols[j] == nil {
			cols[j] = Nums{}
		}
		res.Cols[f] = cols[j]
	}
	if res.T == nil {
		res.T = []int64{}
	}
	return res
}

// Values devolve os valores de um campo no nível indicado, entre from e to (para percentis).
func (s *Series) Values(tierIdx int, field string, from int64) []float64 {
	if tierIdx >= len(s.Tiers) {
		return nil
	}
	fi := -1
	for i, f := range s.Fields {
		if f == field {
			fi = i
		}
	}
	if fi < 0 {
		return nil
	}
	t := s.Tiers[tierIdx]
	var out []float64
	for k := 0; k < t.N; k++ {
		pos := (t.Head - t.N + k + t.Cap) % t.Cap
		if t.T[pos] >= from {
			out = append(out, float64(t.V[fi][pos]))
		}
	}
	return out
}

// Nums vira JSON com null no lugar de NaN e poucas casas decimais.
type Nums []float64

func (n Nums) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, len(n)*6+2)
	b = append(b, '[')
	for i, v := range n {
		if i > 0 {
			b = append(b, ',')
		}
		switch {
		case math.IsNaN(v) || math.IsInf(v, 0):
			b = append(b, "null"...)
		case math.Abs(v) >= 100:
			b = strconv.AppendFloat(b, math.Round(v), 'f', -1, 64)
		default:
			b = strconv.AppendFloat(b, math.Round(v*100)/100, 'f', -1, 64)
		}
	}
	return append(b, ']'), nil
}
