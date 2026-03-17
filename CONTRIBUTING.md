# Contributing

## Prerequisites

Install the required tools:

```
go install golang.org/x/tools/cmd/goimports@latest
go install honnef.co/go/tools/cmd/staticcheck@latest
```

## Makefile

Run all checks (format, vet, staticcheck):

```
make
```

Individual targets:

```
make fmt           # format code with goimports
make vet           # go vet for all platforms
make staticcheck   # staticcheck for all platforms
```

The vet and staticcheck targets run against linux, darwin, and windows
(amd64 and arm64). Use `-j` to run platform checks in parallel:

```
make -j vet
```

## Commit messages

Follow the [Go commit message style](https://go.dev/doc/contribute#commit_messages):

```
pkg: short summary of the change

Optional longer description explaining motivation
and context, wrapped at ~72 characters.
```

- Start with the package or area affected, lowercase, followed by a colon.
- The summary after the colon starts lowercase and has no trailing period.
- Use the imperative mood ("add", "fix", "remove", not "added", "fixes").
- Keep the first line under 72 characters.
- Do not include AI agent information in commit messages.
