# ProofCell Vultr

A hosted-agent hackathon project for Vultr, with work dispatch, isolated
execution, independent verification, and resource teardown.

## Current status

This repository contains the initial project scaffold.
The service, deployment, containment checks, and teardown implementation are
still to be implemented.

## Repository layout

| Directory | Purpose |
| --- | --- |
| `api/` | Service API |
| `inference/` | Model inference integration |
| `orchestrator/` | Work dispatch and execution coordination |
| `runner/` | Isolated task execution |
| `verifier/` | Independent result verification |
| `infra/` | Deployment and resource lifecycle configuration |

## Get started

```sh
git clone https://github.com/instagrim-dev/proofcell-vultr.git
cd proofcell-vultr
```

There are no application dependencies or runnable application yet.

## Continuous integration

Local validation is the primary check path. Hosted GitHub CI is unavailable
under the project cost policy; the retained Actions workflow is manual-only and
does not run automatically on pushes or pull requests.

Run from the repository root:

```sh
sh scripts/check.sh
```

The command checks the README, MIT license, and scaffold directories without
network access or project secrets. The optional manual workflow runs this same
script; it is not required for local readiness.

These checks validate repository setup. Implementation and evaluation checks
will be added as the project develops.

## License

[MIT](LICENSE). Copyright (c) 2026 Jon M.
