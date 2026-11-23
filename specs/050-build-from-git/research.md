# Research 050: lab facts

## R1 Rootless BuildKit under Nomad's docker driver (lab, observed 2026-11-23)

Lab VM: Ubuntu 26.04, kernel 7.0, `kernel.apparmor_restrict_unprivileged_userns = 1`, Docker 29.1.3, Nomad 2.0.7,
docker plugin `allow_privileged = false` (as the installer writes it).

- `moby/buildkit:v0.26.2-rootless` (`moby/buildkit@sha256:0ffa2fcf6b8757c47d569b3ef0f03f9d5eb3b9ff5ce68d858f994f89b749da0c`)
  builds with `buildctl-daemonless.sh build ...` when the container has `--security-opt seccomp=unconfined
  --security-opt apparmor=unconfined` and `BUILDKITD_FLAGS=--oci-worker-no-process-sandbox`, in plain `docker run`
  and as a Nomad batch task (`security_opt` in the docker config is accepted with `allow_privileged = false`). The
  AppArmor user-namespace restriction does not stop it once the container is AppArmor-unconfined.
- A prestart task with `alpine/git` (`alpine/git@sha256:29cff25811f790b9f72fc266bf8009a5a911714c0e03c31c47339c50909d9aef`)
  cloned a public GitHub repository into `/alloc/data/src`; `/alloc/data` is `drwxrwxrwx`, so BuildKit's uid 1000 reads
  it and writes there.
- `--output type=image,name=127.0.0.1:5050/hw:t1,push=true,registry.insecure=true` pushed to a `registry:2` on loopback
  (the job on the host network); `--metadata-file /alloc/data/build.json` wrote `containerimage.digest`, the manifest
  digest the registry has (`sha256:deeea570…`), which is what Caravana pins.
- BuildKit writes its progress to stderr; `--progress=plain` gives lines a log can keep.
- A trivial build took 3 s after the image pull; the rootless image is about 70 MB.

To check during implementation: the cache volume owned by uid 1000 (`mkdir` plugin `uid`/`gid` parameters), the
template permissions for uid 1000, and an SSH clone with a deploy key and `StrictHostKeyChecking=yes`.

## R2 Built-in known hosts (checked 2026-11-23)

`ssh-keyscan -t ed25519,ecdsa,rsa github.com gitlab.com`, fingerprints compared with GitHub's
`https://api.github.com/meta` `ssh_key_fingerprints` (all three equal) and GitLab's published ED25519
`SHA256:eUXGGm1YGsMAS7vkcx6JOJdOGHPem5gQp4taiCfCLB8` (equal):

```
github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
gitlab.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBFSMqzJeV9rUzU4kWitGjeR4PWSa29SPqJ1fVkhtj3Hw9xjLVXVYrU9QlYWrOLXBpQ6KWjbjTDTdDkoohFzgbEY=
gitlab.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCsj2bNKTBSpIYDEGk9KxsGh3mySTRgMtXL583qmBpzeQ+jqCMRgBqB98u3z++J1sKlXHWfM9dyhSevkMwSbhoR8XIq/U0tCNyokEi/ueaBMCvbcTHhO7FcwzY92WK4Yt0aGROY5qX2UKSeOvuP4D6TPqKF1onrSzH9bx9XUf2lEdWT/ia1NEKjunUqu1xOB/StKDHMoX4/OKyIzuS0q/T1zOATthvasJFoPrAjkohTyaDUz2LN5JoH839hViyEG82yB+MjcFV5MU3N1l1QL3cVUCh93xSaua1N85qivl+siMkPGbO5xR/En4iEY6K2XPASUEMaieWVNTRCtJ4S8H+9
```

An SSH repository on another host must give `hostKey`; a host with a port is written `[host]:port` in known_hosts.
