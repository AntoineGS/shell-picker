.PHONY: fmt test check

fmt:
	gofmt -w cmd internal

test:
	go test ./... -count=1

check: test
