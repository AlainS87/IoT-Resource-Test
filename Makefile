build:
	go mod tidy
	go build -o bin/exp ./cmd/exp

build_proto:
	@./scripts/build_proto.sh

clean_containers:
	@./scripts/clean_containers.sh

download_all_test_data:
	chmod +x scripts/download_test_data.sh
	./scripts/download_test_data.sh
run_IoT_test:
	chmod +x scripts/run_IoT_test.sh
	./scripts/run_IoT_test.sh

clean:
	rm -rf bin/*
