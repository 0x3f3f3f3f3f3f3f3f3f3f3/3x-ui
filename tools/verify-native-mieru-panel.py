#!/usr/bin/env python3
"""Require actual PASS lines for native mieru panel acceptance, never SKIP."""
import re
import sys
from pathlib import Path

mode, log_path = sys.argv[1:]
if mode not in ("sqlite", "postgres"):
    raise SystemExit("expected sqlite or postgres")
required = {
    'TestMieruCredentialsRoundTripSeparatelyFromOtherProtocols',
    'TestMieruCredentialRecordMergePreservesOmittedAndRedactsConflicts',
    'TestMieruSQLiteOldSchemaUpgradePreservesIdentityAndReopen',
    'TestMieruPublicInboundCreateAndOmittedUpdatePreserveGeneratedCredentials',
    'TestMieruPublicClientCreateAttachAndUpdateUseNativeCredentials',
    'TestMieruPublicInboundRejectsUnsupportedOptionsBeforeWrites',
    'TestMieruCredentialChangeChecksAllLinkedListenersBeforeWrites',
    'TestMieruConfigRejectsConcurrentCredentialRotation',
    'TestMieruEmptyLocalListenerRequestsManagedActivation',
    'TestMieruLocalAttachmentRejectsUncoordinatedRemoteIdentity',
    'TestMieruPortOwnershipFollowsNativePhysicalTransport',
    'TestMieruStandaloneCRUDAndPortableExportPreserveCredentials',
    'TestMieruDefaultsAreIndependentAndGeneratedOnce',
    'TestMieruCredentialByteLimitsRejectBeforeWrites',
    'TestMieruListenerDuplicateUsernameIncludesDisabledSibling',
    'TestMieruManagedConfigUsesStoredIdentityAndIndependentAuthentication',
    'TestMieruPublicCRUDRealCorePreservesSiblingAndSharedLedger',
    'TestMieruManagedAccountRequiresTypedValidCredentials',
    'TestMieruHandlerNegotiatesNativeCapabilityBeforeMutation',
    'TestMieruEmptyListenerAndRemovalRequireNativeCapability',
    'TestMieruHotCredentialRotationPreservesListenerAndSiblings',
    'TestMieruStartupNegotiatesBeforePreparation',
    'TestCapabilitiesAdvertiseVerifiedNativeMieru',
    'TestMieruShareLinkOfficialParserPreservesNativeProfile',
    'TestMieruOfficialSubscriptionConfigAndUnsupportedFormats',
    'TestMieruShareLinkHostEndpointsPreserveNativeUDP',
    'TestMieruHTTPExportRealCoreLifecycle',
    'TestMieruOfficialLinksAndClientConfigReimport',
    'TestMieruImportRejectsSilentProfileLoss',
    'TestMieruExternalShareFormatsAreExplicit',
}
postgres_only = {"TestMieruPostgresOldSchemaUpgradePreservesIdentityAndReopen", "TestMieruCrossDatabaseExportPreservesCredentialsAndLedger"}
if mode == "postgres":
    required |= postgres_only
log = Path(log_path).read_text()
passed = set(re.findall(r"^--- PASS: (Test\w+) ", log, re.M))
missing = required - passed
if missing:
    raise SystemExit("missing native mieru PASS: " + ", ".join(sorted(missing)))
skipped = set(re.findall(r"^\s*--- SKIP: (TestMieru[^ ]*) ", log, re.M))
if mode == "sqlite":
    skipped = {name for name in skipped if name.split("/")[0] not in postgres_only}
if skipped:
    raise SystemExit("native mieru acceptance skipped: " + ", ".join(sorted(skipped)))
print(f"native mieru panel {mode}: {len(required)} required tests passed")
