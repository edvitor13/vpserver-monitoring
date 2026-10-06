package store

import (
	"bufio"
	"encoding/gob"
	"os"
	"path/filepath"

	"github.com/edvitor13/vpserver-monitoring/internal/docker"
)

// LogCheck é uma passada do contador de logs (a cada poucos minutos).
type LogCheck struct {
	T      int64
	Lines  int
	Errors int
}

// LogStat é a atividade de log de um contêiner nas últimas 24 h.
type LogStat struct {
	Checks     []LogCheck
	LastErrors []docker.LogLine
	Since      int64 // até onde já foi lido (unix)
}

// State é tudo que sobrevive a um reinício do monitor.
type State struct {
	Created  int64
	Host     *Series
	Units    map[string]*Series // "c:<contêiner>", "s:<serviço>", "app:<app>"
	Traffic  *Traffic
	Events   []docker.Event
	Colors   map[string]int // app → posição fixa na paleta
	LogStats map[string]*LogStat
	EventsV  int // formato dos eventos guardados (2 = com kill/stop, que separam deploy de queda)
}

func NewState(now int64) *State {
	return &State{
		Created: now, Units: map[string]*Series{}, Traffic: NewTraffic(),
		Colors: map[string]int{}, LogStats: map[string]*LogStat{},
	}
}

// Load lê o estado salvo; um arquivo ausente ou corrompido devolve erro e o
// monitor começa do zero.
func Load(path string) (*State, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s State
	if err := gob.NewDecoder(bufio.NewReader(f)).Decode(&s); err != nil {
		return nil, err
	}
	if s.Units == nil {
		s.Units = map[string]*Series{}
	}
	if s.Traffic == nil {
		s.Traffic = NewTraffic()
	}
	if s.Traffic.Days == nil {
		s.Traffic.Days = map[string]map[string]*RxTx{}
	}
	if s.Traffic.Last == nil {
		s.Traffic.Last = map[string]Counter{}
	}
	if s.Colors == nil {
		s.Colors = map[string]int{}
	}
	if s.LogStats == nil {
		s.LogStats = map[string]*LogStat{}
	}
	return &s, nil
}

// Save grava de forma atômica (arquivo temporário + rename).
func (s *State) Save(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(tmp, 256*1024)
	if err := gob.NewEncoder(w).Encode(s); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
