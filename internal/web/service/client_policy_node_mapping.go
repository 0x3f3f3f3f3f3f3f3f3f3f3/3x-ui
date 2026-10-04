package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ClientPolicyNodeMappingService struct {
	db      *gorm.DB
	journal *policyauthority.Journal
}

func NewClientPolicyNodeMappingService(db *gorm.DB, journal *policyauthority.Journal) (*ClientPolicyNodeMappingService, error) {
	if db == nil || journal == nil {
		return nil, ErrClientPolicyLedger
	}
	return &ClientPolicyNodeMappingService{db: db, journal: journal}, nil
}

func (s *ClientPolicyNodeMappingService) Enroll(ctx context.Context, api *panelruntime.RemoteAuthorityAPI, request panelruntime.NodeClientMappingRequest) (*panelruntime.NodeClientMappingResult, error) {
	if api == nil || ctx == nil || request.Validate() != nil {
		return nil, ErrClientPolicyLedger
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.withCurrent(ctx, func(tx *gorm.DB) error { return s.currentPolicy(tx, request) }); err != nil {
		return nil, err
	}
	// SQLite uses immediate transactions. Release their write lock before the
	// peer RPC, then revalidate everything under admission before committing.
	proof, err := api.EnrollClientMapping(ctx, request)
	if err != nil {
		return nil, err
	}
	var result *panelruntime.NodeClientMappingResult
	err = s.withCurrent(ctx, func(tx *gorm.DB) error {
		if err := s.currentPolicy(tx, request); err != nil {
			return err
		}
		if err := s.journal.RecordClientMapping(policyauthority.ClientMappingCoordinator, proof.Mapping); err != nil {
			return err
		}
		if err := s.currentPolicy(tx, request); err != nil {
			return err
		}
		raw, err := json.Marshal(proof.Mapping)
		if err != nil {
			return err
		}
		row := model.ClientPolicyNodeMapping{AuthorityID: proof.Mapping.Authority.AuthorityID, Generation: int64(proof.Mapping.Authority.Generation), SourceID: proof.Mapping.SourceID, LocalClientID: proof.Mapping.LocalClientID, GlobalClientID: proof.Mapping.GlobalClientID, NodeID: proof.Mapping.NodeID, MappingJSON: string(raw)}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "authority_id"}, {Name: "generation"}, {Name: "source_id"}, {Name: "local_client_id"}}, DoUpdates: clause.AssignmentColumns([]string{"global_client_id", "node_id", "mapping_json"})}).Create(&row).Error; err != nil {
			return err
		}
		result = proof
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ClientPolicyNodeMappingService) AuthorityAPI(ctx context.Context, api *panelruntime.RemoteAuthorityAPI) (*panelruntime.MappedRemoteAuthorityAPI, error) {
	if api == nil || api.Capabilities() == nil {
		return nil, ErrClientPolicyLedger
	}
	var mappings []policyauthority.ClientMapping
	err := s.withCurrent(ctx, func(tx *gorm.DB) error {
		caps := api.Capabilities()
		cursor := ""
		for {
			page, err := s.journal.ClientMappings(policyauthority.ClientMappingCoordinator, cursor, 128)
			if err != nil {
				return err
			}
			for _, m := range page {
				if m.SourceID != caps.InstanceId {
					continue
				}
				request := panelruntime.NodeClientMappingRequest{Binding: panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: caps.InstanceId, ExpectedBootID: caps.BootId, AuthorityID: m.Authority.AuthorityID, Generation: m.Authority.Generation, NodeID: m.NodeID}, GlobalClientID: m.GlobalClientID, LocalClientID: m.LocalClientID, GlobalPolicyVersion: m.GlobalPolicyVersion, LocalPolicyVersion: m.LocalPolicyVersion, ExpectedPolicyDigest: m.PolicyDigest}
				if err := s.currentPolicy(tx, request); err != nil {
					return err
				}
				mappings = append(mappings, m)
			}
			if len(page) < 128 {
				return nil
			}
			cursor = policyauthority.ClientMappingCursor(page[len(page)-1])
		}
	})
	if err != nil {
		return nil, err
	}
	return panelruntime.NewMappedRemoteAuthorityAPI(api, mappings)
}

func (s *ClientPolicyNodeMappingService) withCurrent(ctx context.Context, operation func(*gorm.DB) error) error {
	if s == nil || s.db == nil || s.journal == nil || ctx == nil {
		return ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return database.WithCurrentDB(s.db, func(current *gorm.DB) error {
		return database.WithConnection(current.WithContext(ctx), func(connection *gorm.DB) error { return connection.Transaction(operation) })
	})
}

func (s *ClientPolicyNodeMappingService) currentPolicy(tx *gorm.DB, request panelruntime.NodeClientMappingRequest) error {
	identity := s.journal.Identity()
	if request.Validate() != nil || identity.AuthorityID != request.Binding.AuthorityID || identity.Generation != request.Binding.Generation {
		return ErrClientPolicyLedger
	}
	account, err := s.journal.Account(request.GlobalClientID)
	if err != nil {
		return err
	}
	if account.Deleted || account.Policy.Version != request.GlobalPolicyVersion {
		return ErrClientPolicyLedger
	}
	global := request
	global.LocalClientID, global.LocalPolicyVersion = request.GlobalClientID, request.GlobalPolicyVersion
	policy, err := mappingDesiredPolicy(tx, global)
	if err != nil {
		return err
	}
	digest, err := panelruntime.EffectiveClientPolicyDigest(policy)
	if err != nil || digest != request.ExpectedPolicyDigest || account.Policy.QuotaBytes != policy.QuotaBytes || account.Policy.QuotaUnlimited != (policy.QuotaBytes == 0) {
		return ErrClientPolicyLedger
	}
	for _, direction := range []struct {
		actual policyauthority.Direction
		rate   uint64
	}{{account.Policy.Upload, policy.UploadBytesPerSecond}, {account.Policy.Download, policy.DownloadBytesPerSecond}} {
		wanted := policyauthority.Direction{Unlimited: true}
		if direction.rate != 0 {
			wanted = policyauthority.Direction{Rate: direction.rate, Burst: policy.BurstBytes}
		}
		if wanted != direction.actual {
			return ErrClientPolicyLedger
		}
	}
	return nil
}
