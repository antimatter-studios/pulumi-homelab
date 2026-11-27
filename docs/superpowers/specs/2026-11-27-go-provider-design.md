# pulumi-homelab and pulumi-homelab-k3s as Go provider plugins

Date: 2026-11-27. Status: approved by the owner to proceed ("if you're clear on the plan, you can go ahead").

## Why

Every resource in both libraries is a Pulumi *dynamic provider*. Pulumi serialises each dynamic
resource's provider code into the state as `__provider`, in inputs and again in outputs. On the
homelab-server stack (measured 2026-11-27):

- state 22 MB, of which 16 MB is `__provider` (24 kB per copy, 2 copies per resource, 325 resources,
  only 32 distinct blobs);
- file contents stored twice (inputs and outputs): 1.2 MB of written files becomes 2.4 MB;
- the DIY (Garage S3) backend rewrites the whole state after every changed step, serially, so a deploy
  with ~50 changes spent ~60 s per change and hit the 25-minute CI limit. The ssh tunnel, the server
  and the resources themselves are fast (an isolated 11-resource test took 4 s; a refresh of all 399
  resources took 9 s).

A real provider plugin stores only a provider reference in state. The rewrite also replaces the
shell-out ssh transport that has its own known problems (one shell command per ssh argument, 128 KiB
argument limit on x86, quoting file contents, parsing text, the 10-session multiplexing limit).

## Goals

1. pulumi-homelab and pulumi-homelab-k3s become Go Pulumi provider plugins, behaving as the
   TypeScript versions do, proven by translating their whole test suites.
2. A new ssh transport inside the plugin (Go `x/crypto/ssh`, connection reuse, contents as data).
3. homelab-server and antimatter-vps both run on the plugins.
4. antimatter-vps also manages its k3s through pulumi-homelab-k3s, which grows to cover a multi-node
   HA cluster (scope and safety from the k3s evaluation, added to this plan as Phase 6).
5. Every step is measured against a baseline, so the gain of each step is visible.

## Non-goals

- Changing what any resource does on the machine. Behaviour parity first; improvements are separate,
  named changes.
- Pulumi Cloud, or any state backend other than the current Garage buckets.
- Rewriting homelab-server or antimatter-vps beyond switching their imports and options.

## Anti-goals (a failure even if it "works")

- Any resource on a live machine deleted and recreated by the switch (files removed, units restarted,
  packages purged, k3s restarted or reinstalled).
- **Any loss of user data.** No disk volume, PVC, btrfs subvolume, git repository, home directory,
  database or file the owner or a service wrote may be deleted, overwritten or orphaned. Every action
  must be one that cannot lose data; when in doubt, keep (`retainOnDelete`, `protect`, copy first).
- A failed pipeline or deploy left broken: the duty is to fix forward to the previous end result.
- Tests deleted or weakened to make the Go version pass.

## Constraints

- Both orgs' CI: homelab-server and antimatter-vps deploy from Gitea Actions; the libraries live on
  GitHub (antimatter-studios/pulumi-homelab, antimatter-studios/pulumi-homelab-k3s), cloned by CI.
- Deploy machines: the Mac (darwin/arm64) and Linux CI runners (linux/amd64); the plugin must build
  for darwin-arm64, linux-amd64, linux-arm64.
- State is in Garage S3 per project; a CI job has a 25-minute budget.
- antimatter-vps is production (Gitea, mail, the runner gate). Its switch comes after homelab-server's
  has proven the path.

## Approaches considered

1. **TypeScript plugin**: wrap the existing code in Pulumi's Node provider interface. Least new code,
   but needs Node and type stripping on every deploy machine, the uncommon path for providers, client
   classes written by hand, and it keeps the shell-out transport.
2. **Go plugin (chosen)**: `pulumi-go-provider` (infer). One binary per platform, the mainstream
   provider path, generated TypeScript SDK, native ssh library. Costs a rewrite of ~7,200 lines of code
   and ~4,600 of tests; justified because the transport redesign rewrites the same code anyway.
3. **Small loader** inside the dynamic providers (import the package at run time). Cheapest state fix,
   but still a dynamic provider; rejected by the owner in favour of proper plugins.

## Design

### Layout

- `pulumi-homelab` repo gains `provider/` (Go module `github.com/antimatter-studios/pulumi-homelab/provider`):
  - `cmd/pulumi-resource-homelab`: the plugin binary, package name `homelab`, tokens `homelab:index:<Type>`.
  - `transport/`: the ssh transport, importable by the k3s provider.
  - one Go file per resource type, mirroring `src/resources/*.ts`; pure logic (parsers, formatters,
    diff rules) in testable functions with no ssh.
- `sdk/nodejs/`: the generated TypeScript SDK, committed; consumers link it the way they link the
  library today.
- `pulumi-homelab-k3s` repo gains the same shape (`homelabk3s` package), importing `transport/`.
- The TypeScript `src/` stays until the last resource of each stack has moved, then is removed.

### Connection as provider configuration

The machine and its route (address, user, port, ProxyJump, identity) become **provider
configuration**: one explicit provider instance per machine, instead of a `host` input on every
resource. A change of route (LAN or tunnel) changes only the provider's config and must never diff or
replace its resources. This removes the "transport stamp" and `provider-revision.json` machinery,
which exist only because the dynamic providers serialise the host into each resource.

### Transport

- `x/crypto/ssh` with agent auth from `SSH_AUTH_SOCK`, known_hosts verification (same files as
  OpenSSH), ProxyJump chains, connection timeout.
- One connection per machine, reused for the whole run; a bounded pool of sessions below sshd's
  `MaxSessions` (10).
- Commands run as `sudo -n` when escalation is configured; file contents go over the session's stdin
  (or SFTP), never in the command line. Results are exit code plus stdout/stderr bytes.
- Integration tests against a disposable Debian 13 sshd container.

### Components

`claude`, `t3code` and `flux` are components (groups of resources). They become Go components in the
provider if `pulumi-go-provider` handles them cleanly; otherwise thin TypeScript helpers over the
generated SDK. Decided in Phase 1, recorded here.

### Test parity

- `docs/superpowers/specs/go-test-parity.md`: every TypeScript test (both repos) listed with the Go
  test that replaces it. A Go type is done only when all its TS tests are translated and pass.
- New tests for what is new: transport (unit and integration), provider configuration, schema/SDK
  generation, state migration, import/adoption.

### Moving the state (per type, per stack)

Pulumi would replace a resource whose provider changes. The move must not touch the machine. Two
candidate routes, spiked in Phase 1 on a throwaway stack and on a copy of homelab-server's state:

- **State rewrite**: export, rewrite the moved resources (type token, provider reference, drop
  `__provider`, map outputs to the new schema), import.
- **Retain and import**: old resources marked `retainOnDelete`, then replaced in the program by new
  ones with `import` set to their ids (two deploys per step).

The chosen route becomes a tool in the repo. Every live switch is rehearsed against a copy of the
state with a preview that shows **no changes** on the machine, then merged and deployed without
waiting for the owner. Before each switch the state is exported and kept, so it can be restored. After
it, the machine is checked against its behaviour before (services active, sites answering, the TV
brain, t3code, Samba, the tunnels). A regression, bug or plan flaw is reported to the owner and then
fixed in the best way available, never by an action that can lose data.

### Accounting

`docs/superpowers/specs/go-provider-accounting.md`: a table, one row per step and stack, against a
baseline taken before step 1. Columns: state size; `__provider` bytes; resources on the plugin vs the
old library; time of one state save to Garage; CI preview with no changes; CI deploy with no changes;
time for a fixed test change of 10 small files; whole CI job time. Timings from CI, 3 runs, median.

## Plan

Phase 0, in parallel (now):
- A. Foundation: Go module, transport (with sshd-container integration tests), provider config,
  `Group` end-to-end with generated SDK, a build for all three platforms, release workflow.
- B. Accounting: the measuring script(s) and the baseline row for both stacks, without changing
  any live resource.
- C. Parity: the test parity checklist for both repos; port the pure logic (parsers, formatters,
  diff rules) and its unit tests to Go packages ahead of the resources that use them.
- D. k3s: the evaluation of antimatter-vps adopting pulumi-homelab-k3s, turned into Phase 6.

Phase 1: state-move spike (both routes), pick one, build the tool; components decision; CI installs
the plugin (homelab-server first).

Phase 2: `Group`, `Symlink`, `Directory` on homelab-server: rehearse, owner approves, switch, account.

Phase 3: `ManagedFile` (and `ProtectedFile`), the bulk of the state: same steps.

Phase 4: the remaining pulumi-homelab types, in batches split across agents by type; switch and
account per batch.

Phase 5: antimatter-vps's 11 pulumi-homelab types; same steps, production care.

Phase 6: pulumi-homelab-k3s (from the k3s evaluation, 2026-11-27). antimatter-vps installs k3s with
cloud-init (`templates/k3s-cloud-init.yaml`, `src/k3s.ts`) and has no Pulumi resource for it; the
library's resources would today restart k3s on all four nodes at once (etcd quorum 3 of 4: API down).
Order:
1. Port the five resources to Go at parity, with real `Read`/import.
2. Make them safe to adopt: create writes nothing and restarts nothing when the node already matches;
   restart only on real change; download only when the version differs; compare parsed config and
   normalised units, not bytes; token-mismatch guard; `protect`/`retainOnDelete` defaults. Prove on
   homelab-server's single node (preview shows nothing).
3. Read each antimatter node's real config.yaml and unit, import all four with per-node declarations
   that match; 0-change preview; one node at a time with `--target`.
4. Health gate and drain resources; chain s2, s3, s4, s5; pinned-version upgrades one node at a time
   (replaces upgrade-by-reinstall).
5. Export a cloud-init renderer from the same source, used by antimatter-vps's Contabo reinstall.
6. Only then normalise (0600, banner, join flags into config.yaml), one node per run.
The state machine and the Contabo reinstall stay in antimatter-vps. Never two nodes restarting at once.

Phase 7: remove the TypeScript libraries from both stacks, final accounting.

## Working rules

- Up to 4 agents; each agent's account from `chore global.agents.next <effort>`.
- PRs merge when tests and the pipeline preview pass, including live switches (owner, 2026-11-27:
  "don't wait for my ok, merge them and manage it yourself"). Broken pipeline or deploy: fix forward
  first, to the end result before the change. Regressions are reported to the owner, then fixed.
- Never lose user data (see anti-goals).
- A scheduled check keeps agents on this plan.

## Left to the builder

Exact Go package layout below `provider/`, SDK generation command, release tooling (goreleaser or a
plain workflow), the sshd test image, and the output mapping per type, provided the behaviour and the
state stay as described here.
