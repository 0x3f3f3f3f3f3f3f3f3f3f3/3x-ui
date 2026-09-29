package core

import (
	"context"
	"errors"
	"maps"
	"sort"
	"sync"

	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/features/stats"
)

var (
	ErrTrafficDrainUnsupported = errors.New("core does not support final traffic drain")
	ErrTrafficDrainBoot        = errors.New("traffic drain boot identity mismatch")
	ErrTrafficDrainPage        = errors.New("invalid final traffic counter page")
	ErrTrafficDrainCounterSize = errors.New("final counter name exceeds page byte limit")
)

type trafficDrainState struct {
	bootOnce sync.Once
	boot     string
	mu       sync.Mutex
	done     chan struct{}
	counters map[string]int64
	names    []string
	err      error
}

type (
	trafficDrainOwner interface{ CloseForTrafficDrain() error }
	trafficDrainStats interface {
		SealCounters(context.Context) (map[string]int64, error)
	}
)

// TrafficDrainBootID identifies this instance, independently of persistent policy epochs.
func (s *Instance) TrafficDrainBootID() string {
	s.trafficDrain.bootOnce.Do(func() { id := uuid.New(); s.trafficDrain.boot = id.String() })
	return s.trafficDrain.boot
}

func (s *Instance) trafficDrainFeatures() (trafficDrainOwner, trafficDrainOwner, trafficDrainStats) {
	in, _ := s.GetFeature(inbound.ManagerType()).(trafficDrainOwner)
	out, _ := s.GetFeature(outbound.ManagerType()).(trafficDrainOwner)
	counters, _ := s.GetFeature(stats.ManagerType()).(trafficDrainStats)
	return in, out, counters
}

// CanDrainTraffic requires permanent handler admission closure and an IO counter barrier.
func (s *Instance) CanDrainTraffic() bool {
	in, out, counters := s.trafficDrainFeatures()
	return in != nil && out != nil && counters != nil
}

// DrainTraffic owns one irreversible operation; cancellation only stops this caller's wait.
func (s *Instance) DrainTraffic(ctx context.Context, expectedBoot string) (map[string]int64, error) {
	d, err := s.waitTrafficDrain(ctx, expectedBoot, true)
	if err != nil {
		return nil, err
	}
	return maps.Clone(d.counters), nil
}

// DrainTrafficPage reads the immutable final snapshot in bounded lexical pages.
func (s *Instance) DrainTrafficPage(ctx context.Context, expectedBoot, after string, limit uint32) (map[string]int64, string, error) {
	if limit > 1000 {
		return nil, "", ErrTrafficDrainPage
	}
	if limit == 0 {
		limit = 1000
	}
	d, err := s.waitTrafficDrain(ctx, expectedBoot, after == "")
	if err != nil {
		return nil, "", err
	}
	index := 0
	if after != "" {
		index = sort.SearchStrings(d.names, after)
		if index == len(d.names) || d.names[index] != after {
			return nil, "", ErrTrafficDrainPage
		}
		index++
	}
	page := make(map[string]int64)
	bytes := 128
	last := ""
	for index < len(d.names) && len(page) < int(limit) {
		name := d.names[index]
		cost := len(name) + 32
		if bytes+cost+len(name)+8 > 1<<20 {
			if len(page) == 0 {
				return nil, "", ErrTrafficDrainCounterSize
			}
			break
		}
		page[name] = d.counters[name]
		bytes += cost
		last = name
		index++
	}
	if index == len(d.names) {
		last = ""
	}
	return page, last, nil
}

func (s *Instance) waitTrafficDrain(ctx context.Context, expectedBoot string, mayStart bool) (*trafficDrainState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expectedBoot == "" || expectedBoot != s.TrafficDrainBootID() {
		return nil, ErrTrafficDrainBoot
	}
	in, out, counters := s.trafficDrainFeatures()
	if in == nil || out == nil || counters == nil {
		return nil, ErrTrafficDrainUnsupported
	}
	d := &s.trafficDrain
	d.mu.Lock()
	if d.done == nil {
		if !mayStart {
			d.mu.Unlock()
			return nil, ErrTrafficDrainPage
		}
		if err := ctx.Err(); err != nil {
			d.mu.Unlock()
			return nil, err
		}
		d.done = make(chan struct{})
		go d.run(in, out, counters)
	}
	done := d.done
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if d.err != nil {
			return nil, d.err
		}
		return d, nil
	}
}

func (d *trafficDrainState) run(in, out trafficDrainOwner, counters trafficDrainStats) {
	defer close(d.done)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var owners sync.WaitGroup
	owners.Add(2)
	for _, owner := range []trafficDrainOwner{in, out} {
		go func() {
			defer owners.Done()
			if err := owner.CloseForTrafficDrain(); err != nil {
				cancel(err)
			}
		}()
	}
	closed := make(chan struct{})
	go func() { owners.Wait(); close(closed) }()
	values, err := counters.SealCounters(ctx)
	if err != nil {
		d.err = context.Cause(ctx)
		if d.err == nil {
			d.err = err
		}
		return
	}
	select {
	case <-ctx.Done():
		d.err = context.Cause(ctx)
		return
	case <-closed:
		if err := context.Cause(ctx); err != nil {
			d.err = err
			return
		}
	}
	d.counters = values
	d.names = make([]string, 0, len(values))
	for name := range values {
		d.names = append(d.names, name)
	}
	sort.Strings(d.names)
}
