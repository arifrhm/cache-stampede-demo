.PHONY: all build test up down clean demo record server loadtest-baseline loadtest-singleflight

all: build

build:
	@mkdir -p bin results
	go build -o bin/server ./cmd/server
	go build -o bin/loadtest ./cmd/loadtest
	go build -o bin/compare ./cmd/compare

test:
	go test -v -race ./...

up:
	docker compose up -d postgres redis
	./scripts/wait-for-services.sh

down:
	docker compose down

clean:
	rm -rf bin results/*.json results/*.txt results/*.cast results/*.mp4 results/*.gif

demo:
	./scripts/demo.sh

record:
	./scripts/record-demo.sh

server: build
	PORT=8880 DB_PORT=5433 REDIS_ADDR=localhost:6380 ./bin/server

loadtest-baseline: build
	./bin/loadtest -url=http://localhost:8880 -requests=10000 -concurrency=10000 -mode=baseline

loadtest-singleflight: build
	./bin/loadtest -url=http://localhost:8880 -requests=10000 -concurrency=10000 -mode=singleflight
