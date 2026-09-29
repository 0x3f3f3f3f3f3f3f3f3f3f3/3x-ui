package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

type trafficLocalApplyAction uint8

const (
	trafficAddUser trafficLocalApplyAction = iota + 1
	trafficRemoveUser
	trafficDisableInbound
)

type trafficLocalApplyPlan struct {
	action  trafficLocalApplyAction
	inbound model.Inbound
	client  map[string]any
	email   string
}

type trafficMutationBatch struct {
	localPlans  []trafficLocalApplyPlan
	remotePlans []trafficInboundUpdatePlan
	nodeIDs     map[int]struct{}
}

type trafficInboundUpdatePlan struct{ oldInbound, newInbound model.Inbound }

func newTrafficMutationBatch() *trafficMutationBatch {
	return &trafficMutationBatch{nodeIDs: make(map[int]struct{})}
}

func (b *trafficMutationBatch) addNode(nodeID int) {
	if nodeID > 0 {
		b.nodeIDs[nodeID] = struct{}{}
	}
}

func (b *trafficMutationBatch) markNodesTx(tx *gorm.DB) error {
	if b == nil {
		return nil
	}
	nodeSvc := NodeService{}
	for nodeID := range b.nodeIDs {
		if err := nodeSvc.MarkNodeDirtyTx(tx, nodeID); err != nil {
			return err
		}
	}
	return nil
}

// applyTrafficRemotePlans is bounded like every per-client node push: the nodes are
// already dirty, so an offline or slow one defers to the reconcile.
func (s *InboundService) applyTrafficRemotePlans(plans []trafficInboundUpdatePlan) bool {
	ids := make([]int, len(plans))
	for i := range plans {
		ids[i] = plans[i].newInbound.Id
	}
	failed, panics := fanoutInboundResults(ids, inboundFanoutConcurrency, func(i int) bool {
		rt, push, _, err := s.nodePushPlan(&plans[i].newInbound)
		if err == nil && push {
			ctx, cancel := nodePushContext()
			err = rt.UpdateInbound(ctx, &plans[i].oldInbound, &plans[i].newInbound)
			cancel()
		}
		if err != nil {
			logger.Debug("traffic post-commit remote apply failed:", err)
		}
		return err != nil
	})
	needRestart := false
	for i := range failed {
		needRestart = needRestart || failed[i] || panics[i] != nil
	}
	return needRestart
}

func (s *InboundService) applyTrafficMutationBatch(b *trafficMutationBatch) bool {
	if b == nil || len(b.localPlans) == 0 {
		return false
	}
	if handled, err := s.reconcileManagedChange(&b.localPlans[0].inbound); handled || err != nil {
		if err != nil {
			logger.Debug("traffic managed reconciliation failed:", err)
		}
		return err != nil
	}
	byInbound := make(map[int][]trafficLocalApplyPlan)
	var ids []int
	for _, plan := range b.localPlans {
		if _, exists := byInbound[plan.inbound.Id]; !exists {
			ids = append(ids, plan.inbound.Id)
		}
		byInbound[plan.inbound.Id] = append(byInbound[plan.inbound.Id], plan)
	}
	needRestart := false
	for _, id := range ids {
		if err := s.applyTrafficLocalPlans(id, byInbound[id]); err != nil {
			logger.Debug("traffic post-commit runtime apply failed:", err)
			needRestart = true
		}
	}
	return needRestart
}

func (s *InboundService) applyTrafficLocalPlans(inboundID int, plans []trafficLocalApplyPlan) error {
	defer lockInbound(inboundID).Unlock()
	inbound, err := s.GetInbound(inboundID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if inbound.NodeID != nil {
		return nil
	}
	valid := plans[:0]
	var emails []string
	hasAdd := false
	for _, plan := range plans {
		if inbound.Tag != plan.inbound.Tag || inbound.Protocol != plan.inbound.Protocol {
			continue
		}
		if plan.action == trafficDisableInbound {
			if inbound.Enable {
				continue
			}
		} else {
			if !inbound.Enable {
				continue
			}
			email := plan.email
			if plan.action == trafficAddUser {
				email, _ = plan.client["email"].(string)
				hasAdd = true
			}
			emails = append(emails, email)
		}
		valid = append(valid, plan)
	}
	if len(valid) == 0 {
		return nil
	}
	// The inbound lock spans the fresh read and runtime calls, outside the writer.
	switch inbound.Protocol {
	case model.MTProto:
		s.applyLocalMtproto(inbound.Id)
		return nil
	case model.AmneziaWG:
		s.applyLocalAmneziaWG(inbound.Id)
		return nil
	case model.TUIC:
		s.applyLocalTuic(inbound.Id)
		return nil
	}
	current := make(map[string]model.Client, len(emails))
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		clients, err := s.clientService.ListForInbound(database.GetDB().Where("clients.email IN ?", batch), inbound.Id)
		if err != nil {
			return err
		}
		for _, client := range clients {
			current[client.Email] = client
		}
	}
	var settings struct {
		Method string `json:"method"`
	}
	if hasAdd {
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return err
		}
	}
	if hasAdd && inbound.Protocol == model.WireGuard {
		peers, err := ParseInboundSettingsClients(inbound.Settings)
		if err != nil {
			return err
		}
		byEmail := make(map[string]model.Client, len(peers))
		for _, peer := range peers {
			byEmail[strings.ToLower(strings.TrimSpace(peer.Email))] = peer
		}
		for email, client := range current {
			if peer, exists := byEmail[strings.ToLower(strings.TrimSpace(email))]; exists {
				client.AllowedIPs, client.PreSharedKey = peer.AllowedIPs, peer.PreSharedKey
				current[email] = client
			}
		}
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return err
	}
	var failures []error
	for _, plan := range valid {
		switch plan.action {
		case trafficAddUser:
			email, _ := plan.client["email"].(string)
			client, exists := current[email]
			if !exists || !client.Enable {
				continue
			}
			raw, err := json.Marshal(client)
			if err != nil {
				return err
			}
			var user map[string]any
			if err := json.Unmarshal(raw, &user); err != nil {
				return err
			}
			user["reverse"] = client.Reverse
			err = rt.AddUser(context.Background(), inbound, apiUserFromClient(user, settings.Method))
			if err != nil {
				failures = append(failures, err)
			}
		case trafficRemoveUser:
			if client, exists := current[plan.email]; exists && client.Enable {
				continue
			}
			err = rt.RemoveUser(context.Background(), inbound, plan.email)
			if err != nil && !strings.Contains(err.Error(), "not found") {
				failures = append(failures, err)
			}
		case trafficDisableInbound:
			err = rt.DelInbound(context.Background(), inbound)
			if err != nil && !xray.IsMissingHandlerErr(err) {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
