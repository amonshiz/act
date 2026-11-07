# macOS jobs with Tart

act can run a job in a throwaway macOS virtual machine instead of a Docker container. The VM is managed by [Tart](https://tart.run). act clones an image into a new VM for each job, boots it, runs every step inside it over SSH, and deletes the VM when the job ends.

Use it for jobs that need macOS: Xcode builds, Swift packages, Homebrew, or anything that targets `runs-on: macos-*` on GitHub.

## Requirements

- A Mac with Apple Silicon running macOS 13 (Ventura) or newer. Tart needs it for directory sharing.
- Tart on your `PATH`: `brew install openai/tools/tart`. act was tested with Tart 2.41.1.
- A macOS VM image with Remote Login (SSH) enabled. The Cirrus Labs images (`ghcr.io/cirruslabs/macos-*`) work out of the box with user `admin` and password `admin`.
- Disk space. Xcode images are large: `ghcr.io/cirruslabs/macos-tahoe-xcode:latest` is about 83 GB on disk. Each job's clone is copy-on-write and claims little extra space.

Pull the image once before the first run so the first job does not spend its time downloading:

```sh
tart pull ghcr.io/cirruslabs/macos-tahoe-xcode:latest
```

## Selecting Tart

Map a `runs-on` label to a `tart://` image with `-P`:

```sh
act -P macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest
```

Or put it in `~/.actrc`:

```text
-P macos-latest=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest
-P macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest
```

Everything after `tart://` up to an optional `?` is passed to `tart clone`. It can be an OCI image reference or the name of a local VM (`tart://my-base-vm`).

## Options

Per-image options go in the URI query, for example `tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest?cpu=6&memory=12288`. Unknown options are an error.

| Option          | Default       | Effect                                                                                                                                                   |
| --------------- | ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cpu`           | image default | `tart set --cpu` after cloning                                                                                                                           |
| `memory`        | image default | `tart set --memory`, in megabytes                                                                                                                        |
| `softnet`       | `0`           | `1` runs the VM with `--net-softnet` (see [Networking](#networking))                                                                                     |
| `softnet_allow` | none          | Comma-separated CIDRs passed to `--net-softnet-allow`; implies `softnet=1`                                                                               |
| `headless`      | `1`           | `0` opens a window so you can watch the VM                                                                                                               |
| `pull`          | `0`           | `1` runs `tart pull` before cloning. `tart clone` already pulls a missing image; act's own `--pull` does not apply                                       |
| `resolver`      | `dhcp`        | `tart ip --resolver`: `dhcp`, `arp` or `agent`                                                                                                           |
| `transport`     | `ssh`         | `agent` runs commands with `tart exec`, which needs the Tart Guest Agent in the image. act falls back to SSH with a warning if the agent does not answer |

Host-wide settings and credentials are flags, so a project `.actrc` cannot set them:

| Flag                  | Default | Effect                                                                                    |
| --------------------- | ------- | ----------------------------------------------------------------------------------------- |
| `--tart-ssh-user`     | `admin` | SSH user in the guest                                                                     |
| `--tart-ssh-password` | `admin` | SSH password. You can set `ACT_TART_SSH_PASSWORD` instead to keep it off the command line |
| `--tart-ssh-key`      | none    | Private key file (no passphrase), used instead of the password                            |
| `--tart-max-vms`      | `2`     | How many VMs this act process runs at once                                                |
| `--tart-keep-failed`  | off     | Keep the VM of a failed job for debugging                                                 |

A project `.actrc` may choose a `tart://` image and its query options; they only affect the throwaway VM.

## How a job runs

1. act clones the image into a VM named after the job (`act-<workflow>-<job>-<hash>-<random>`) and boots it with `tart run --no-graphics`.
2. Three per-job host directories are shared into the guest over virtiofs and mounted at `~/act`: `act/` (event payload, `GITHUB_ENV` and other command files, step scripts, copied actions), `work/` (the workspace) and `tmp/` (`RUNNER_TEMP`). With `--bind`, your project directory is shared as `~/act/bind` and used as the workspace instead.
3. act connects over SSH and captures the environment of a login shell (`zsh -l`), so steps see the image's `PATH`, Homebrew and other tools.
4. Each step runs as the SSH user. The default shell is `bash`, as on GitHub's macOS runners. `RUNNER_OS` is `macOS`, `RUNNER_ARCH` comes from the guest, and `ImageOS` from its macOS version (for example `macos26`).
5. The tool cache (`RUNNER_TOOL_CACHE`, `~/hostedtoolcache`) is on the guest disk and is discarded with the VM.
6. When the job ends, act stops and deletes the VM and removes the job's host directories.

Your host environment variables are not copied into the job.

JavaScript actions need `node` in the image. act warns at setup when the image has none.

## Limits

- **Two VMs at a time.** macOS allows two macOS VMs per host. act runs at most `--tart-max-vms` (default 2) at once and queues the rest, so a three-leg matrix runs two legs, then the third. If other programs already run two VMs, the job fails right away instead of hanging.
- **No Docker.** As on GitHub's macOS runners, these fail before the VM starts:
  - `container:` on the job: "container jobs are only supported on Linux runners"
  - `services:`: "service containers are only supported on Linux runners"
  - `uses: docker://...` and Dockerfile actions: "container actions are only supported on Linux runners"
- `--container-architecture`, `--privileged` and `--container-options` are ignored for Tart platforms, with a warning.
- The workspace lives on a virtiofs share, which is slower than the guest disk for builds that touch many files.

## Debugging a failed job

By default the VM is deleted even when the job fails. To keep it:

```sh
act --tart-keep-failed -P macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest
```

act logs the VM name and the commands to remove it later. Connect with `ssh admin@$(tart ip <name>)`. A cancelled run (Ctrl-C) always deletes its VM.

## Networking

Under Tart's default shared (NAT) network, the VM can reach the internet and your local network. act's artifact and cache servers listen on your Mac's LAN address, which the VM reaches through NAT.

`softnet=1` isolates the VM from private networks, including your LAN, which also blocks act's artifact and cache servers. Allow your Mac's LAN address explicitly:

```text
-P macos-26=tart://ghcr.io/cirruslabs/macos-tahoe-xcode:latest?softnet_allow=192.168.1.62/32
```

Use `ipconfig getifaddr en0` to find the address. Softnet needs Tart's `softnet` helper; see Tart's documentation.

## Cleaning up

If act is killed without a chance to clean up (`kill -9`, power loss), a VM may be left behind. act deletes stopped leftovers of the same job the next time it runs it, and never touches running VMs. To clean up by hand:

```sh
tart list
tart stop <name>
tart delete <name>
```

Job directories live under act's cache directory (`~/.cache/act/<random>` by default) and can be deleted when no job is running.
