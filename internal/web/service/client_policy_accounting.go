package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/app/clientpolicy"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type clientPolicyAccountingRow struct {
	model.ClientPolicyReceipt
	Email          string
	DesiredVersion int64
	QuotaBytes     int64
	SourceEpoch    int64
	SourceSequence int64
	SourceCount    int64
	HasRemote      bool
	TotalClientID  string
	TotalUpload    int64
	TotalDownload  int64
	TotalBilled    int64
	TotalUncertain int64
	Reset          model.ClientPolicyReset `gorm:"embedded;embeddedPrefix:reset_"`
	LatestReset    model.ClientPolicyReset `gorm:"embedded;embeddedPrefix:latest_reset_"`
	DesiredClient  model.ClientRecord      `gorm:"embedded;embeddedPrefix:desired_"`
}

const clientPolicyAccountingQuery = `
SELECT p.*, c.email, c.desired_policy_version AS desired_version, c.total_gb AS quota_bytes,
 c.stable_id AS desired_stable_id, c.enable AS desired_enable,
 c.total_gb AS desired_total_gb, c.expiry_time AS desired_expiry_time,
 c.policy_upload_bytes_per_second AS desired_policy_upload_bytes_per_second,
 c.policy_download_bytes_per_second AS desired_policy_download_bytes_per_second,
 c.policy_multiplier AS desired_policy_multiplier, c.policy_fingerprint AS desired_policy_fingerprint,
 c.desired_policy_version AS desired_desired_policy_version,
 s.epoch AS source_epoch, s.sequence AS source_sequence,
 t.client_id AS total_client_id, t.raw_upload AS total_upload, t.raw_download AS total_download,
 t.billed_bytes AS total_billed, t.uncertain_bytes AS total_uncertain,
 r.id AS reset_id, r.client_id AS reset_client_id, r.request_id AS reset_request_id,
 r.instance_id AS reset_instance_id, r.epoch AS reset_epoch, r.sequence AS reset_sequence,
 r.raw_upload AS reset_raw_upload, r.raw_download AS reset_raw_download,
 r.billed_bytes AS reset_billed_bytes, r.remainder AS reset_remainder,
 r.uncertain_bytes AS reset_uncertain_bytes, r.policy_version AS reset_policy_version,
 latest.id AS latest_reset_id, latest.policy_version AS latest_reset_policy_version,
 latest.instance_id AS latest_reset_instance_id, latest.client_id AS latest_reset_client_id,
 latest.request_id AS latest_reset_request_id, latest.epoch AS latest_reset_epoch,
 latest.sequence AS latest_reset_sequence, latest.raw_upload AS latest_reset_raw_upload,
 latest.raw_download AS latest_reset_raw_download, latest.billed_bytes AS latest_reset_billed_bytes,
 latest.remainder AS latest_reset_remainder, latest.uncertain_bytes AS latest_reset_uncertain_bytes,
 (SELECT COUNT(*) FROM client_policy_receipts other WHERE other.client_id = p.client_id) AS source_count,
 EXISTS (SELECT 1 FROM client_inbounds ci JOIN inbounds i ON i.id = ci.inbound_id
  WHERE ci.client_id = c.id AND i.node_id IS NOT NULL) AS has_remote
FROM clients c
JOIN client_policy_receipts p ON p.client_id = c.stable_id
JOIN client_policy_sources s ON s.instance_id = p.instance_id AND s.node_key = 'local'
LEFT JOIN client_policy_totals t ON t.client_id = p.client_id
LEFT JOIN client_policy_resets r ON r.id = (
 SELECT MAX(id) FROM client_policy_resets WHERE client_id = p.client_id AND policy_version <= p.policy_version
)
LEFT JOIN client_policy_resets latest ON latest.id = (
 SELECT MAX(id) FROM client_policy_resets WHERE client_id = p.client_id
)
WHERE c.email IN ? AND p.policy_version > 0`

func overlayClientPolicyAccounting(tx *gorm.DB, rows []*xray.ClientTraffic) error {
	byEmail := make(map[string][]*xray.ClientTraffic, len(rows))
	var emails []string
	for _, row := range rows {
		if row == nil || row.Email == "" {
			continue
		}
		row.Accounting = nil
		if _, ok := byEmail[row.Email]; !ok {
			emails = append(emails, row.Email)
		}
		byEmail[row.Email] = append(byEmail[row.Email], row)
	}
	for _, batch := range chunkStrings(emails, sqlInChunk) {
		var records []clientPolicyAccountingRow
		if err := tx.Raw(clientPolicyAccountingQuery, batch).Scan(&records).Error; err != nil {
			return err
		}
		for _, record := range records {
			accounting, err := projectClientPolicyAccounting(record)
			if err != nil {
				return err
			}
			for _, row := range byEmail[record.Email] {
				row.Accounting = accounting
			}
		}
	}
	return nil
}

func overlayClientPolicyAccountingValues(tx *gorm.DB, rows []xray.ClientTraffic) error {
	pointers := make([]*xray.ClientTraffic, len(rows))
	for i := range rows {
		pointers[i] = &rows[i]
	}
	return overlayClientPolicyAccounting(tx, pointers)
}

func projectClientPolicyAccounting(row clientPolicyAccountingRow) (*xray.ClientPolicyAccounting, error) {
	if row.SourceCount != 1 || row.HasRemote || row.TotalClientID != row.ClientID || row.Epoch <= 0 || row.Epoch > row.SourceEpoch || row.Sequence <= 0 || row.Sequence > row.SourceSequence || row.PolicyVersion <= 0 || row.PolicyVersion > row.DesiredVersion || row.Remainder < 0 || row.Remainder >= int64(clientpolicy.MultiplierScale) {
		return nil, ErrClientPolicyLedger
	}
	if row.RawUpload < 0 || row.RawDownload < 0 || row.BilledBytes < 0 || row.UncertainBytes < 0 || row.TotalUpload != row.RawUpload || row.TotalDownload != row.RawDownload || row.TotalBilled != row.BilledBytes || row.TotalUncertain != row.UncertainBytes {
		return nil, ErrClientPolicyLedger
	}
	if row.QuotaBytes < 0 || row.BilledBytes > math.MaxInt64-row.UncertainBytes {
		return nil, ErrClientPolicyLedger
	}
	if row.LatestReset.Id != 0 && (row.LatestReset.PolicyVersion <= 0 || row.LatestReset.PolicyVersion > row.DesiredVersion || row.LatestReset.InstanceID != row.InstanceID) {
		return nil, ErrClientPolicyLedger
	}
	if row.Reset.Id != 0 {
		if err := validateClientPolicyReset(&row.Reset); err != nil {
			return nil, err
		}
		if row.Reset.ClientID != row.ClientID || row.Reset.InstanceID != row.InstanceID || row.Reset.PolicyVersion > row.PolicyVersion || row.Reset.Epoch > row.Epoch || row.Reset.Sequence > row.Sequence {
			return nil, ErrClientPolicyLedger
		}
	}
	base := row.Reset
	if row.RawUpload < base.RawUpload || row.RawDownload < base.RawDownload || row.UncertainBytes < base.UncertainBytes || row.BilledBytes < base.BilledBytes || row.BilledBytes == base.BilledBytes && row.Remainder < base.Remainder {
		return nil, ErrClientPolicyLedger
	}
	billed, remainder := row.BilledBytes-base.BilledBytes, row.Remainder-base.Remainder
	if remainder < 0 {
		billed--
		remainder += int64(clientpolicy.MultiplierScale)
	}
	uncertain := row.UncertainBytes - base.UncertainBytes
	var remaining *string
	if row.QuotaBytes > 0 {
		whole := row.QuotaBytes - billed - uncertain
		amount := "0"
		if whole > 0 {
			fraction := int64(0)
			if remainder > 0 {
				whole--
				fraction = int64(clientpolicy.MultiplierScale) - remainder
			}
			amount = formatClientPolicyBilled(whole, fraction)
		}
		remaining = &amount
	}
	var latestReset *model.ClientPolicyReset
	if row.LatestReset.Id != 0 {
		latestReset = &row.LatestReset
	}
	_, fingerprint, desiredErr := fingerprintClientPolicy(row.DesiredClient, latestReset)
	policyPending := desiredErr != nil || fingerprint != row.DesiredClient.PolicyFingerprint || row.PolicyVersion != row.DesiredVersion
	return &xray.ClientPolicyAccounting{
		ClientID:   row.ClientID,
		Lifetime:   formatClientPolicyUsage(row.RawUpload, row.RawDownload, row.BilledBytes, row.Remainder, row.UncertainBytes),
		Period:     formatClientPolicyUsage(row.RawUpload-base.RawUpload, row.RawDownload-base.RawDownload, billed, remainder, uncertain),
		QuotaBytes: strconv.FormatInt(row.QuotaBytes, 10), Remaining: remaining,
		AppliedVersion: strconv.FormatInt(row.PolicyVersion, 10), DesiredVersion: strconv.FormatInt(row.DesiredVersion, 10),
		ResetPending: row.LatestReset.Id > row.Reset.Id, PolicyPending: policyPending,
	}, nil
}

func formatClientPolicyUsage(upload, download, billed, remainder, uncertain int64) xray.ClientPolicyUsage {
	return xray.ClientPolicyUsage{
		Upload: strconv.FormatInt(upload, 10), Download: strconv.FormatInt(download, 10),
		Billed: formatClientPolicyBilled(billed, remainder), Uncertain: strconv.FormatInt(uncertain, 10),
	}
}

func formatClientPolicyBilled(billed, remainder int64) string {
	amount := strconv.FormatInt(billed, 10)
	if remainder != 0 {
		amount = strings.TrimRight(fmt.Sprintf("%s.%06d", amount, remainder), "0")
	}
	return amount
}
