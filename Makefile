.PHONY: fmt fmt-check test check real-fzf security-gate performance-stable performance-dedicated cross-build release-snapshot release-check

fmt:
	gofmt -w cmd internal integration

test:
	go test ./... -count=1

check: test
	$(MAKE) fmt-check
	go vet ./...
	$(MAKE) performance-stable

fmt-check:
	test -z "$(gofmt -l cmd internal integration)"

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

cross-build:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 go test -exec=true ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/shell-picker_linux_amd64 ./cmd/shell-picker
	GOOS=linux GOARCH=arm64 go test -exec=true ./...
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/shell-picker_linux_arm64 ./cmd/shell-picker
	GOOS=windows GOARCH=amd64 go test -exec=true ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o bin/shell-picker_windows_amd64.exe ./cmd/shell-picker
	GOOS=windows GOARCH=arm64 go test -exec=true ./...
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o bin/shell-picker_windows_arm64.exe ./cmd/shell-picker

release-snapshot:
	test -n "$(VERSION)"
	go run ./scripts/release.go snapshot "$(VERSION)"

release-check:
	go run ./scripts/release.go check $(VERSION)
