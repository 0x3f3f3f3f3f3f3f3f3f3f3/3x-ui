package service

import (
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func snapshotMieruOnlineSessions(db *gorm.DB) []managedOnlineSession {
	mieruRuntimeState.Lock()
	defer mieruRuntimeState.Unlock()
	manager := mieruRuntimeState.manager
	if manager == nil || manager.db != db {
		return nil
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	process := currentXrayProcess()
	if process == nil || !process.IsRunning() {
		return nil
	}
	fingerprint := managedConfigFingerprint(process.GetConfig())
	if fingerprint != manager.expected && fingerprint != manager.pending {
		return nil
	}
	var sessions []managedOnlineSession
	for id, entry := range manager.entries {
		if entry.server == nil {
			continue
		}
		for _, session := range entry.server.OnlineSessions() {
			sessions = append(sessions, managedOnlineSession{PolicyID: session.PolicyID, Username: session.Username, SourceIP: session.SourceIP, inboundID: id, tag: entry.inbound.Tag})
		}
	}
	return sessions
}

func (s *InboundService) GetLocalMieruOnlineUsers() ([]xray.OnlineUser, []string, error) {
	db := database.GetDB()
	return localManagedOnlineUsers(db, model.Mieru, snapshotMieruOnlineSessions(db))
}
