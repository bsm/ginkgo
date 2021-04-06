default: test

test:
	go test ./...

tidy:
	go mod tidy

version:
	@cat config/config.go | grep 'VERSION' | cut -d' ' -f4 | jq -r .

update:
	@./update.sh
