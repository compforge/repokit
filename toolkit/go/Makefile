# TEST_FILES selects whole Go packages, preserving helpers and package fixtures.
# An empty selection or non-Go inputs retains the full check.
GO_TEST_PACKAGES := ./...
ifneq ($(strip $(TEST_FILES)),)
ifeq ($(filter-out %.go,$(TEST_FILES)),)
GO_TEST_PACKAGES := $(sort $(addprefix ./,$(dir $(TEST_FILES))))
endif
endif

.PHONY: build test lint lint-ci format generate-languages
build:
	go build ./...
test:
	go test -json $(GO_TEST_PACKAGES)
lint:
	@test -z "$$(gofmt -l *.go internal forge state errs config git depot)" || (gofmt -l *.go internal forge state errs config git depot; exit 1)
	go vet ./...
lint-ci: lint
format:
	gofmt -w *.go internal forge state errs config git depot
generate-languages:
	go test ./internal/project -run TestLanguageCatalog -update-language-catalog
