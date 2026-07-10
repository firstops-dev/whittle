# whittle

Cut your AI agent's token bill. Lose nothing that matters.

This package installs the prebuilt `whittle` binary for your platform (macOS and
Linux, arm64 and amd64). It is the npm distribution of
[github.com/firstops-dev/whittle](https://github.com/firstops-dev/whittle); the
same binaries also ship via `go install` and Homebrew.

```sh
npx @firstops/whittle setup     # one command: hook + local daemon + sidecar
# or install globally:
npm install -g @firstops/whittle && whittle setup
```

Full documentation, benchmarks, and the fidelity contract:
[the repository](https://github.com/firstops-dev/whittle).
