// Demo: build an App in code, translate it, print the Nomad job JSON,
// and register it if NOMAD_ADDR is set.
//
//	go run ./cmd/demo                              # print JSON only
//	NOMAD_ADDR=http://127.0.0.1:4646 go run ./cmd/demo  # also register
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	nomad "github.com/hashicorp/nomad/api"

	"github.com/you/nomadpaas/internal/app"
	"github.com/you/nomadpaas/internal/nomadjob"
)

func main() {
	a := app.App{
		ID:     "whoami",
		Name:   "whoami",
		Source: app.Source{Kind: app.SourceImage, Image: "traefik/whoami:latest"},
		Services: []app.Service{{
			Name:     "web",
			Image:    "traefik/whoami:latest",
			Ports:    []app.Port{{Name: "http", ContainerPort: 80}},
			Domains:  []app.Domain{{Host: "whoami.localhost", Port: "http"}},
			Replicas: 2,
			Health:   &app.HealthCheck{Path: "/", Port: "http"},
		}},
	}

	// In the real flow this comes from resolving the tag against the
	// registry. Hardcoded here to keep the demo self-contained.
	d := app.Deployment{
		ID:      fmt.Sprintf("dep-%d", time.Now().Unix()),
		AppID:   a.ID,
		Trigger: "manual",
		Digests: map[string]string{"web": "traefik/whoami:latest"},
	}

	job, err := nomadjob.Translate(a, d)
	if err != nil {
		log.Fatal(err)
	}

	out, _ := json.MarshalIndent(map[string]any{"Job": job}, "", "  ")
	fmt.Println(string(out))

	if os.Getenv("NOMAD_ADDR") == "" {
		return
	}
	client, err := nomad.NewClient(nomad.DefaultConfig()) // reads NOMAD_ADDR, NOMAD_TOKEN
	if err != nil {
		log.Fatal(err)
	}
	resp, _, err := client.Jobs().Register(job, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("registered, eval %s", resp.EvalID)
}
