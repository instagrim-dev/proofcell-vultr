package main

import (
	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	key, err := os.ReadFile("/run/secrets/vultr-inference-key")
	if err != nil {
		log.Fatal("cannot read inference key")
	}
	socket := "/repair/worker.sock"
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chown(socket, 65532, 65532); err != nil {
		log.Fatal(err)
	}
	if err = os.Chmod(socket, 0660); err != nil {
		log.Fatal(err)
	}
	svc := &repair.Service{Endpoint: "https://api.vultrinference.com/v1/chat/completions", Key: strings.TrimSpace(string(key)), Model: "qwen3.8-27b", Sandbox: repair.DockerSandbox{WorkRoot: "/work", HostWorkRoot: "/var/lib/trajectory-repair/work", Image: "golang:1.24-alpine"}}
	server := &http.Server{Handler: repair.Handler(svc), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 220 * time.Second, IdleTimeout: 60 * time.Second}
	log.Print("repair worker ready")
	log.Fatal(server.Serve(listener))
}
