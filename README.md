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

The `validate-scaffold` job in `.github/workflows/ci.yml` runs on pushes,
pull requests, and manual dispatch. It checks that the README and MIT license
are present and nonempty, and that the scaffold directories exist. It uses a
GitHub-hosted Ubuntu runner with read-only repository access and requires no
project secrets.

Run the same checks locally from the repository root:

```sh
set -eu
test -s README.md
test -s LICENSE
grep -qx 'MIT License' LICENSE
for directory in api inference orchestrator runner verifier infra; do
  test -d "$directory"
done
printf 'Scaffold checks passed.\n'
```

These checks validate repository setup. Implementation and evaluation checks
will be added as the project develops.

## License

[MIT](LICENSE). Copyright (c) 2026 Jon M.
