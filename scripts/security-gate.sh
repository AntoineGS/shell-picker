#!/bin/sh
set -eu

TASK20_PACKAGES='./internal/session ./internal/sessionipc ./internal/candidate ./internal/process ./internal/callback ./internal/preview ./internal/fzf ./internal/app ./internal/pathutil ./integration'
TASK20_PATTERN='^Test(Actor|Handle|Reduce|NormalEscape|Validate|Server|Client|FinishedTelemetry|CachedPolicy|Fresh|IndependentFresh|CPNever|CallerCancellation|BuilderZoxide|Zoxide|EnumerateReadDir|Kqueue|RejectsNonIdentifiable|WaitDelay|WaitIs|CancellationCloses|ExitErrorPrecedesBlocking|OrdinaryCompletion|Foreground|RestoreForeground|RetainedInherited|CancelKills|InheritedTree|Preview|Archive|Zip|Cache|Converter|Renderer|ExternalRenderer|Terminal|SessionSpec|Run|ParseOutput|ActionArgument|TokenCanary|PickerBackend|CreateDirectory|SecurityGate|Forged|CancelledNavigation|ResourceSnapshot|ParityPreview|RealFZF|Windows|CreateProcess|RunnerBeforeStart|RunnerNilBeforeStart)'

# Package splitting is intentional: this is the checked manifest package list.
# shellcheck disable=SC2086
go test "$@" $TASK20_PACKAGES -run "$TASK20_PATTERN"
