# Contributing

Thank you for your help.

## Before you start

Open an issue for a large change before you write the code.
A short discussion saves rework.

## Development

You need Go 1.26.4 or later.
Docker is only necessary to build the runner image.

```bash
make generate
```

```bash
make test
```

```bash
make lint
```

`make lint` needs [golangci-lint](https://golangci-lint.run) v2.

## Pull requests

- Keep each pull request on one topic.
- Add or update tests for behaviour changes.
- Run `make generate` after you change a `.templ` file and commit the generated files.
- Use [Conventional Commits](https://www.conventionalcommits.org) with a sentence-case subject, for example `fix: Retry when the message session expires`.
- Sign off your commits with `git commit -s` to certify the [Developer Certificate of Origin](https://developercertificate.org).

## Code style

- Write comments only where the code cannot explain itself, for example a protocol detail or a constraint.
- Do not add a helper function or an interface for a single caller.
- Use `log/slog` with lowercase messages and snake_case keys.
