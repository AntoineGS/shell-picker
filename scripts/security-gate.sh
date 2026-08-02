#!/bin/sh
set -eu

TASK20_PACKAGES='./internal/session ./internal/sessionipc ./internal/candidate ./internal/process ./internal/callback ./internal/preview ./internal/fzf ./internal/app ./internal/pathutil ./integration'
TASK20_PATTERN='^Test(Actor|Handle|Reduce|NormalEscape|Validate|Server|Client|FinishedTelemetry|CachedPolicy|Fresh|IndependentFresh|CPNever|CallerCancellation|BuilderZoxide|Zoxide|EnumerateReadDir|Kqueue|RejectsNonIdentifiable|WaitDelay|WaitIs|CancellationCloses|ExitErrorPrecedesBlocking|OrdinaryCompletion|Foreground|RestoreForeground|RetainedInherited|CancelKills|InheritedTree|Preview|Archive|Zip|Cache|Converter|Renderer|ExternalRenderer|Terminal|SessionSpec|Run|ParseOutput|ActionArgument|TokenCanary|PickerBackend|CreateDirectory|SecurityGate|Forged|CancelledNavigation|ResourceSnapshot|ParityPreview|IntegrationRealFZFNoLeaks|IntegrationAdaptiveRealFZF|RealFZF|Windows|CreateProcess|RunnerBeforeStart|RunnerNilBeforeStart)'

for argument in "$@"; do
	case "$argument" in
	-race)
		;;
	-count=* | -p=*)
		value=${argument#*=}
		case "$value" in
		'' | *[!0-9]* | 0) exit 2 ;;
		esac
		;;
	-timeout=*)
		remaining=${argument#*=}
		[ -n "$remaining" ] || exit 2
		while [ -n "$remaining" ]; do
			digits=0
			while [ -n "$remaining" ]; do
				first=${remaining%"${remaining#?}"}
				case "$first" in
				[0-9]) digits=1; remaining=${remaining#?} ;;
				*) break ;;
				esac
			done
			[ "$digits" -eq 1 ] || exit 2
			case "$remaining" in
			.*)
				remaining=${remaining#?}
				fraction_digits=0
				while [ -n "$remaining" ]; do
					first=${remaining%"${remaining#?}"}
					case "$first" in
					[0-9]) fraction_digits=1; remaining=${remaining#?} ;;
					*) break ;;
					esac
				done
				[ "$fraction_digits" -eq 1 ] || exit 2
				;;
			esac
			case "$remaining" in
			ns*) remaining=${remaining#ns} ;;
			us*) remaining=${remaining#us} ;;
			µs*) remaining=${remaining#µs} ;;
			μs*) remaining=${remaining#μs} ;;
			ms*) remaining=${remaining#ms} ;;
			s*) remaining=${remaining#s} ;;
			m*) remaining=${remaining#m} ;;
			h*) remaining=${remaining#h} ;;
			*) exit 2 ;;
			esac
		done
		;;
	*)
		exit 2
		;;
	esac
done

# Package splitting is intentional: this is the checked manifest package list.
# shellcheck disable=SC2086
GOENV=off GOFLAGS= go test "$@" $TASK20_PACKAGES -run "$TASK20_PATTERN"
