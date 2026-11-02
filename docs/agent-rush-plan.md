# ProofCell — Vultr Agent Rush preparation

## The user problem

A developer reviewing an agent-proposed change needs to know what code actually ran, which owner interpretation it implements, and whether the checks support the claimed outcome. ProofCell's first target is a bounded “prove-it” code review: the owner selects the intended behavior, the agent proposes a change and checks, and a separate disposable execution environment runs the checks and returns a trace.

Use the existing Trajectory decision-guided synthesis idea for a small synthetic example. A 450-second-old MFA confirmation produces different export outcomes under a five-minute versus ten-minute rule. The owner chooses the interpretation; the result must show the applicable candidate and observed test outcome, alongside fixed audit and tenant-denial checks. The fixture and any examples must state their provenance; an unexecuted candidate is not a failed test.

## Planned system

1. **Web app and Go control layer on a Vultr VM:** accept a bounded task, display the chosen intent, dispatch runs, and show receipts.
2. **Vultr Serverless Inference:** handle agent planning and candidate generation on the control side. Credentials remain outside the sandbox.
3. **Disposable isolated runner on Vultr:** execute each submitted task in a separate, resource-limited environment; no Docker socket, host credentials, or host workspace mounts; deny network unless the task and safeguards explicitly need it. Destroy it after the task.
4. **Verifier:** independently inspect the original input, selected intent, actual executed revision, test output and run identity. Show failed, absent, and inconclusive checks distinctly. A container's own claims are not proof of host state.

The current implementation is only the local fixed Docker canary described in the README. It does not yet provide the model, actual review workflow, hosted sandbox, independent code-result verifier, or deployment. The fixture's host-path observation and canary check show only this particular local test.

## Work plan

- LabLab screening submitted; the event page shows **Enrolled**. Discord is connected, the LABLAB community is joined, and the closed solo [ProofCell team](https://lablab.ai/ai-hackathons/vultr-hackathon/proofcell) is created. The project has not yet been submitted.
- At kickoff, obtain the gift code, verify an eligible Vultr account and credit balance, and provision the VM and Serverless Inference only when available.
- Implement a narrow review workflow, the Vultr inference client and a disposable runner; demonstrate a real success, a real failed check and one containment moment.
- Record provenance and teardown for each run, and test the hosted path. Keep the access key in the control service, not the runner.
- Add the public demo URL, 3–5-minute video, slides, and actual Vultr architecture and usage to the public repo; submit on LabLab.

## Dates and resources

- The [LabLab live dashboard](https://lablab.ai/ai-hackathons/vultr-hackathon/live) says registration closes at kickoff, **Tuesday November 3, 7:00 a.m. Pacific** (15:00 UTC).
- The same live dashboard lists submission close as **Sunday November 8, 2:00 p.m. Pacific** (22:00 UTC). The main event schedule separately displays 3:00 p.m. Pacific; plan against the earlier live-dashboard time and recheck at kickoff.
- The [event page](https://lablab.ai/ai-hackathons/vultr-hackathon) says the $200 Vultr gift code arrives at kickoff and specifies a new account email not previously associated with Vultr. It lists a Vultr VM, Vultr Serverless Inference, and isolated sandbox as required for this challenge.
- [Vultr Serverless Inference](https://docs.vultr.com/products/serverless/inference/provisioning) · [Vultr sandbox guide](https://docs.vultr.com/how-to-set-up-agent-sandboxing-on-vultr-cloud-compute) · [LabLab rulebook](https://lablab.ai/hackathon-rules)
