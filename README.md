# ukc-runner-controller

ukc-runner-controller runs GitHub Actions jobs on [Unikraft Cloud](https://unikraft.com/docs).
Each job gets a fresh microVM with its own work disk.
The microVM runs one job and is then deleted.

The controller uses the GitHub runner scale set API, the same API that
[Actions Runner Controller](https://github.com/actions/actions-runner-controller) uses.
It does not need Kubernetes.
It also serves a small dashboard that shows jobs, runners and scale sets.

## How it works

1. You define one or more runner specs in a config file.
   Each spec becomes a runner scale set in GitHub and has a fixed vCPU, memory and disk size.
2. The controller long-polls GitHub for jobs that target each scale set.
3. When a job arrives, the controller asks GitHub for a just-in-time (JIT) runner config.
4. The controller creates a Unikraft Cloud instance from the runner image.
   The instance gets the JIT config in an environment variable and a new volume for the runner work folder.
   If the Unikraft Cloud API rate-limits the request (HTTP 429), the controller queues the runner and retries in the background.
   The retries back off from 10 seconds up to 5 minutes and obey the `Retry-After` header.
5. The runner registers, runs exactly one job, and exits.
   The instance stops.
6. The controller saves the exit code and the end of the console log, and then deletes the instance and its volume.

The controller keeps its state in a local SQLite database.
If the controller restarts, running jobs continue.
Instances that stop while the controller is down are deleted by the platform after 30 minutes.

## Requirements

- A Unikraft Cloud account and an API token.
  Make sure that your quota allows the vCPU, memory and volume sizes that you plan to use.
- A GitHub organization, repository or enterprise where you can register self-hosted runners.
- The [`unikraft` CLI](https://unikraft.com/docs/cli/overview) to build the runner image.
- A machine to run the controller.
  It needs outbound HTTPS access to GitHub and to Unikraft Cloud.
  It does not need inbound access, except for the dashboard.

## Install

### 1. Give the controller access to GitHub

A GitHub App is the recommended option.

1. Create a GitHub App in your organization settings: **Settings → Developer settings → GitHub Apps → New GitHub App**.
   You can turn off the webhook.
2. Give the app these permissions:

   | Scope of the scale sets | Permissions |
   | --- | --- |
   | Organization | Organization: **Self-hosted runners** (read and write). Repository: **Metadata** (read-only). |
   | Repository | Repository: **Administration** (read and write) and **Metadata** (read-only). |

3. Generate a private key and save the `.pem` file.
4. Write down the **Client ID** of the app.
   The controller uses the Client ID, not the App ID.
5. Install the app on your organization or repository.
   The installation ID is the number at the end of the installation URL:
   `https://github.com/organizations/<org>/settings/installations/<installation-id>`.

GitHub Apps cannot register runners for an enterprise.
For an enterprise, or if you do not want to use an app, use a personal access token (classic) instead:

| Scope of the scale sets | Token scope |
| --- | --- |
| Repository | `repo` |
| Organization | `admin:org` |
| Enterprise | `manage_runners:enterprise` |

Give the token to the controller in the `GITHUB_TOKEN` environment variable and leave out `github.app` in the config.

### 2. Build and push the runner image

The runner image is in [`image/`](image).
It is a Debian 13 root filesystem with the GitHub Actions runner, git, curl, jq and sudo.
It runs on the `base-compat` runtime of Unikraft Cloud.

```bash
unikraft login
```

```bash
unikraft build ./image --output <your-org>/actions-runner:2.337.0
```

The controller turns off automatic runner updates, because an update on every job start would make each job slower.
Rebuild the image when GitHub releases a new runner version.
Change `RUNNER_VERSION` and `RUNNER_SHA256` in [`image/Dockerfile`](image/Dockerfile).
The SHA-256 sum is in the release notes of [actions/runner](https://github.com/actions/runner/releases).

To give your jobs more tools, add them to the Dockerfile.
Keep the image small.
The image counts against the image storage quota of your account.

### 3. Write the config

Copy [`deploy/config.example.yaml`](deploy/config.example.yaml) and edit it.

```yaml
github:
  url: https://github.com/acme
  app:
    client_id: Iv23liExampleClientID
    installation_id: 12345678
    private_key_file: /etc/ukc-runner-controller/github-app.pem

unikraft:
  metro: fra

runners:
  - name: ukc-small
    image: acme/actions-runner:2.337.0
    vcpus: 1
    memory_mb: 4096
    disk_mb: 10240
    max_runners: 10
```

See [Configuration](#configuration) for all the fields.

### 4. Run the controller

The controller needs your Unikraft Cloud token in `UKC_TOKEN`.
Only run one controller for each set of scale sets.

#### On Unikraft Cloud

The controller can run as a Unikraft Cloud instance in the same account as its runners.
A persistent volume holds the config file, the GitHub App key and the database.

1. Put `config.yaml` and `github-app.pem` in a local directory, for example `controller-data/`.
   In `config.yaml`, set `github.app.private_key_file` to `/data/github-app.pem`.

2. Create the volume and copy the files into it:

   ```bash
   unikraft volumes create --metro fra --name ukc-runner-controller --size 256M
   ```

   ```bash
   unikraft volume import ukc-runner-controller --source ./controller-data
   ```

3. Build the controller image from the root of this repository:

   ```bash
   unikraft build . --output <your-org>/ukc-runner-controller:latest
   ```

4. Start the controller:

   ```bash
   unikraft run --metro fra \
     -n ukc-runner-controller \
     -m 512M \
     -p 443:8080/http+tls \
     -v ukc-runner-controller:/data \
     --restart on-failure \
     -e UKC_TOKEN="$UKC_TOKEN" \
     -e UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME=admin \
     -e UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD="$DASHBOARD_PASSWORD" \
     --image <your-org>/ukc-runner-controller:latest
   ```

The `-p` flag publishes the dashboard on the public domain that the command prints.
Always set the dashboard username and password with this setup.
The controller instance counts against the same quota as the runners.

To change the config, delete the controller instance, import the new files, and run step 4 again.
The volume keeps the database when you delete the instance.

#### Container

```bash
docker run -d --name ukc-runner-controller --restart unless-stopped \
  -e UKC_TOKEN \
  -e UKC_RUNNER_CONTROLLER_CONFIG=/config/config.yaml \
  -e UKC_RUNNER_CONTROLLER_DATABASE=/home/nonroot/ukc-runner-controller.db \
  -e UKC_RUNNER_CONTROLLER_LISTEN=0.0.0.0:8080 \
  -e UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME=admin \
  -e UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD \
  -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -v "$PWD/github-app.pem:/etc/ukc-runner-controller/github-app.pem:ro" \
  -v ukc-runner-controller:/home/nonroot \
  -p 8080:8080 \
  ghcr.io/utibeabasi6/ukc-runner-controller:latest
```

The image runs as a non-root user.
The named volume keeps the database between restarts.

#### Binary with systemd

Download a release archive from the [releases page](https://github.com/utibeabasi6/ukc-runner-controller/releases), or build from source with Go 1.26.4 or later:

```bash
go install github.com/utibeabasi6/ukc-runner-controller/cmd/ukc-runner-controller@latest
```

Then install the binary and the files:

```bash
sudo install -m 0755 ukc-runner-controller /usr/local/bin/
```

```bash
sudo useradd --system --no-create-home ukc-runner-controller
```

```bash
sudo install -d -m 0750 -g ukc-runner-controller /etc/ukc-runner-controller
```

```bash
sudo install -m 0640 -g ukc-runner-controller config.yaml github-app.pem /etc/ukc-runner-controller/
```

Put the secrets in `/etc/ukc-runner-controller/env`, with mode `0640` and group `ukc-runner-controller`:

```
UKC_TOKEN=...
UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME=admin
UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD=...
```

Install and start the unit from [`deploy/ukc-runner-controller.service`](deploy/ukc-runner-controller.service):

```bash
sudo install -m 0644 deploy/ukc-runner-controller.service /etc/systemd/system/
```

```bash
sudo systemctl enable --now ukc-runner-controller
```

When the controller starts, it does these steps:

1. It checks each runner spec against the limits of your Unikraft Cloud account.
2. It creates or updates one scale set for each runner spec.
3. It logs `listening for jobs` for each scale set.

### 5. Send a job

Set `runs-on` to the name of a runner spec:

```yaml
jobs:
  build:
    runs-on: ukc-small
    steps:
      - uses: actions/checkout@v5
      - run: make test
```

If a spec has extra labels, a job can also target those labels, for example `runs-on: [ukc-large, linux]`.
On GitHub Enterprise Server, extra labels need version 3.18 or later.

## Dashboard

The dashboard listens on `127.0.0.1:8080` by default.
It shows these pages:

- **Overview:** active runners, assigned jobs, median wait time, scale sets, recent jobs and events.
- **Jobs:** filters by scale set and status, plus a timeline for each job.
- **Runners:** the runners and their instances, with exit codes and the console output of each instance.
- **Scale Sets:** the listener state, the GitHub statistics and the account quota for each scale set.

The pages refresh every 5 seconds.

The dashboard shows repository and workflow names.
On an address other than loopback, the controller only starts when you set
`UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME` and `UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD`.
These turn on HTTP basic auth.
Serve the dashboard over TLS.
`GET /healthz` always responds without auth, for health checks.

## Configuration

### Config file

| Field | Default | Description |
| --- | --- | --- |
| `github.url` | (required) | URL of the organization, repository or enterprise, for example `https://github.com/acme`. |
| `github.runner_group` | `default` | Runner group for the scale sets. The group must exist. |
| `github.app.client_id` | | Client ID of the GitHub App. |
| `github.app.installation_id` | | Installation ID of the GitHub App. |
| `github.app.private_key_file` | | Path to the private key of the GitHub App. |
| `unikraft.metro` | `fra` | Unikraft Cloud metro for the instances. |
| `runners[].name` | (required) | Name of the scale set. It is also the `runs-on` label and the prefix of the instance names. Use 1–40 lowercase letters, digits and dashes. |
| `runners[].labels` | `[]` | Extra labels for the scale set. |
| `runners[].image` | (required) | Runner image, for example `acme/actions-runner:2.337.0`. |
| `runners[].vcpus` | `1` | vCPUs for each runner instance. |
| `runners[].memory_mb` | (required) | Memory in MiB for each runner instance. |
| `runners[].disk_mb` | `10240` | Size in MiB of the work volume of each runner. |
| `runners[].max_runners` | (required) | Largest number of runners of this spec at the same time. |

Each job that runs at the same time uses one volume of `disk_mb`.
Make sure that the total fits your volume quota.

### Flags and environment variables

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--config` | `UKC_RUNNER_CONTROLLER_CONFIG` | `config.yaml` |
| `--database` | `UKC_RUNNER_CONTROLLER_DATABASE` | `ukc-runner-controller.db` |
| `--listen` | `UKC_RUNNER_CONTROLLER_LISTEN` | `127.0.0.1:8080` |
| `--unikraft-token` | `UKC_TOKEN` | (required) |
| `--github-token` | `GITHUB_TOKEN` | (only without a GitHub App) |
| `--dashboard-username` | `UKC_RUNNER_CONTROLLER_DASHBOARD_USERNAME` | |
| `--dashboard-password` | `UKC_RUNNER_CONTROLLER_DASHBOARD_PASSWORD` | |
| `--log-level` | `UKC_RUNNER_CONTROLLER_LOG_LEVEL` | `info` |
| `--log-format` | `UKC_RUNNER_CONTROLLER_LOG_FORMAT` | `text` |

## Limitations

- The runner image is for x86_64 only.
- Jobs cannot use `container:` or `services:`, because the instance has no Docker daemon.
- Only one controller can listen to a scale set at a time.
- The controller keeps the scale sets when it shuts down.
  To remove a runner spec, remove it from the config and delete its scale set in the GitHub runner settings.

## Development

```bash
make test
```

```bash
make lint
```

The pages use [templ](https://templ.guide).
After you change a `.templ` file, run `make generate` and commit the generated `_templ.go` files.
The dashboard styles come from the Unikraft Design System tokens in [`internal/web/static/css/tokens.css`](internal/web/static/css/tokens.css).

See [CONTRIBUTING.md](CONTRIBUTING.md) before you open a pull request.

## License

[MIT](LICENSE).
The bundled Inter and JetBrains Mono fonts use the SIL Open Font License.
Their licenses are in [`internal/web/static/fonts`](internal/web/static/fonts).
