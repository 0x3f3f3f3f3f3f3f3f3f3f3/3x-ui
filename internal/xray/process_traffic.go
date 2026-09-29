package xray

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"

	statscommand "github.com/xtls/xray-core/app/stats/command"
)

// TrafficBatch is stable across retries, including a lost SQL commit acknowledgement.
type TrafficBatch struct {
	ProcessID      string
	Sequence       int64
	ID             string
	Traffics       []*Traffic
	ClientTraffics []*ClientTraffic
}

type pendingTrafficBatch struct {
	batch  TrafficBatch
	cursor map[string]int64
}

// SettleTraffic advances this child's cursor only after settle commits.
// The callback must not call process lifecycle methods.
func (p *Process) SettleTraffic(settle func(*TrafficBatch) error) ([]*Traffic, []*ClientTraffic, error) {
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	if p.trafficDraining {
		return nil, nil, ErrFinalTrafficPending
	}
	if !p.IsControlReady() {
		return nil, nil, errors.New("xray control is not ready")
	}
	if p.trafficPending == nil {
		if err := p.ensureTrafficSequence(); err != nil {
			return nil, nil, err
		}
		pending, err := p.prepareTrafficBatch()
		if err != nil {
			return nil, nil, err
		}
		p.trafficPending = pending
	}
	pending := p.trafficPending
	if err := p.commitTrafficPending(settle); err != nil {
		return nil, nil, err
	}
	return pending.batch.Traffics, pending.batch.ClientTraffics, nil
}

func (p *Process) prepareTrafficBatch() (*pendingTrafficBatch, error) {
	var api XrayAPI
	if err := api.InitProcess(p); err != nil {
		return nil, err
	}
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := (*api.StatsServiceClient).QueryStats(ctx, &statscommand.QueryStatsRequest{Reset_: false})
	if err != nil {
		return nil, err
	}
	counters := make(map[string]int64, len(page.Stat))
	for _, stat := range page.Stat {
		counters[stat.Name] = stat.Value
	}
	return p.trafficBatchFromCounters(counters, false)
}

func (p *Process) ensureTrafficSequence() error {
	if p.trafficSequence == math.MaxInt64 {
		return errors.New("traffic batch sequence exhausted")
	}
	if p.trafficID == "" {
		p.trafficID = uuid.NewString()
	}
	return nil
}

func (p *Process) commitTrafficPending(settle func(*TrafficBatch) error) error {
	pending := p.trafficPending
	if err := settle(&pending.batch); err != nil {
		return err
	}
	p.trafficCursor = pending.cursor
	p.trafficSequence = pending.batch.Sequence
	p.trafficPending = nil
	return nil
}

func (p *Process) trafficBatchFromCounters(values map[string]int64, final bool) (*pendingTrafficBatch, error) {
	if final {
		for name, last := range p.trafficCursor {
			if values[name] < last {
				return nil, errors.New("final traffic counters are behind the child cursor")
			}
		}
	}
	cursor := make(map[string]int64, len(values))
	inbounds, outbounds := make(map[string]*Traffic), make(map[string]*Traffic)
	clients := make(map[string]*ClientTraffic)
	for name, value := range values {
		if final && value < 0 {
			return nil, errors.New("negative final traffic counter")
		}
		last := p.trafficCursor[name]
		if value < last {
			last = 0
		}
		cursor[name] = value
		delta := value - last
		if matches := trafficRegex.FindStringSubmatch(name); len(matches) == 4 {
			if matches[1] == "inbound" {
				processTraffic(matches, delta, inbounds)
			} else {
				processTraffic(matches, delta, outbounds)
			}
		} else if matches := clientTrafficRegex.FindStringSubmatch(name); len(matches) == 3 {
			processClientTraffic(matches, delta, clients)
		}
	}
	traffics := append(mapToSlice(inbounds), mapToSlice(outbounds)...)
	clientTraffics := mapToSlice(clients)
	return &pendingTrafficBatch{batch: TrafficBatch{
		ProcessID: p.trafficID, Sequence: p.trafficSequence + 1, ID: uuid.NewString(),
		Traffics: traffics, ClientTraffics: clientTraffics,
	}, cursor: cursor}, nil
}
