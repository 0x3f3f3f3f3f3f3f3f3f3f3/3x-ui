package service

import (
	"net/netip"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type managedOnlineSession struct {
	PolicyID  string
	Username  string
	SourceIP  netip.Addr
	inboundID int
	tag       string
}

func localManagedOnlineUsers(db *gorm.DB, protocol model.Protocol, sessions []managedOnlineSession) ([]xray.OnlineUser, []string, error) {
	if len(sessions) == 0 {
		return nil, nil, nil
	}
	policyIDs := make([]string, 0, len(sessions))
	for _, session := range sessions {
		policyIDs = append(policyIDs, session.PolicyID)
	}
	type membership struct {
		PolicyID  string
		Email     string
		InboundID int
		Tag       string
	}
	current := make(map[membership]bool)
	for _, batch := range chunkStrings(uniqueNonEmptyStrings(policyIDs), sqlInChunk) {
		var rows []membership
		err := db.Table("clients").Select("clients.policy_id, clients.email, ci.inbound_id, inbounds.tag").
			Joins("JOIN client_inbounds ci ON ci.client_id = clients.id").
			Joins("JOIN inbounds ON inbounds.id = ci.inbound_id").
			Where("clients.policy_id IN ? AND inbounds.protocol = ? AND inbounds.node_id IS NULL", batch, protocol).
			Scan(&rows).Error
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			current[row] = true
		}
	}
	ipsByEmail := make(map[string]map[string]bool)
	tags := make(map[string]bool)
	for _, session := range sessions {
		if !current[membership{PolicyID: session.PolicyID, Email: session.Username, InboundID: session.inboundID, Tag: session.tag}] {
			continue
		}
		if ipsByEmail[session.Username] == nil {
			ipsByEmail[session.Username] = make(map[string]bool)
		}
		ipsByEmail[session.Username][session.SourceIP.String()] = true
		tags[session.tag] = true
	}
	now := time.Now().Unix()
	users := make([]xray.OnlineUser, 0, len(ipsByEmail))
	for email, ips := range ipsByEmail {
		user := xray.OnlineUser{Email: email}
		for ip := range ips {
			user.IPs = append(user.IPs, xray.OnlineIP{IP: ip, LastSeen: now})
		}
		sort.Slice(user.IPs, func(i, j int) bool { return user.IPs[i].IP < user.IPs[j].IP })
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Email < users[j].Email })
	activeTags := make([]string, 0, len(tags))
	for tag := range tags {
		activeTags = append(activeTags, tag)
	}
	sort.Strings(activeTags)
	return users, activeTags, nil
}
