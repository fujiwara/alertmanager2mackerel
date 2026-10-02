.PHONY: clean test test-e2e

alertmanager2mackerel: go.* *.go
	go build -o $@ ./cmd/alertmanager2mackerel

clean:
	rm -rf alertmanager2mackerel dist/

test:
	go test -v ./...

test-e2e:
	go test -tags e2e -v -count=1 ./e2e/

install:
	go install github.com/fujiwara/alertmanager2mackerel/cmd/alertmanager2mackerel

dist:
	goreleaser build --snapshot --clean
