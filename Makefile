TOOLS = runtracer spawnctnr runexper
GO_SRC := $(shell find . -type f -name '*.go')

.PHONY: all
all: $(TOOLS)

$(TOOLS): %: $(GO_SRC)
	go build -o $@ ./tools/$@
