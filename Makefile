GO_TOOLCHAIN ?= go1.25.12
GO := env GOTOOLCHAIN=$(GO_TOOLCHAIN) go

.PHONY: build test race vet check clean

build:
	BOM_BUILDER_GO_TOOLCHAIN=$(GO_TOOLCHAIN) ./scripts/build.sh

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: test race vet build

clean:
	rm -rf bin
