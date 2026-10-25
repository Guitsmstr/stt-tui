.PHONY: build run test probe config clean

build:
	go build -o stt-tui .

run: build
	./stt-tui

test:
	go test ./...

probe: build
	./stt-tui --probe

config: build
	./stt-tui --print-config

clean:
	rm -f stt-tui
