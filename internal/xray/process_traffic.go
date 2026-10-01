package xray

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	statscommand "github.com/xtls/xray-core/app/stats/command"
)

// TrafficBatch is stable across retries, including a lost SQL commit acknowledgement.
type TrafficBatch struct {
	ProcessID        string
	Sequence         int64
	ID               string
	Final            bool
	Traffics         []*Traffic
	ClientTraffics   []*ClientTraffic
	SourceMode       string
	SourceInstanceID string
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
	if p.trafficDraining || p.trafficOwners != nil {
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
	mode, instanceID, err := p.trafficSourceAccounting()
	if err != nil {
		return nil, err
	}
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
		Traffics: traffics, ClientTraffics: clientTraffics, Final: final,
		SourceMode: mode, SourceInstanceID: instanceID,
	}, cursor: cursor}, nil
}

// Snapshot before entering the SQL callback; a retry retains this batch even
// when the current process or its stored configuration later changes.
func (p *Process) trafficSourceAccounting() (string, string, error) {
	config := p.GetConfig()
	if config == nil {
		return "unknown", "", nil
	}
	if len(config.ClientPolicy) == 0 || strings.TrimSpace(string(config.ClientPolicy)) == "null" {
		return "legacy", "", nil
	}
	var policy struct {
		InstanceID string `json:"instanceId"`
	}
	if err := json.Unmarshal(config.ClientPolicy, &policy); err != nil || !utf8.Valid(config.ClientPolicy) || policy.InstanceID == "" || len(policy.InstanceID) > 36 {
		return "", "", errors.New("invalid managed traffic source accounting")
	}
	return "managed", policy.InstanceID, nil
}
