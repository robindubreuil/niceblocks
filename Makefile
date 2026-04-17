.PHONY: build run test clean

BINARY_NAME=niceblocks

build:
	go build -o $(BINARY_NAME) ./cmd/niceblocks

run: build
	sudo ./$(BINARY_NAME)

test:
	go test -race -count=1 ./...

clean:
	rm -f $(BINARY_NAME)
	go clean
