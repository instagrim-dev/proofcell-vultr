package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/instagrim-dev/proofcell-vultr/internal/sandbox"
	"github.com/instagrim-dev/proofcell-vultr/internal/web"
)

func main() {
	addr := os.Getenv("PROOFCELL_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	runner := sandbox.Runner{Image: os.Getenv("PROOFCELL_RUNNER_IMAGE")}
	server := &http.Server{Addr: addr, Handler: web.New(runner), ReadHeaderTimeout: 5 * time.Second}
	log.Printf("ProofCell local fixture at http://%s", addr)
	log.Fatal(server.ListenAndServe())
}
