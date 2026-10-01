#!/usr/bin/env python3
"""Require named actual native Snell panel PASS results; SKIP is not acceptance."""
import re
import sys
from pathlib import Path

mode, log_path = sys.argv[1:]
if mode not in ("sqlite", "postgres"):
    raise SystemExit("expected sqlite or postgres")
required = {
    'TestSnellSQLiteNativeJSONPreservesNULPSK',
    'TestSnellNativeExportDoesNotReplaceExcludedHostsWithListenerAddress',
    'TestCapabilitiesAdvertiseVerifiedNativeSnell',
    'TestSnellCredentialsCanonicalRoundTripAndRedactedMerge',
    'TestSnellCrossDatabaseExportPreservesCredentialsAndLedger',
    'TestSnellHTTPExportRealCoreTCPUDPQUICAndSharedLifecycle',
    'TestSnellHandlerNegotiatesNativeCapabilityBeforeMutation',
    'TestSnellHotCredentialRotationPreservesListenerAndSiblings',
    'TestSnellListenerAddAndRemovalRequireNativeCapability',
    'TestSnellManagedAccountRequiresTypedNativePSK',
    'TestSnellNativeExportPreservesIDNIPv6AndRepeatedHostLabels',
    'TestSnellNativeExportRejectsUnsupportedManagedHostOptions',
    'TestSnellNativeJSONSubscriptionBuildsActualCoreConfiguration',
    'TestSnellNativeSubscriptionsExcludeInactiveOwnerOrResource',
    'TestSnellNativeSubscriptionsRejectUnsupportedFormats',
    'TestSnellNativeSurgeRejectsControlPSKButJSONPreservesIt',
    'TestSnellNativeSurgeSubscriptionPreservesCanonicalUTF8PSK',
    'TestSnellPanelBulkAttachReadsCurrentPSKInsideWriter',
    'TestSnellPanelBulkCreateRejectsMultipleOwnersBeforeWrites',
    'TestSnellPanelBulkDetachIgnoresStaleOwnerMirror',
    'TestSnellPanelCanonicalIndependentPSK',
    'TestSnellPanelClientMutationRejectsRuntimeIdentityAndRemoteScope',
    'TestSnellPanelExclusiveOwnerAndLastDetach',
    'TestSnellPanelExpiryQuotaAndFinalDeleteRealCore',
    'TestSnellPanelFilteredOrdinaryUpdateRefusesReusedCanonicalLabel',
    'TestSnellPanelGeneratedPSKOwnerCommandAndDisabledReservation',
    'TestSnellPanelInvalidSocketOptionsRejectBeforeWrites',
    'TestSnellPanelManagedConfigUsesSQLIdentityAndIndependentPSK',
    'TestSnellPanelOptionsRetainDisabledOwnerFromSQL',
    'TestSnellPanelPostgresConcurrentMembershipUsesResourceLock',
    'TestSnellPanelPublicCRUDRealCorePreservesSiblingAndSharedLedger',
    'TestSnellPanelRejectsUnsupportedOptionsBeforeWrites',
    'TestSnellPanelSharedVersionSixChecksAllCredentialUpdatePaths',
    'TestSnellPanelStandalonePortableRoundTripPreservesIndependentPSK',
    'TestSnellPanelStandaloneRejectsInvalidPSKBeforeWrites',
    'TestSnellPanelVersionFiveReservesUDP',
    'TestSnellPostgresOldSchemaUpgradePreservesIdentityAndReopen',
    'TestSnellSQLiteOldSchemaUpgradePreservesIdentityAndReopen',
    'TestSnellStartupNegotiatesBeforePreparation',
}
postgres_only = {
    "TestSnellCrossDatabaseExportPreservesCredentialsAndLedger",
    "TestSnellPostgresOldSchemaUpgradePreservesIdentityAndReopen",
    "TestSnellPanelPostgresConcurrentMembershipUsesResourceLock",
}
if mode == "sqlite":
    required -= postgres_only
required.add("TestMigrationModelsMatchPanelModels")
log = Path(log_path).read_text()
if re.search(r"^\s*--- FAIL:|^FAIL(?:\s|$)", log, re.M):
    raise SystemExit("native Snell log contains a failed test")
passed = set(re.findall(r"^\s*--- PASS: (Test[^ ]+) ", log, re.M))
required.update(
    "TestSnellHTTPExportRealCoreTCPUDPQUICAndSharedLifecycle/" + transport
    for transport in ("v4-quic-false", "v5-quic-false", "v5-quic-true", "v6-quic-false")
)
required.update(
    f"TestSnellStartupNegotiatesBeforePreparation/{mode}/v{version}"
    for mode in ("preceding-native-core", "current-core")
    for version in (4, 5, 6)
)
missing = required - passed
if missing:
    raise SystemExit("missing native Snell PASS: " + ", ".join(sorted(missing)))
skipped = set(re.findall(r"^\s*--- SKIP: (TestSnell[^ ]*|TestCapabilitiesAdvertiseVerifiedNativeSnell) ", log, re.M))
if mode == "sqlite":
    skipped = {name for name in skipped if name.split("/")[0] not in postgres_only}
if skipped:
    raise SystemExit("native Snell acceptance skipped: " + ", ".join(sorted(skipped)))
print(f"native Snell panel {mode}: {len(required)} required tests passed")
