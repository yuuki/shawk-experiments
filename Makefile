TOOL = runexper
GO_SRC := $(shell find . -type f -name '*.go')

.PHONY: all
all: build

.PHONY: build
build: $(TOOL)

$(TOOL): $(GO_SRC)
	go build -o $(TOOL) ./tools/$@
