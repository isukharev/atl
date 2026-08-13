package main

const (
	extensionProtocolRuntimeStepContract = `      - name: Extension protocol runtime
        run: |
          env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go -C internal/agenteval test ./extension \
            -run '^(TestExtensionManifestV1IsClosed|TestExtensionProtocolV1StateMachineIsClosed)$' -count=1
          env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go -C internal/agenteval test . \
            -run '^(TestExtensionHostAdmissionMaterializesNativeExecutableWithClosedEnvironment|TestExtensionHostAdmissionRejectsUnsafeExecutable|TestExtensionProcessHostBoundsAndCleanup|TestDarwinZombieOnlyProcessGroupSignal|TestVerifyExtensionProtocolReportIsContentMinimized|TestPrivateExtensionRuntimeRootUsesTrustedSystemTemporaryDirectory|TestPrivateExtensionRuntimePathsAreOwnerOnly|TestPrivateExtensionRuntimeRejectsSymlinks|TestExtensionPlatformEnvironmentIsEmptyOnUnix)$' -count=1`
	schedulerRuntimeStepContract = `      - name: Scheduler runtime
        run: |
          env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go -C internal/agenteval test ./scheduler \
            -run '^(TestSchedulerCodecIsClosedCanonicalBoundedAndContentAddressed|TestSchedulerDispatchIsRoundOrderedBoundedAndCompletionOrderIndependent|TestSchedulerCohortAndResourceLimitsBoundEachBatch|TestSchedulerCostExhaustionStopsBeforeTheNextReservation|TestSchedulerFailFastCancelsTheCurrentBatchAndNeverStartsLaterRounds|TestSchedulerCanceledContextStartsNothing|TestSchedulerResumeCountsTerminalTasksAndDispatchesOnlyPlannedComplement|TestSchedulerPlanRejectsRosterOrderCohortAndResourceDrift)$' -count=1
          env -u GOROOT GOTOOLCHAIN=auto GOWORK=off go -C internal/agenteval test . \
            -run '^(TestScheduledReferenceParallelBlocksMatchTheSequentialOracleExactly|TestScheduledReferenceResumePreservesTerminalAttemptAndRunsOnlyPlannedComplement|TestScheduledReferenceResumeAbsorbsCommittedCrashTailAsUnknownWithoutReplay|TestScheduledReferenceResumeRejectsArtifactsForNeverStartedAttempt|TestScheduledReferenceResumeRejectsStartedAttemptAfterPlannedScheduleMember)$' -count=1`
	extensionProtocolWindowsRuntimeStepContract = `      - name: Extension protocol runtime
        shell: pwsh
        run: |
          Remove-Item Env:GOROOT -ErrorAction SilentlyContinue
          $env:GOTOOLCHAIN = "auto"
          $env:GOWORK = "off"
          go -C internal/agenteval test ./extension ` + "`" + `
            -run '^(TestExtensionManifestV1IsClosed|TestExtensionProtocolV1StateMachineIsClosed)$' -count=1
          if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
          go -C internal/agenteval test . ` + "`" + `
            -run '^(TestAttemptLedgerWindowsFailsClosedBeforeExtensionProcessEntry|TestExtensionHostAdmissionMaterializesNativeExecutableWithClosedEnvironment|TestExtensionHostAdmissionRejectsUnsafeExecutable|TestExtensionProcessHostBoundsAndCleanup|TestVerifyExtensionProtocolReportIsContentMinimized|TestPrivateExtensionWindowsRuntimeACLsAreProtected|TestPrivateExtensionWindowsRootGuardBlocksDeleteUntilClose|TestPrivateExtensionWindowsRuntimeRootAcceptsTrailingBaseSeparator|TestPrivateExtensionWindowsExecutableGuardBlocksReplacementAndLaunchesAdmittedBytes|TestPrivateExtensionWindowsRejectsPermissiveOrInheritedACL|TestPrivateExtensionWindowsRejectsReparseDirectory|TestExtensionPlatformEnvironmentIgnoresAmbientWindowsDirectory)$' -count=1
          if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }`
	schedulerWindowsRuntimeStepContract = `      - name: Scheduler runtime
        shell: pwsh
        run: |
          Remove-Item Env:GOROOT -ErrorAction SilentlyContinue
          $env:GOTOOLCHAIN = "auto"
          $env:GOWORK = "off"
          go -C internal/agenteval test ./scheduler ` + "`" + `
            -run '^(TestSchedulerCodecIsClosedCanonicalBoundedAndContentAddressed|TestSchedulerDispatchIsRoundOrderedBoundedAndCompletionOrderIndependent|TestSchedulerCohortAndResourceLimitsBoundEachBatch|TestSchedulerCostExhaustionStopsBeforeTheNextReservation|TestSchedulerFailFastCancelsTheCurrentBatchAndNeverStartsLaterRounds|TestSchedulerCanceledContextStartsNothing|TestSchedulerResumeCountsTerminalTasksAndDispatchesOnlyPlannedComplement|TestSchedulerPlanRejectsRosterOrderCohortAndResourceDrift)$' -count=1
          if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }`
)
