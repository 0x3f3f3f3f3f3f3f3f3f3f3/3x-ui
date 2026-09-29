package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// DesiredAmneziaWGInstances derives the AmneziaWG interfaces this panel
// should be running: one instance per enabled local AmneziaWG inbound,
// serving only the peers of clients that are both enabled in the inbound
// settings and not depletion-disabled in client_traffics. That is the same
// effective peer set buildInboundForLocalRuntime pushes on interactive edits,
// so the reconcile job and the push path agree on one fingerprint — see
// DesiredMtprotoInstances, which this mirrors exactly.
func (s *InboundService) DesiredAmneziaWGInstances() ([]amneziawg.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.AmneziaWG, true).
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(inbounds))
	for _, ib := range inbounds {
		ids = append(ids, ib.Id)
	}
	var disabledRows []xray.ClientTraffic
	err = db.Model(xray.ClientTraffic{}).
		Where("inbound_id IN ? AND enable = ?", ids, false).
		Select("inbound_id", "email").
		Find(&disabledRows).Error
	if err != nil {
		return nil, err
	}
	disabled := make(map[int]map[string]struct{}, len(disabledRows))
	for _, row := range disabledRows {
		if disabled[row.InboundId] == nil {
			disabled[row.InboundId] = map[string]struct{}{}
		}
		disabled[row.InboundId][row.Email] = struct{}{}
	}

	instances := make([]amneziawg.Instance, 0, len(inbounds))
	for _, ib := range inbounds {
		inst, ok := amneziawg.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		if off := disabled[ib.Id]; len(off) > 0 {
			kept := make([]amneziawg.Peer, 0, len(inst.Peers))
			for _, p := range inst.Peers {
				if _, skip := off[p.Email]; !skip {
					kept = append(kept, p)
				}
			}
			inst.Peers = kept
		}
		if len(inst.Peers) == 0 {
			continue
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// applyLocalAmneziaWG pushes a single local AmneziaWG inbound's current peer
// set to its interface right after a client edit commits, so an add,
// removal, re-key or enable-toggle takes effect immediately instead of
// waiting up to 10s for the reconcile job. It re-reads the inbound so it sees
// the committed settings, filters depleted clients exactly like the
// reconcile job, and is a no-op for node-owned or non-AmneziaWG inbounds.
// Failures are logged and swallowed: the reconcile job is the backstop.
// Mirrors applyLocalMtproto.
func (s *InboundService) applyLocalAmneziaWG(inboundId int) {
	inbound, err := s.GetInbound(inboundId)
	if err != nil || inbound == nil || inbound.Protocol != model.AmneziaWG || inbound.NodeID != nil {
		return
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return
	}
	payload := inbound
	if inbound.Enable {
		if built, bErr := s.buildInboundForLocalRuntime(database.GetDB(), inbound); bErr == nil {
			payload = built
		}
	}
	if err := rt.UpdateInbound(context.Background(), inbound, payload); err != nil {
		logger.Debugf("amneziawg: immediate apply failed for inbound %d: %v", inboundId, err)
	}
}

// defaultAmneziaWGServer builds a fresh server block: a random AmneziaWG 3.1
// obfuscation set, the default tunnel subnet/DNS, and a freshly generated
// keypair.
func defaultAmneziaWGServer() (*amneziawg.ServerSettings, error) {
	obf := amneziawg.GenerateObfuscation31()
	server := &amneziawg.ServerSettings{
		SubnetIP:     "10.8.1.0",
		SubnetCIDR:   24,
		PrimaryDNS:   "8.8.8.8",
		SecondaryDNS: "8.8.4.4",
		Jc:           obf.Jc,
		Jmin:         obf.Jmin,
		Jmax:         obf.Jmax,
		S1:           obf.S1,
		S2:           obf.S2,
		S3:           obf.S3,
		S4:           obf.S4,
		H1:           obf.H1,
		H2:           obf.H2,
		H3:           obf.H3,
		H4:           obf.H4,
		I1:           obf.I1,

		HeaderProtectionKey:    obf.HeaderProtectionKey,
		ContentPaddingAddition: obf.ContentPaddingAddition,
		RekeyAfterTime:         obf.RekeyAfterTime,
		RekeyTimeout:           obf.RekeyTimeout,
		RejectAfterTime:        obf.RejectAfterTime,
		KeepaliveTimeout:       obf.KeepaliveTimeout,
		MaxHandshakeAttempts:   obf.MaxHandshakeAttempts,
		RandomTrailers:         obf.RandomTrailers,
		DisableCookies:         obf.DisableCookies,
	}
	if err := fillAmneziaWGServerKeys(server); err != nil {
		return nil, err
	}
	return server, nil
}

// fillAmneziaWGServerKeys generates a real WireGuard-compatible keypair for
// the server block when one is missing.
func fillAmneziaWGServerKeys(server *amneziawg.ServerSettings) error {
	priv, pub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		return fmt.Errorf("amneziawg: generate server keypair: %w", err)
	}
	server.PrivateKey = priv
	server.PublicKey = pub
	return nil
}

// resolveAmneziaWGServerKeys settles the server keypair for a save. An omitted
// key means "unchanged", never "mint a new one": rotating it silently
// invalidates every client config already handed out.
func resolveAmneziaWGServerKeys(server *amneziawg.ServerSettings, oldSettings string) error {
	if server.PrivateKey == "" {
		storedPriv, storedPub := storedAmneziaWGServerKeys(oldSettings)
		if storedPriv == "" {
			return fillAmneziaWGServerKeys(server)
		}
		server.PrivateKey, server.PublicKey = storedPriv, storedPub
	}
	if server.PublicKey == "" {
		pub, err := wgutil.PublicKeyFromPrivate(server.PrivateKey)
		if err != nil {
			return fmt.Errorf("amneziawg: derive server public key: %w", err)
		}
		server.PublicKey = pub
	}
	return nil
}

// storedAmneziaWGServerKeys returns the keypair already saved for this inbound.
// oldSettings is empty on a first save, and need not be valid AmneziaWG JSON.
func storedAmneziaWGServerKeys(oldSettings string) (priv, pub string) {
	if strings.TrimSpace(oldSettings) == "" {
		return "", ""
	}
	var prev amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(oldSettings), &prev); err != nil || prev.Server == nil {
		return "", ""
	}
	return prev.Server.PrivateKey, prev.Server.PublicKey
}

// normalizeAmneziaWGSettings ensures an AmneziaWG inbound's settings have a
// valid server block, generating one (fresh obfuscation params + keypair) on
// first save and validating a manually-edited one so a bad entry can't bring
// the interface down on the next apply. A no-op for every other protocol.
func (s *InboundService) normalizeAmneziaWGSettings(inbound *model.Inbound, oldSettings string) error {
	if inbound.Protocol != model.AmneziaWG {
		return nil
	}

	trimmed := strings.TrimSpace(inbound.Settings)
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		server, err := defaultAmneziaWGServer()
		if err != nil {
			return err
		}
		settings := amneziawg.InboundSettings{Server: server, Clients: []model.Client{}}
		bs, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}
		inbound.Settings = string(bs)
		return nil
	}

	var parsed amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
		return fmt.Errorf("amneziawg: invalid settings: %w", err)
	}
	if parsed.Server == nil {
		server, err := defaultAmneziaWGServer()
		if err != nil {
			return err
		}
		parsed.Server = server
	} else if err := resolveAmneziaWGServerKeys(parsed.Server, oldSettings); err != nil {
		return err
	}
	parsed.Server.HeaderProtectionKey = strings.TrimSpace(parsed.Server.HeaderProtectionKey)
	for _, f := range []*string{
		&parsed.Server.ContentPaddingAddition, &parsed.Server.RekeyAfterTime,
		&parsed.Server.RekeyTimeout, &parsed.Server.RejectAfterTime,
		&parsed.Server.KeepaliveTimeout, &parsed.Server.MaxHandshakeAttempts,
	} {
		*f = amneziawg.CanonicalizeUintRange(*f)
	}
	if err := amneziawg.ValidateServerObfuscation(parsed.Server.Obfuscation()); err != nil {
		return fmt.Errorf("amneziawg: %w", err)
	}
	if err := amneziawg.ValidateIPv6Subnet(parsed.Server.IPv6Enabled, parsed.Server.IPv6Subnet); err != nil {
		return fmt.Errorf("amneziawg: %w", err)
	}
	if err := amneziawg.ValidateSubnetIPv4(parsed.Server.SubnetIP, parsed.Server.SubnetCIDR); err != nil {
		return fmt.Errorf("amneziawg: %w", err)
	}
	if err := amneziawg.ValidateInterfaceName(parsed.Server.ExternalInterface); err != nil {
		return fmt.Errorf("amneziawg: externalInterface: %w", err)
	}
	if err := amneziawg.ValidateInterfaceName(parsed.Server.IPv6ExternalInterface); err != nil {
		return fmt.Errorf("amneziawg: ipv6ExternalInterface: %w", err)
	}
	if err := amneziawg.ValidateConfigValue("privateKey", parsed.Server.PrivateKey); err != nil {
		return fmt.Errorf("amneziawg: %w", err)
	}
	if err := amneziawg.ValidateConfigValue("publicKey", parsed.Server.PublicKey); err != nil {
		return fmt.Errorf("amneziawg: %w", err)
	}
	signaturePackets := []struct{ field, v string }{
		{"i1", parsed.Server.I1},
		{"i2", parsed.Server.I2},
		{"i3", parsed.Server.I3},
		{"i4", parsed.Server.I4},
		{"i5", parsed.Server.I5},
	}
	for _, sp := range signaturePackets {
		if err := amneziawg.ValidateConfigValue(sp.field, sp.v); err != nil {
			return fmt.Errorf("amneziawg: %w", err)
		}
	}

	portCtx, err := s.loadPortConflictContext(database.GetDB())
	if err != nil {
		return err
	}
	portCtx.replaceInbound(inbound)
	for i := range parsed.Clients {
		c := &parsed.Clients[i]
		if err := s.amneziaWGForwardedPortsConflict(portCtx, c); err != nil {
			return err
		}
		if err := amneziawg.ValidateConfigValue("email", c.Email); err != nil {
			return fmt.Errorf("amneziawg: %w", err)
		}
		if err := amneziawg.ValidateConfigValue("publicKey", c.PublicKey); err != nil {
			return fmt.Errorf("amneziawg: client %q: %w", c.Email, err)
		}
		if err := amneziawg.ValidateConfigValue("preSharedKey", c.PreSharedKey); err != nil {
			return fmt.Errorf("amneziawg: client %q: %w", c.Email, err)
		}
		// AllowedIPs lands verbatim in a rendered [Peer] block, so a newline here
		// re-opens an [Interface] section whose PostUp runs as root once the
		// downloaded config is applied (client app, or awg-quick directly).
		normalized, err := normalizeWireguardAllowedIPs(c.AllowedIPs)
		if err != nil {
			return fmt.Errorf("amneziawg: client %q: %w", c.Email, err)
		}
		// An enabled peer with no address is skipped by InstanceFromInbound, and
		// if it was the only one the whole inbound never starts, silently.
		if c.Enable && len(normalized) == 0 {
			return fmt.Errorf("amneziawg: client %q: allowedIPs is required", c.Email)
		}
		c.AllowedIPs = normalized
	}

	bs, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return err
	}
	inbound.Settings = string(bs)
	return nil
}

// portConflictContext caches this host's listeners and managed reservations
// so a multi-client save does not repeat queries for each forwarded port.
type portConflictContext struct {
	webPort      int
	inbounds     []*model.Inbound
	reservations []*model.Inbound
}

// Disabled managed inbounds keep their bridge reservations for re-enablement.
func (s *InboundService) loadPortConflictContext(db *gorm.DB) (portConflictContext, error) {
	var ctx portConflictContext
	if webPort, err := (&SettingService{}).GetPort(); err == nil {
		ctx.webPort = webPort
	}
	err := db.Model(model.Inbound{}).
		Where("node_id IS NULL AND (enable = ? OR protocol IN ?)", true, []model.Protocol{model.SSH, model.Mieru, model.MTProto}).
		Find(&ctx.inbounds).Error
	if err != nil {
		return ctx, err
	}
	ctx.reservations, err = templateListenerReservationsTx(db)
	if err != nil {
		return ctx, err
	}
	upstreamPort, err := sshOutboundBridgePort()
	if err != nil {
		return ctx, err
	}
	ctx.reservations = append(ctx.reservations,
		&model.Inbound{Tag: "ssh-upstream", Listen: "127.0.0.1", Port: upstreamPort},
		&model.Inbound{Tag: "amneziawg-egress", Listen: "127.0.0.1", Port: amneziawgnet.EgressPort()},
	)
	return ctx, nil
}

// amneziaWGForwardedPortsConflict renders one client's ForwardedPorts collision,
// or nil: the single copy both the pre-Save pass and the post-Save re-run use.
func (s *InboundService) amneziaWGForwardedPortsConflict(ctx portConflictContext, c *model.Client) error {
	hit := s.checkForwardedPortsConflict(ctx, c.ForwardedPorts)
	if hit == "" {
		return nil
	}
	return fmt.Errorf("amneziawg: client %q forwardedPorts collides with %s", c.Email, hit)
}

// checkAmneziaWGForwardedPorts re-runs the guard over one row's stored clients:
// on create it ran before Save, when the row's own ports were not in the context.
func (s *InboundService) checkAmneziaWGForwardedPorts(db *gorm.DB, inbound *model.Inbound) error {
	var parsed amneziawg.InboundSettings
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
		return nil
	}
	ctx, err := s.loadPortConflictContext(db)
	if err != nil {
		return err
	}
	ctx.replaceInbound(inbound)
	for i := range parsed.Clients {
		if err := s.amneziaWGForwardedPortsConflict(ctx, &parsed.Clients[i]); err != nil {
			return err
		}
	}
	return checkAWGForwardOwnershipTx(db, inbound)
}

// checkForwardedPortsConflict names the panel, inbound or AmneziaWG relay port a
// client's ForwardedPorts spec would collide with: a lost bind race kills the relay.
func (s *InboundService) checkForwardedPortsConflict(ctx portConflictContext, forwardedPorts string) string {
	if forwardedPorts == "" {
		return ""
	}
	if amneziawg.ExceedsForwardedPortsCap(forwardedPorts) {
		return fmt.Sprintf("more than %d forwarded ports", amneziawg.MaxForwardedPorts)
	}
	if ctx.webPort > 0 && amneziawg.ForwardedPortsInclude(forwardedPorts, ctx.webPort) {
		return fmt.Sprintf("the panel's own port (%d)", ctx.webPort)
	}
	for _, reservation := range ctx.reservations {
		if amneziawg.ForwardedPortsInclude(forwardedPorts, reservation.Port) && listenOverlaps(inboundBindAddr(&model.Inbound{}), inboundBindAddr(reservation)) {
			return fmt.Sprintf("reserved listener %q (port %d)", reservation.Tag, reservation.Port)
		}
	}
	for _, ib := range ctx.inbounds {
		bridgePort, err := inboundRoutingBridgePort(ib)
		if err != nil {
			return fmt.Sprintf("invalid managed bridge reservation on inbound #%d", ib.Id)
		}
		if bridgePort != 0 && amneziawg.ForwardedPortsInclude(forwardedPorts, bridgePort) {
			return fmt.Sprintf("inbound '%s' (#%d)'s managed bridge port (%d)", ib.Tag, ib.Id, bridgePort)
		}
		if !ib.Enable && bridgePort != 0 {
			continue
		}
		if amneziawg.ForwardedPortsInclude(forwardedPorts, ib.Port) {
			name := ib.Remark
			if name == "" {
				name = ib.Tag
			}
			return fmt.Sprintf("inbound '%s' (#%d, port %d)", name, ib.Id, ib.Port)
		}
		if ib.Protocol != model.AmneziaWG {
			continue
		}
		socksPort := amneziawgnet.SOCKSPortForInbound(ib.Id)
		if amneziawg.ForwardedPortsInclude(forwardedPorts, socksPort) {
			name := ib.Remark
			if name == "" {
				name = ib.Tag
			}
			return fmt.Sprintf("inbound '%s' (#%d)'s own SOCKS5 relay port (%d)", name, ib.Id, socksPort)
		}
	}
	return ""
}

// GetAmneziaWGDiagnostics returns a live diagnostics snapshot for inbound
// id: interface up/down, listen port, and per-client handshake/traffic
// state, read entirely from data amneziawgnet.Manager already tracks --
// gathering it can never itself change anything. Returns an error only
// when id doesn't name an AmneziaWG inbound at all; an inbound that simply
// isn't running right now (disabled, no enabled clients, or reconcile
// hasn't caught up yet) comes back as amneziawgnet.Diagnostics{}
// (Running=false), not an error, since that's a normal state an admin
// might specifically be checking for.
func (s *InboundService) GetAmneziaWGDiagnostics(id int) (amneziawgnet.Diagnostics, error) {
	inbound, err := s.GetInbound(id)
	if err != nil {
		return amneziawgnet.Diagnostics{}, err
	}
	if inbound.Protocol != model.AmneziaWG {
		return amneziawgnet.Diagnostics{}, fmt.Errorf("inbound %d is not an AmneziaWG inbound", id)
	}
	inst, ok := amneziawg.InstanceFromInbound(inbound)
	if !ok {
		return amneziawgnet.Diagnostics{}, nil
	}
	return amneziawgnet.Diagnose(inst.Id, inst.Peers), nil
}
