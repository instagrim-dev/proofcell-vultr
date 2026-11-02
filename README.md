# ProofCell Vultr

ProofCell is a work-in-progress web agent for reviewing code changes against an owner's intent, running checks in an isolated environment, and returning evidence a reviewer can inspect. It is being prepared for the **Vultr Agent Rush: Blast Radius Zero** challenge.

## What works now

A local Go web app runs one **fixed containment fixture** in Docker. It creates a temporary host canary, starts a disposable container with no host mounts or network, and runs a fixed attempt to remove the host path. The host then checks the canary and confirms the container is gone. The browser shows these observations and the sandbox's output.

This is a local fixture for one boundary; it does not prove general container security. It does not accept user code, call a model, deploy to Vultr, or execute on a Vultr VM yet.

## Run locally

Requirements: Go 1.22 or newer and a running Docker daemon. Pull the pinned fixture image once:

```sh
git clone https://github.com/instagrim-dev/proofcell-vultr.git
cd proofcell-vultr
docker pull busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
go run ./cmd/proofcell
```

Open http://127.0.0.1:8080 and select **Run boundary check**. Or call `curl -X POST http://127.0.0.1:8080/api/runs/boundary`. The runner uses `--pull=never`; set `PROOFCELL_RUNNER_IMAGE` if you deliberately want to use another locally available compatible image. The default address binds to loopback; `PROOFCELL_ADDR` can change it for local development.

The fixed fixture runs as a non-root container user with no network, a read-only root filesystem, dropped capabilities, `no-new-privileges`, process/memory/CPU limits, and a small temporary filesystem. The host canary is not mounted. The verifier compares its before/after hash, requires the fixture's path-absence observation, and checks Docker for cleanup. A sample local result is in [`evidence/`](evidence/local-boundary-2026-11-02.json).

## Project layout

- `cmd/proofcell`: local HTTP entry point.
- `internal/web`: browser UI and fixed-run API.
- `internal/sandbox`: Docker fixture and lifecycle.
- `internal/verifier`: canary and cleanup checks.
- `api/`, `inference/`, `orchestrator/`, `runner/`, `verifier/`, `infra/`: reserved project boundaries for the challenge implementation.
- [`docs/agent-rush-plan.md`](docs/agent-rush-plan.md): use case, architecture, evidence plan, and dates.

## Checks

```sh
sh scripts/check.sh
go test -race ./...
```

GitHub Actions remains manual-only under the existing cost policy. The checks above run locally.

## Challenge work remaining

Connect planning and model calls to Vultr Serverless Inference, implement a bounded work dispatcher and runner for actual code-review tasks, independently verify results, and deploy the web app and backend to a Vultr VM. A public application URL, demo video with a real containment moment, presentation, and LabLab submission are still needed. No Vultr deployment or inference is claimed by this scaffold.

## License

[MIT](LICENSE). Copyright (c) 2026 Jon M.

## Trajectory GitHub App

The separate Go executable in `cmd/trajectory-app` implements installation through a GitHub App manifest, signed webhook delivery, owner decisions in issues, bounded repository-authored code replacements, and publication to dedicated branches and draft PRs. See [setup and issue commands](docs/github-app-setup.md) and [design](docs/github-app-design.md). The [subscription example](examples/subscription/) provides two different decision outcomes and declared JSON checks.

The app publishes the exact checked patch only after an authorized writer requests it. These checks validate file preimages and repository-declared JSON expectations; Vultr inference and hosted behavioral execution are not connected to this App path yet. Existing local demo work remains available separately.
