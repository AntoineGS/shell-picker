.PHONY: fmt test check real-fzf security-gate performance-stable performance-dedicated

fmt:
	gofmt -w cmd internal integration

test:
	go test ./... -count=1

check: test

security-gate:
	./scripts/security-gate.sh -race -count=10 -p=1 -timeout=10m

real-fzf:
	test -n "$(SHELL_PICKER_REAL_FZF)"
	go test ./integration -run TestRealFZF -count=1 -v

performance-stable:
	go test ./integration -run '^(TestStablePerformanceGates|TestStablePreviewReplacementBudgets)$$' -count=1
	go test ./internal/app ./internal/fzf ./internal/callback ./internal/preview ./internal/sessionipc ./internal/candidate \
		-run '^(TestRunPickerOwnsOneSessionAndOneFZF|TestRunPickerShipsWorkingPreviewCallback|TestRunDoesNotProbeVersion|TestOptionsHaveNoListenOrDuplicateBindings|TestPreviewAggregatesSequentialChildTelemetry|TestPreviewTelemetryDistinguishesNativeFallbackAfterChild|TestCorePreviewEveryCategoryHasBoundedNativeFallback|TestServerRejectsSeventeenthHandlerAndCloseCancelsAndJoins|TestLocalWorkerCountBounded)$$' -count=1

performance-dedicated:
	test "$${SHELL_PICKER_DEDICATED_PERF}" = 1
	mkdir -p bin
	go build -trimpath -o bin/shell-picker ./cmd/shell-picker
	go test -c -o bin/shell-picker-perf.test ./integration
	./bin/shell-picker-perf.test -test.run TestDedicatedBaseline -binary ./bin/shell-picker -samples 50 -output host-baseline.json
	./bin/shell-picker-perf.test -test.run TestDedicatedTargets -binary ./bin/shell-picker -samples 50 -baseline host-baseline.json -output performance.json
