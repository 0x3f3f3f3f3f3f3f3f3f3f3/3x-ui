package service

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

func (s *ClientService) ResetTrafficByEmail(inboundSvc *InboundService, email string) (bool, error) {
	return s.ResetTrafficByEmailWithRequest(context.Background(), inboundSvc, email, ClientTrafficResetRequest{})
}

func (s *ClientService) ResetTrafficByEmailWithRequest(ctx context.Context, inboundSvc *InboundService, email string, request ClientTrafficResetRequest) (bool, error) {
	if email == "" {
		return false, common.NewError("client email is required")
	}
	if request.RequestID != "" && !validPolicySourceKey(request.RequestID) {
		return false, ErrClientPolicyLedger
	}
	if request.RequestID != "" && request.ClientID == "" {
		return false, errors.New("clientId is required with requestId")
	}
	if request.RequestID == "" {
		request.RequestID = uuid.NewString()
	}
	rec, err := s.GetRecordByEmail(database.GetDB().WithContext(ctx), email)
	if err != nil {
		return false, err
	}
	if request.ClientID != "" && request.ClientID != rec.StableID {
		return false, errors.New("client identity changed; reload before resetting traffic")
	}
	if handled, err := resetPreparedClientPolicy(ctx, rec.StableID, request.RequestID); handled || err != nil {
		return false, err
	}
	inboundIds, err := s.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		return false, err
	}

	needRestart := false
	if len(inboundIds) == 0 {
		if rErr := inboundSvc.resetLegacyClientTrafficByEmail(email, rec.StableID); rErr != nil {
			return false, rErr
		}
	} else {
		applies := make([]inboundApply, 0, len(inboundIds))
		for _, ibId := range inboundIds {
			applies = append(applies, inboundApply{id: ibId, run: func() (bool, error) {
				return inboundSvc.resetLegacyClientTraffic(ibId, email, rec.StableID)
			}})
		}
		nr, applyErr := fanoutInboundApplies(applies)
		if applyErr != nil {
			return nr, applyErr
		}
		needRestart = nr
	}

	// Enable only once the counters are zero: a still-depleted client enabled
	// first is switched off again by the next traffic tick.
	if !rec.Enable {
		updated := rec.ToClient()
		updated.Enable = true
		nr, uErr := s.Update(inboundSvc, rec.Id, *updated, rec.LimitHwid)
		if uErr != nil {
			logger.Warning("Failed to auto-enable client during traffic reset:", uErr)
		}
		if nr {
			needRestart = true
		}
	}
	return needRestart, nil
}

func (s *ClientService) BulkResetTraffic(inboundSvc *InboundService, emails []string) (int, error) {
	return s.BulkResetTrafficWithRequest(context.Background(), inboundSvc, emails, "")
}

func (s *ClientService) ResetAllClientTraffics(inboundSvc *InboundService, id int) error {
	return s.ResetAllClientTrafficsWithRequest(context.Background(), inboundSvc, id, "")
}

func (s *ClientService) ResetAllTraffics() (bool, error) {
	return s.ResetAllTrafficsWithRequest(context.Background(), "")
}
