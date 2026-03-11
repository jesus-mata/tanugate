.PHONY: build test lint vet fmt run docker-build docker-push clean

BINARY=gateway

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(BINARY) ./cmd/gateway

test:
	go test -v -race -count=1 ./...

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

run: build
	./bin/$(BINARY) -config config/gateway.yaml

docker-build:
	docker build -t api-gateway .

docker-push: docker-build
	docker push api-gateway

clean:
	rm -rf bin/
