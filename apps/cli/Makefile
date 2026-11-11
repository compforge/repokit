# Selected Go files expand to packages; selected Node tests run as files.
# Empty or unrecognized selections retain both complete suites.
GO_TEST_PACKAGES := ./...
NODE_TEST_FILES := internal/viewer/*_test.cjs
ifneq ($(strip $(TEST_FILES)),)
ifeq ($(filter-out %.go %_test.cjs,$(TEST_FILES)),)
GO_TEST_PACKAGES := $(sort $(addprefix ./,$(dir $(filter %.go,$(TEST_FILES)))))
NODE_TEST_FILES := $(filter %_test.cjs,$(TEST_FILES))
endif
endif

.PHONY: build install test lint lint-ci format
VERSION := $(shell cat ../../VERSION)
LDFLAGS := -X github.com/compforge/repokit/apps/cli/internal/cli.Version=$(VERSION)
build:
	go build -ldflags "$(LDFLAGS)" -o ../../bin/repocli ./cmd/repocli
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/repocli
test:
	@if [ -n "$(NODE_TEST_FILES)" ]; then node --test $(NODE_TEST_FILES); fi
	@if [ -n "$(GO_TEST_PACKAGES)" ]; then go test -json $(GO_TEST_PACKAGES); fi
lint:
	node --check internal/viewer/static/app.js
	node --check internal/viewer/static/reading-map.js
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)
	go vet ./...
lint-ci: lint
format:
	gofmt -w cmd internal
