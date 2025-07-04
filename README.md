# Yamikura

Yamikura is a high-performance Redis-compatible in-memory cache system written in Go.

## Features

- High-performance in-memory cache with sharding (1024 shards)
- Redis protocol (RESP) compatibility
- Support for commands: GET, SET, DEL, TTL, KEYS, PING
- Key expiration (TTL) functionality
- Pattern matching for KEYS command using prefix tree
- TCP server with TLS support
- Colorful interactive CLI client

## Installation

```bash
go get github.com/yourusername/yamikura
```

## Running the Server

```bash
# Basic usage
go run cmd/yamikura/main.go

# With custom options
go run cmd/yamikura/main.go -addr 127.0.0.1:6380 -tls -cert server.crt -key server.key
```

## Using the CLI

```bash
go run cmd/yamikura-cli/main.go -host 127.0.0.1 -port 6379
```

## Performance

Benchmarks show Yamikura performs 1.8-2.6x faster than Redis for basic operations.

## License

MIT 