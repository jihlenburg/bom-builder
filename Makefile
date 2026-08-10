GO_TOOLCHAIN ?= go1.25.12
GO := env GOTOOLCHAIN=$(GO_TOOLCHAIN) go
GO_WINDOWS := env GOTOOLCHAIN=$(GO_TOOLCHAIN) GOOS=windows GOARCH=amd64 go

.PHONY: build test race vet windows check clean

build:
	BOM_BUILDER_GO_TOOLCHAIN=$(GO_TOOLCHAIN) ./scripts/build.sh

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

# Windows is a supported target: cross-compile and vet every package for
# windows/amd64 so platform-specific breakage fails the check pipeline
# instead of surfacing on a user's machine.
windows:
	$(GO_WINDOWS) build ./...
	$(GO_WINDOWS) vet ./...

check: test race vet windows build

clean:
	rm -rf bin
