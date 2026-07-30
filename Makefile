.PHONY: fmt test check real-fzf security-gate

fmt:
	gofmt -w cmd internal integration

test:
	go test ./... -count=1

check: test

security-gate:
	./scripts/security-gate.sh $(GO_TEST_ARGS)

real-fzf:
	test -n "$(SHELL_PICKER_REAL_FZF)"
	go test ./integration -run TestRealFZF -count=1 -v
