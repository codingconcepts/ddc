test:
	go test ./... -v -cover

build:
	- go build -ldflags="-s -w" .
	- mv ./ddc ~/dev/bin
	- rm ~/go/bin/ddc