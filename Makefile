.PHONY: clean test

alertmanager2mackerel: go.* *.go
	go build -o $@ ./cmd/alertmanager2mackerel

clean:
	rm -rf alertmanager2mackerel dist/

test:
	go test -v ./...

install:
	go install github.com/fujiwara/alertmanager2mackerel/cmd/alertmanager2mackerel

dist:
	goreleaser build --snapshot --clean
