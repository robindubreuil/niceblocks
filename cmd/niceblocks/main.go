package main

import (
	"flag"
	"fmt"
	"log"
	"niceblocks/internal/web"
	"os"
	"strconv"
)

func main() {
	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = "8080"
	}

	port := flag.String("port", defaultPort, "Port to listen on")
	password := flag.String("password", os.Getenv("PASSWORD"), "Optional UI password (can also be set via PASSWORD env var)")
	flag.Parse()

	portNum, err := strconv.Atoi(*port)
	if err != nil || portNum < 1 || portNum > 65535 {
		log.Fatalf("Invalid port: %q (must be a number between 1 and 65535)", *port)
	}

	server := web.NewServer()
	server.Password = *password

	addr := fmt.Sprintf("0.0.0.0:%s", *port)

	if err := server.Start(addr); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
