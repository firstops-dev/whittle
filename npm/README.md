# whittle

Cut your AI agent's token bill. Lose nothing that matters.

This package installs the prebuilt `whittle` binary for your platform (macOS and
Linux, arm64 and amd64). It is the npm distribution of
[github.com/firstops-dev/whittle](https://github.com/firstops-dev/whittle); the
same binaries also ship via `go install` and Homebrew.

```sh
npm install -g @firstops/whittle
whittle setup                   # one command: hook + local daemon + sidecar
```

(Use `npm install -g` rather than bare `npx` for setup: setup registers a
background service pointing at the installed binary, and the npx cache is not a
stable home for it. `npx -y @firstops/whittle compress file.log` is fine for
trying whittle without installing.)

Full documentation, benchmarks, and the fidelity contract:
[the repository](https://github.com/firstops-dev/whittle).
