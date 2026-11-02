# Single-host deployment

On a Linux VM with Docker Compose, route a public DNS hostname to the VM and allow inbound TCP 80 and 443. Caddy obtains and renews HTTPS certificates. Back up the `app-state` volume: it contains the GitHub App credentials and durable webhook/decision state. Run one App replica per state volume.

In this directory, create a restricted `.env` containing `TRAJECTORY_HOST` (the hostname only) and `TRAJECTORY_SETUP_TOKEN` (a long random administrator password). Do not commit this file. Then run `docker compose up -d --build`. Visit `https://<hostname>/healthz`, then `/setup`. The setup page is protected by HTTP Basic authentication, user `trajectory`, using the administrator password. Follow the registration and installation steps in [the setup guide](../docs/github-app-setup.md), then restart the app service so it loads its credentials.

Caddy's default configuration does not enable request access logging; keep callback query strings out of any upstream access logs. This deploys the GitHub App service. Vultr inference and a sandbox for executing candidate work are separate integration steps.
