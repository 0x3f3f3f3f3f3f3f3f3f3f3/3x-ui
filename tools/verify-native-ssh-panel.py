#!/usr/bin/env python3
"""Require named actual native SSH panel PASS results; SKIP is not acceptance."""
import re
import sys
from pathlib import Path

mode, log_path = sys.argv[1:]
if mode not in ("sqlite", "postgres"):
    raise SystemExit("expected sqlite or postgres")
required = {
    'TestCapabilitiesAdvertiseVerifiedNativeSSH',
    'TestSSHCredentialRecordMergePreservesOmittedAndRedactsConflicts',
    'TestSSHCrossDatabaseRecoveryPreservesBusinessTrustAndCanonicalIdentity',
    'TestSSHEmptyListenerAndRemovalRequireNativeCapability',
    'TestSSHEmptyListenerRemovalStillRequiresNativeCapability',
    'TestSSHHTTPAuthorizedReverseAndStrictNativeOutbound',
    'TestSSHHTTPExportRealOpenSSHAndSharedLifecycle',
    'TestSSHHandlerNegotiatesNativeCapabilityBeforeMutation',
    'TestSSHHostKeyPrivateMaterialIsExcludedFromOrdinaryJSON',
    'TestSSHHotCredentialRotationPreservesListenerAndSiblings',
    'TestSSHManagedAccountRequiresTypedNativeAuthentication',
    'TestSSHOpenSSHExportNativeBoundaries',
    'TestSSHOpenSSHExportsRefuseUnsupportedFormatsAndInactiveBindings',
    'TestSSHOpenSSHSubscriptionUsesPinnedPublicTrustAndSafeNativeConfig',
    'TestSSHOutboundBusinessPrivateKeyConfinement',
    'TestSSHOutboundRejectsUnpinnedAndUnsupportedOptions',
    'TestSSHPanelAttachReadsCanonicalCredentialsInsideSQLWriter',
    'TestSSHPanelCandidateUsesCanonicalIdentityWithoutMaterializingPrivateKey',
    'TestSSHPanelClearCommandIsConsumedBeforeListenerReopen',
    'TestSSHPanelCloneGetsNewTrustAndMissingSQLKeyCannotRotate',
    'TestSSHPanelCredentialChangeChecksDisabledLinkedOwnersAndLastMethod',
    'TestSSHPanelEmptyListenerOwnsPersistentBusinessHostKey',
    'TestSSHPanelExplicitClearIsSeparateFromOmission',
    'TestSSHPanelInboundOptionsDescribeAllowedAuthentication',
    'TestSSHPanelInboundOptionsExposeOnlyPublicBusinessTrust',
    'TestSSHPanelInboundUpdateResponseDoesNotReplayClearCommand',
    'TestSSHPanelKeyOnlyPublicInboundUsesCanonicalAuthentication',
    'TestSSHPanelLocalAccountRejectsRemoteBindingBeforeWrites',
    'TestSSHPanelManagedStartupMaterializesAndRecoversSameHostTrust',
    'TestSSHPanelManagedStartupRejectsUnsafeBusinessKeyPaths',
    'TestSSHPanelOmittedUpdateReadsCredentialsInsideSQLWriter',
    'TestSSHPanelPrecedingCoreRefusesBeforePrivateKeyPreparation',
    'TestSSHPanelReviewCredentialsUpdatePasswordProxyOwner',
    'TestSSHPanelReviewAddClientRejectsForgedRuntimeOwnerBeforeWrites',
    'TestSSHPanelReviewClientUpdateRefusesReusedCanonicalLabel',
    'TestSSHPanelReviewPasswordOwnerFilterValidatesAllNativeListeners',
    'TestSSHPanelReviewPasswordOwnerOmissionsReadCurrentNativeCredentials',
    'TestSSHPanelReviewFilteredRenameConsumesClearInOrdinaryMirrors',
    'TestSSHOpenSSHExportRetainsAllAllowedUnicodeUsernames',
    'TestSSHPanelPublicCRUDRealCorePreservesSiblingAndSharedLedger',
    'TestSSHPanelRejectsInvalidNativeAuthenticationBeforeWrites',
    'TestSSHPanelRejectsUnsupportedServerOptionsBeforeWrites',
    'TestSSHPanelRemoteBindingRejectsLocalOwnerBeforeWrites',
    'TestSSHPanelRestoredDatabaseRecreatesOriginalHostTrust',
    'TestSSHPanelStandaloneNativeCredentialsPreserveIdentityOnRenameAndRotation',
    'TestSSHPostgresOldSchemaUpgradePreservesExistingIdentity',
    'TestSSHSQLiteBackupRestorePreservesBusinessTrustAndCanonicalIdentity',
    'TestSSHSQLiteOldSchemaUpgradePreservesExistingIdentity',
}
postgres_only = {
    "TestSSHCrossDatabaseRecoveryPreservesBusinessTrustAndCanonicalIdentity",
    "TestSSHPostgresOldSchemaUpgradePreservesExistingIdentity",
}
if mode == "sqlite":
    required -= postgres_only
required.add("TestMigrationModelsMatchPanelModels")
log = Path(log_path).read_text()
passed = set(re.findall(r"^--- PASS: (Test\w+) ", log, re.M))
missing = required - passed
if missing:
    raise SystemExit("missing native SSH PASS: " + ", ".join(sorted(missing)))
skipped = set(re.findall(r"^\s*--- SKIP: (TestSSH[^ ]*|TestCapabilitiesAdvertiseVerifiedNativeSSH) ", log, re.M))
if mode == "sqlite":
    skipped = {name for name in skipped if name.split("/")[0] not in postgres_only}
if skipped:
    raise SystemExit("native SSH acceptance skipped: " + ", ".join(sorted(skipped)))
print(f"native SSH panel {mode}: {len(required)} required tests passed")
