GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOINSTALL=$(GOCMD) get
GOMOD=$(GOCMD) mod

BUILD_DIR=build
BIN_NAME=azure_storage_queue_exporter

all: deps build

.PHONY: deps
deps:
	$(GOGET) .
	$(GOMOD) tidy

.PHONY: deps-update
deps-update:
	$(GOGET) -u
	$(GOMOD) tidy

.PHONY: test
test:
	$(GOTEST) -v ./...

.PHONY: build
build:
	$(GOBUILD) -o $(BUILD_DIR)/$(BIN_NAME)

.PHONY: install
install:
	$(GOINSTALL)

.PHONY: clean
clean:
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
