.PHONY: build test run clean

build:
	go build -o photodupe .

test:
	go test ./...

run: build
	./photodupe ui

clean:
	rm -f photodupe
	rm -rf data/heic_cache data/thumbs
