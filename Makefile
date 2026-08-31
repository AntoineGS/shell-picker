.PHONY: build install windows-native fmt fmt-check test check real-fzf security-gate performance-stable performance-dedicated performance-first-frame cross-build release-snapshot release-check

SHELL_PICKER_FIRST_FRAME_POWERSHELL ?= pwsh
SHELL_PICKER_SETUP_SCRIPT ?= $(HOME)/gits/configurations/Both/ShellPicker/setup-shell-picker.sh
GIT_COMMIT := $(shell git rev-parse HEAD)
DEVELOPMENT_GOFLAGS := $(GOFLAGS) -ldflags=-X=main.version=$(GIT_COMMIT)

build:
	mkdir -p bin
	GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker ./cmd/shell-picker
	@if [ -f "$(SHELL_PICKER_SETUP_SCRIPT)" ]; then \
		grep -q '^latest_commit=' "$(SHELL_PICKER_SETUP_SCRIPT)" || { printf 'missing latest_commit in %s\n' "$(SHELL_PICKER_SETUP_SCRIPT)" >&2; exit 1; }; \
		sed -i 's/^latest_commit=.*/latest_commit="$(GIT_COMMIT)"/' "$(SHELL_PICKER_SETUP_SCRIPT)"; \
	fi

install:
	GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go install -trimpath ./cmd/shell-picker

windows-native:
	go run ./scripts/windowsnative

fmt:
	gofmt -w cmd internal integration scripts

test:
	go test ./... -count=1

check: test
	$(MAKE) fmt-check
	go vet ./...
	$(MAKE) performance-stable

fmt-check:
	test -z "$$(gofmt -l cmd internal integration scripts)"

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
	GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker ./cmd/shell-picker
	go test -c -o bin/shell-picker-perf.test ./integration
	./bin/shell-picker-perf.test -test.run TestDedicatedBaseline -binary ./bin/shell-picker -samples 50 -output host-baseline.json
	./bin/shell-picker-perf.test -test.run TestDedicatedTargets -binary ./bin/shell-picker -samples 50 -baseline host-baseline.json -output performance.json

performance-first-frame:
	test "$(SHELL_PICKER_DEDICATED_PERF)" = 1
	test -n "$(SHELL_PICKER_FIRST_FRAME_BINARY)" -a -n "$(SHELL_PICKER_FIRST_FRAME_TEST_BINARY)" -a -n "$(SHELL_PICKER_FIRST_FRAME_BUILD_METADATA)" -a -n "$(SHELL_PICKER_FIRST_FRAME_BASELINE)" -a -n "$(SHELL_PICKER_FIRST_FRAME_OUTPUT)" -a -n "$(SHELL_PICKER_FIRST_FRAME_RAW_DIR)" -a -n "$(SHELL_PICKER_FIRST_FRAME_SAMPLES)"
	$(SHELL_PICKER_FIRST_FRAME_POWERSHELL) -NoProfile -File scripts/verify-first-frame-build.ps1 -ProductionOutput "$(SHELL_PICKER_FIRST_FRAME_BINARY)" -HarnessOutput "$(SHELL_PICKER_FIRST_FRAME_TEST_BINARY)" -MetadataOutput "$(SHELL_PICKER_FIRST_FRAME_BUILD_METADATA)"
	"$(SHELL_PICKER_FIRST_FRAME_TEST_BINARY)" -test.run=^TestDedicatedBaseline$$ -binary "$(SHELL_PICKER_FIRST_FRAME_BINARY)" -samples "$(SHELL_PICKER_FIRST_FRAME_SAMPLES)" -output "$(SHELL_PICKER_FIRST_FRAME_BASELINE)"
	"$(SHELL_PICKER_FIRST_FRAME_TEST_BINARY)" -test.run=^TestDedicatedFirstFrameTargets$$ -binary "$(SHELL_PICKER_FIRST_FRAME_BINARY)" -baseline "$(SHELL_PICKER_FIRST_FRAME_BASELINE)" -first-frame-build-metadata "$(SHELL_PICKER_FIRST_FRAME_BUILD_METADATA)" -first-frame-output "$(SHELL_PICKER_FIRST_FRAME_OUTPUT)" -first-frame-raw-dir "$(SHELL_PICKER_FIRST_FRAME_RAW_DIR)" -first-frame-samples "$(SHELL_PICKER_FIRST_FRAME_SAMPLES)"

cross-build:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 go test -exec=true ./...
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker_linux_amd64 ./cmd/shell-picker
	GOOS=linux GOARCH=arm64 go test -exec=true ./...
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker_linux_arm64 ./cmd/shell-picker
	GOOS=windows GOARCH=amd64 go test -exec=true ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker_windows_amd64.exe ./cmd/shell-picker
	GOOS=windows GOARCH=arm64 go test -exec=true ./...
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 GOFLAGS="$(DEVELOPMENT_GOFLAGS)" go build -trimpath -o bin/shell-picker_windows_arm64.exe ./cmd/shell-picker

release-snapshot:
	test -n "$(VERSION)"
	go run ./scripts/release.go snapshot "$(VERSION)"

release-check:
	go run ./scripts/release.go check
