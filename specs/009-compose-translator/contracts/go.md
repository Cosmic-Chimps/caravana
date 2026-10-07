# Contract: Go seams

```go
// package compose (pure)
type Request struct {
	Compose  string             `json:"compose"`
	Name     string             `json:"name,omitempty"`
	Services map[string]Overlay `json:"services,omitempty"`
}
type Overlay struct {
	Domains []Domain      `json:"domains,omitempty"`
	Health  *Health       `json:"health,omitempty"`
	Secrets []app.Secret  `json:"secrets,omitempty"`
}
type Domain struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}
type Health struct {
	Path     string        `json:"path,omitempty"`
	Port     int           `json:"port,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}
func Translate(appID string, req Request) (app.App, []app.Warning, error)

// package service
func (s *Service) DeployCompose(ctx context.Context, appID string, req compose.Request, trigger app.Trigger) (store.DeploymentRecord, error)
func (s *Service) PreviewCompose(ctx context.Context, appID string, req compose.Request) (app.App, []app.Warning, error)

// package deploy
func (d *Deployer) EnsureVolumes(ctx context.Context, a app.App) error // called by Register
func (d *Deployer) DeleteVolumes(ctx context.Context, appID string) error
func VolumeName(appID, volume string) string // "<app>_<volume>", in nomadjob

// package nomadjob
type Settings struct { /* … */ NamesImage string } // a reference pinned by digest
```
