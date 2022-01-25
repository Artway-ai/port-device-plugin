all: build

fmt:
	go fmt ./...

vet:
	go vet ./...

build: fmt vet
	go build -o plugin main.go
