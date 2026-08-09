.PHONY: fmt test vet build cross firmware clean

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

vet:
	go vet ./...

build:
	go build -o bin/quota-monitor ./cmd/quota-monitor

cross:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1

firmware:
	pio run -d firmware

clean:
	go clean -testcache
