package service

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type SSHRuntimeStatus struct {
	InboundID                int    `json:"inboundId" example:"1"`
	State                    string `json:"state" example:"running"`
	Reason                   string `json:"reason" example:""`
	AuthenticatedConnections int    `json:"authenticatedConnections" example:"2"`
}

func (s *InboundService) GetSSHRuntimeStatuses(userID int) ([]SSHRuntimeStatus, error) {
	var rows []struct {
		ID          int
		Enable      bool
		NodeID      *int
		ClientCount int
	}
	db := database.GetDB()
	err := db.Model(&model.Inbound{}).
		Select(`inbounds.id, inbounds.enable, inbounds.node_id,
			(SELECT COUNT(*) FROM client_inbounds ci JOIN clients c ON c.id = ci.client_id
			 WHERE ci.inbound_id = inbounds.id AND c.enable = ?) AS client_count`, true).
		Where("inbounds.user_id = ? AND inbounds.protocol = ?", userID, model.SSH).
		Order("inbounds.id").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	statuses := make([]SSHRuntimeStatus, 0, len(rows))
	observed := snapshotSSHRuntime(db)
	for _, row := range rows {
		status := SSHRuntimeStatus{InboundID: row.ID, State: "pending", Reason: "awaiting configuration"}
		actual, exists := observed[row.ID]
		switch {
		case row.NodeID != nil:
			status.State, status.Reason = "unsupported", "remote SSH runtime unavailable"
		case !row.Enable:
			status.State, status.Reason = "disabled", ""
			if actual.listening || actual.status.AuthenticatedConnections > 0 {
				status.State, status.Reason = "pending", "awaiting disable"
				status.AuthenticatedConnections = actual.status.AuthenticatedConnections
			}
		case row.ClientCount == 0:
			status.State, status.Reason = "idle", "no enabled clients"
			if actual.listening || actual.status.AuthenticatedConnections > 0 {
				status.State, status.Reason = "pending", "awaiting client update"
				status.AuthenticatedConnections = actual.status.AuthenticatedConnections
			}
		case exists:
			status = actual.status
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

type sshRuntimeObservation struct {
	status    SSHRuntimeStatus
	listening bool
}

func snapshotSSHRuntime(db *gorm.DB) map[int]sshRuntimeObservation {
	sshRuntimeState.Lock()
	defer sshRuntimeState.Unlock()
	manager := sshRuntimeState.manager
	if manager == nil || manager.db != db {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	process := currentXrayProcess()
	matched := false
	if process != nil && process.IsRunning() {
		fingerprint := managedConfigFingerprint(process.GetConfig())
		matched = fingerprint == manager.expected || fingerprint == manager.pending
	}
	observed := make(map[int]sshRuntimeObservation, len(manager.entries))
	for id, entry := range manager.entries {
		actual := sshRuntimeObservation{status: SSHRuntimeStatus{InboundID: id, State: "pending", Reason: "awaiting configuration"}}
		if entry.server != nil {
			server := entry.server.Status()
			actual.listening = server.Listening
			actual.status.AuthenticatedConnections = server.AuthenticatedConnections
		}
		switch {
		case entry.suspended:
		case !matched:
			actual.status.State, actual.status.Reason = "protected", "waiting for the applied Xray configuration"
		case entry.lastError != "":
			actual.status.State, actual.status.Reason = "protected", entry.lastError
		case actual.listening:
			actual.status.State, actual.status.Reason = "running", ""
		case entry.server != nil:
			actual.status.State, actual.status.Reason = "protected", "listener stopped"
		}
		observed[id] = actual
	}
	return observed
}
