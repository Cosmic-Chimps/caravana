# nomadpaas (POC sketch)

    go mod tidy        # fetch the official Nomad API client
    go run ./cmd/demo  # print the generated Nomad job JSON

    # with a local dev agent: nomad agent -dev
    NOMAD_ADDR=http://127.0.0.1:4646 go run ./cmd/demo

Layout:
- internal/app       the internal app model (App, Service, Deployment)
- internal/nomadjob  app model -> Nomad job via github.com/hashicorp/nomad/api
- cmd/demo           builds an app in code, prints/registers the job
