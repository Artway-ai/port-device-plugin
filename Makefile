all: build

fmt:
	go fmt ./...

vet:
	go vet ./...

build: fmt vet
	go build -o port-plugin main.go

clean:
	rm -rf port-plugin
