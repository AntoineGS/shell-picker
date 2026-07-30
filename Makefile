.PHONY: fmt test check real-fzf security-gate

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
