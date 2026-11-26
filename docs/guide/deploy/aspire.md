# From a .NET Aspire AppHost

If you build your system with [.NET Aspire](https://aspire.dev), you can deploy the whole AppHost to Caravana with one
extra line and `aspire deploy`. The publisher is a NuGet package, `CosmicChimps.Aspire.Hosting.Caravana`, kept in its own
repository: [Cosmic-Chimps/aspire-hosting-caravana](https://github.com/Cosmic-Chimps/aspire-hosting-caravana). Its README
is the manual for the C# side. This page explains what it does to Caravana, so you know what you will see in the console.

## What it does

`aspire deploy` builds your images and pushes them to a container registry, as Aspire always does. The publisher then
takes the Compose file Aspire wrote and sends it to Caravana's API (`POST /v1/apps/{id}/compose`, the same call as
[Compose deploys](compose.md)), together with an **overlay** for what Compose cannot say: public domains, health checks,
secret values, backups, memory. It waits until the app is healthy, prints Caravana's warnings, and fails the step with
Caravana's reason if the deploy fails.

- **One AppHost is one Caravana app**, and each resource is a service. The app lives in the project and environment you
  set in the AppHost (`default` and `production` unless you say otherwise), and its ID follows the usual rule: the bare
  name in `default`/`production`, otherwise `<project>--<environment>--<name>`.
- **Run it again and the same app is updated.** Each deploy sends the whole definition, so Caravana records it as a new
  version of the app that you can [roll back](rollback.md), like any other deploy.
- The app is kept with its Compose file, like any [Compose app](compose.md#the-file-is-kept): the console shows it on
  **Configure › Source** and in the history.

## Add it to the AppHost

```bash
dotnet add package CosmicChimps.Aspire.Hosting.Caravana
```

```csharp
using CosmicChimps.Aspire.Hosting.Caravana;

var builder = DistributedApplication.CreateBuilder(args);

// Deployment settings are Aspire parameters: prompted for when missing, secret when marked.
var caravanaUrl = builder.AddParameter("caravana-url");                  // https://caravana.example.com
var caravanaToken = builder.AddParameter("caravana-token", secret: true); // cvat_…

var caravana = builder.PublishToCaravana("shop", s =>
{
    s.Url = caravanaUrl.AsCaravanaValue();
    s.Token = caravanaToken.AsCaravanaValue();
    s.Project = "shop";
    s.Environment = "production";
});

builder.AddProject<Projects.Shop_Web>("web")
    .WithExternalHttpEndpoints()
    .WithCaravanaDomain(caravana, "shop.example.com")
    .WithCaravanaHealthCheck(caravana, "/health");

builder.Build().Run();
```

You also need a container registry that `aspire deploy` can push to and Caravana can pull from; if it is private, add it
to Caravana first (see [Private registries](registries.md)). The package's README has the complete example, the list of
settings, and a GitHub Actions workflow that runs `aspire deploy`.

The package needs .NET 10 and Aspire 13.6, and Caravana 0.9.0 or later. Its version number follows Aspire's.

## What you can say about a resource

Each of these goes on a resource and takes the `caravana` builder:

| Call | Effect in Caravana |
| --- | --- |
| `WithCaravanaDomain` | A public host over HTTPS. See [Domains and HTTPS](../configure/domains.md). |
| `WithCaravanaHealthCheck` | An HTTP check on a path, or a TCP check without one. |
| `WithCaravanaBackup` | A backup policy: `Postgres` (a dump) or `Volume` (the volumes archived while the service is stopped briefly). It needs a backup destination in Caravana. See [Backups and restore](../operate/backups.md). |
| `WithCaravanaScheduledRestore` | Restores a pinned backup on a schedule, for example a demo that resets every night. |
| `WithCaravanaIdentity`, `WithCaravanaBellaIdentity` | Asks for a [workload identity](../../workload-identity.md). |
| `WithCaravanaMemory` | The memory reservation and ceiling, written as `mem_reservation` and `mem_limit`. |
| `WithCaravanaExclude` | Leaves a resource out of the app. |

The newer calls need a Caravana that knows them (the README says which Caravana release or API version each one needs), and an older Caravana refuses
the deploy. Secrets from a provider are checked first: the publisher reads Caravana's version before it sends anything.

## Secrets

A parameter marked `secret: true` is sent as a Caravana secret. The publisher moves every environment entry that holds it,
including one inside a connection string, out of the Compose file and into the overlay's `secrets`. Caravana stores the
value only in its [secret store](../configure/variables.md), the container receives it as an environment variable, and the
Compose file Caravana keeps has no secret value in it. A secret found anywhere else in the file (a `command`, a label) is
refused, naming the field.

A secret can also come from a [secret provider](../../secret-providers.md) such as Bella Baxter, with
`AsCaravanaProviderSecret`: the publisher then sends a reference and never the value, and Caravana reads it from the
provider when it deploys. A value that holds the parameter inside longer text is sent as a template; see
[Templates](../../secret-providers.md#templates-a-value-built-from-text-and-references).

## Volumes

Aspire names a volume with dots and a hash. Caravana's volume names allow neither, so the publisher renames each one to the
part after the hash, lowercased (`seq-data`), the same on every run, so the data survives redeploys. Renaming a volume in
the AppHost makes a new, empty volume. See [Volumes](../configure/volumes.md).

## Tokens

A full API token can create the project and the environment when they do not exist. A [deploy token](ci.md#deploy-tokens)
can only deploy one app name of one project and cannot create projects, so create the project and environment first.

## Limits

- Caravana's rules for an app apply: a volume belongs to one service, service names are DNS labels, and anything Caravana
  cannot honor fails the deploy and names the field. Warnings (for example about `depends_on`)
  appear in the step's output and on the deployment.
- Images are built and pushed by Aspire, not by Caravana.
- Services reach each other as Aspire writes it, `http://<service>:<port>`, which resolves by name inside one Caravana app.
  Aspire's own service discovery is not used.
- **The AppHost is the source of truth.** Each deploy sends the whole definition, so what someone changed in the console
  for something the AppHost describes is replaced by the next deploy.
- The Aspire dashboard is not deployed unless you ask for it.

## Next

- [From a Compose file](compose.md): what Caravana does with the file the publisher sends.
- [From CI](ci.md): the other ways to deploy from a pipeline.
