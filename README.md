# agent-mesh

Let Claude Code sessions on different machines work together.

You run many Claude Code sessions on each machine, say a Linux box and a Mac. Normally they can only talk to sessions on the same machine. With agent-mesh you turn one session per machine into a **gateway session**. Gateway sessions talk to each other across machines, so work can go to the machine that fits it best. All sessions on a machine, the gateway included, keep talking to each other the way Claude Code already allows.

<p align="center"><img src="docs/assets/mesh-flow.svg" alt="The Ubuntu gateway session sends a task through the hub to the Mac gateway session, which shares it with the other Mac sessions and sends one summary back" width="900"></p>

## What you need

- Two or more machines (Linux or macOS) with [Claude Code](https://claude.com/claude-code) logged in
- [Tailscale](https://tailscale.com/download) on each machine, signed in to the same account

## Set up (about 2 minutes)

<p align="center"><img src="docs/assets/setup-flow.svg" alt="Run agent-mesh up and agent-mesh invite on the hub machine, then agent-mesh join with the invite code on every other machine" width="900"></p>

**1. Install** on every machine. Run this in a terminal:

```sh
curl -fsSL https://raw.githubusercontent.com/felixn678/agent-mesh/main/scripts/install.sh | sh
```

**2. On one always-on machine (the hub):**

```sh
agent-mesh up
agent-mesh invite mac        # any name for the other machine; prints a secret code
```

**3. On every other machine:**

```sh
agent-mesh join              # paste the code when asked
```

**4. Restart** the Claude Code sessions that are open.

Something wrong? `agent-mesh doctor` checks the machine and tells you how to fix each problem.

## Use it

In one Claude Code session on each machine, type this to make it the gateway session:

```
/am:on
```

Then just ask in plain words, for example:

> Build the login API here, and ask the Mac to build the iOS screen and test it on the simulator.

The gateway session sends the Mac part to the Mac gateway session. How the work is shared on each machine is up to you: often the gateway hands it to the other sessions there, as you would ask any Claude Code session to, and sends back one summary. `/am:off` turns a gateway back into an ordinary session.

**Tip:** tell agent-mesh what each machine is good at, so gateway sessions pick the right machine by themselves:

```sh
agent-mesh host-note "backend, API, database, web"            # run on the Linux box
agent-mesh host-note "iOS and React Native, Xcode, simulator" # run on the Mac
```

## Update

```sh
agent-mesh update            # on each machine, hub machine first
```

## Learn more

- [Usage guide](docs/usage-guide.md): every command, how tasks travel, the optional overnight daemon, where files live
- [Troubleshooting](docs/troubleshooting.md) and limits
- [Security](docs/security.md): tokens and what a session in the mesh may do
- [Manual setup and daemon config](docs/deployment-guide.md)
- [System architecture](docs/system-architecture.md) and [code map](docs/codebase-summary.md)
