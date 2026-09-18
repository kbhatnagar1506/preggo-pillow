GO ?= $(HOME)/.local/go/bin/go
BIN := bin/lull

.PHONY: run build pi clean test fmt

run: build            ## build and run with simulated sensors
	./$(BIN)

build:                ## build for this machine
	$(GO) build -o $(BIN) ./cmd/lull

pi:                   ## cross-compile for Raspberry Pi (64-bit). Pure Go, no cgo.
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -o bin/lull-pi ./cmd/lull
	@ls -lh bin/lull-pi

pi32:                 ## cross-compile for 32-bit Pi OS
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 $(GO) build -o bin/lull-pi32 ./cmd/lull

deploy: pi            ## scp the binary to the Pi. usage: make deploy PI=user@host
	@test -n "$(PI)" || (echo "usage: make deploy PI=pi@raspberrypi.local" && exit 1)
	scp bin/lull-pi $(PI):~/lull
	@echo "now on the Pi:  ./lull -source=serial"

fmt:
	$(GO) fmt ./...

test:
	$(GO) test ./...

clean:
	rm -rf bin data/lull.db
