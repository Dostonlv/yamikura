package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/dostonlv/yamikura/pkg/yamikura"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:6379", "Address to listen on")
	tlsEnable := flag.Bool("tls", false, "Enable TLS")
	certFile := flag.String("cert", "server.crt", "TLS certificate file")
	keyFile := flag.String("key", "server.key", "TLS key file")

	flag.Parse()

	fmt.Println("Starting Yamikura cache server...")
	fmt.Println("Version: 1.0.0")

	cache := yamikura.New()

	var err error

	if *tlsEnable {
		fmt.Printf("Starting TLS server on %s\n", *addr)
		_, err = yamikura.StartTLSServer(*addr, *certFile, *keyFile, cache)
		if err != nil {
			log.Fatalf("Failed to start TLS server: %v", err)
		}
	} else {
		fmt.Printf("Starting server on %s\n", *addr)
		_, err = yamikura.StartServer(*addr, cache)
		if err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}

	fmt.Println("Server is running. Press Ctrl+C to stop.")

	select {}
}
