package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/commander"
	"github.com/xtls/xray-core/infra/conf"
)

// StartManaged negotiates a control-only core and prepares usage before adding business listeners.
// The preparation callback must restore the panel's durable seed for every new identity.
func (p *Process) StartManaged(ctx context.Context, prepare func(context.Context, *ClientPolicyAPI) error) (err error) {
	if p.IsRunning() {
		return errors.New("xray is already running")
	}
	if prepare == nil {
		return fmt.Errorf("%w: managed preparation is required", ErrClientPolicyCapability)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	desired := p.GetConfig()
	data, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	var validate conf.Config
	if err := json.Unmarshal(data, &validate); err != nil {
		return err
	}
	if validate.ClientPolicy == nil || validate.API == nil {
		return fmt.Errorf("%w: missing policy or private API configuration", ErrClientPolicyCapability)
	}
	socket := validate.API.Listen
	if err := commander.ValidatePrivateUnixSocket(socket); err != nil {
		return err
	}
	services := make(map[string]bool)
	for _, service := range validate.API.Services {
		services[strings.ToLower(service)] = true
	}
	if !services["clientpolicyservicev1"] || !services["handlerservice"] {
		return fmt.Errorf("%w: policy and handler services are required", ErrClientPolicyCapability)
	}
	if _, err := validate.Build(); err != nil {
		return fmt.Errorf("managed configuration validation: %w", err)
	}
	bootstrap := *desired
	bootstrap.InboundConfigs = nil
	policyConfig := *validate.ClientPolicy
	policyConfig.Policies = nil
	bootstrap.ClientPolicy, err = json.Marshal(&policyConfig)
	if err != nil {
		return err
	}
	p.controlReady.Store(false)
	if err := p.startConfig(&bootstrap); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			p.controlReady.Store(false)
			err = errors.Join(err, p.Stop())
			p.setExitErr(err)
		}
	}()
	negotiationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	policyAPI, err := p.waitForPolicy(negotiationCtx, socket, policyConfig.InstanceID)
	cancel()
	if err != nil {
		return err
	}
	defer policyAPI.Close()
	if err := prepare(ctx, policyAPI); err != nil {
		return err
	}
	policies := make([]*clientpolicy.PolicyConfig, 0, len(validate.ClientPolicy.Policies))
	for _, policy := range validate.ClientPolicy.Policies {
		if _, err := policyAPI.GetClient(ctx, policy.ClientID); err != nil {
			return fmt.Errorf("managed client was not prepared: %w", err)
		}
		policies = append(policies, &clientpolicy.PolicyConfig{
			ClientId: policy.ClientID, Version: policy.Version,
			Enabled: policy.Enabled, MultiplierMicros: policy.Multiplier, QuotaBytes: policy.QuotaBytes,
			UploadBytesPerSecond: policy.UploadRate, DownloadBytesPerSecond: policy.DownloadRate,
			BurstBytes: policy.BurstBytes, ExpiresAt: policy.ExpiresAt,
		})
	}
	for len(policies) > 0 {
		n := min(len(policies), 1000)
		if err := policyAPI.Apply(ctx, policies[:n]); err != nil {
			return err
		}
		policies = policies[n:]
	}
	var api XrayAPI
	if err := api.InitEndpoint(socket); err != nil {
		return err
	}
	defer api.Close()
	for _, inbound := range desired.InboundConfigs {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(inbound)
		if err != nil {
			return err
		}
		if err := api.AddInbound(data); err != nil {
			return fmt.Errorf("managed listener activation: %w", err)
		}
	}
	p.controlReady.Store(true)
	return nil
}

func (p *Process) waitForPolicy(ctx context.Context, socket, instanceID string) (*ClientPolicyAPI, error) {
	for {
		attempt, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		api, err := DialClientPolicy(attempt, socket, instanceID)
		cancel()
		if err == nil || errors.Is(err, ErrClientPolicyCapability) {
			return api, err
		}
		if !p.IsRunning() {
			return nil, fmt.Errorf("managed core exited before negotiation: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("managed core negotiation: %w: %w", ctx.Err(), err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
