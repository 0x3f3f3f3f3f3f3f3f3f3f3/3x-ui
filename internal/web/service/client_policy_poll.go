package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Instance and epoch checks reject stale replies; Runtime releases its RPC mutex before settlement.
func pollLocalClientPolicyLedger(ctx context.Context, process *xray.Process) error {
	var config conf.ClientPolicyConfig
	if err := json.Unmarshal(process.GetConfig().ClientPolicy, &config); err != nil {
		return err
	}
	var source model.ClientPolicySource
	if err := database.GetDB().Where("node_key = ? AND instance_id = ?", "local", config.InstanceID).First(&source).Error; err != nil {
		return err
	}
	if source.Sequence < 0 || source.Epoch <= 0 {
		return ErrClientPolicyLedger
	}
	manager := runtime.GetManager()
	if manager == nil {
		return errors.New("managed ledger requires Runtime")
	}
	rt, err := manager.RuntimeFor(nil)
	if err != nil {
		return err
	}
	managed, ok := rt.(runtime.ManagedProcessRuntime)
	if !ok {
		return errors.New("Runtime does not support managed ledger collection")
	}
	after := uint64(source.Sequence)
	for pageNumber := 0; pageNumber <= 100; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		caps, page, err := managed.ReadManagedLedger(ctx, process, after, pageNumber == 0)
		if err != nil {
			return err
		}
		if caps == nil || caps.InstanceId != config.InstanceID || caps.ApiVersion != 1 {
			return ErrClientPolicyLedger
		}
		if err := SettleClientPolicyLedger(config.InstanceID, caps.Epoch, after, page); err != nil {
			return err
		}
		after = page.NextSequence
		if len(page.Records) < 1000 {
			return nil
		}
	}
	return fmt.Errorf("%w: ledger collection exceeded its page budget; retry from the committed cursor", ErrClientPolicyLedger)
}
