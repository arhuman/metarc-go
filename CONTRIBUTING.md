# Contributing to Metarc

First off, thank you for considering contributing to Metarc!
It's people like you that make the open-source community such a great place to learn, inspire, and create.

## How Can I Contribute?

### Reporting Bugs
* Check the issue tracker to see if the bug has already been reported.
* If not, open a new issue. Clearly describe the problem, including steps to reproduce the bug and your Go version (`go version`).

### Suggesting Enhancements
* Open an issue with the tag `enhancement`.
* Provide a clear description of the proposed feature and why it would be useful.

## Development Setup

### Prerequisites
* **Go**: Version 1.26 or higher is recommended.
* **Golangci-lint**: We use [golangci-lint](https://golangci-lint.run/) for static analysis. (use `make audit`)

### Commit Messages

We follow the [Conventional Commits](https://www.conventionalcommits.org/) specification.
Every commit message must have the format:

```
<type>(<optional scope>): <description>
```

Valid types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.

#### Automated enforcement

**CI**: PRs with non-conforming commit messages are rejected automatically.

**Local (git hook)**: `make tools` installs commitlint and sets up the local git `commit-msg` hook automatically.

**Local (jj users)**: If you use [Jujutsu](https://github.com/jj-vcs/jj) instead of git, add this shell function to your `~/.zshrc` (or `~/.bashrc`) to validate messages on `jj describe -m`:

```bash
jj() {
  if [[ "$1" == "desc" || "$1" == "describe" ]] && [[ "$2" == "-m" ]]; then
    if echo "$3" | commitlint lint; then
      command jj "$@"
    else
      echo "Commit message rejected by commitlint."
      return 1
    fi
  else
    command jj "$@"
  fi
}
compdef _jj jj  # preserve zsh autocompletion
```

Requires commitlint in your `$PATH` (`make tools` or `go install github.com/conventionalcommit/commitlint@v0.12.0`).

### Build and Test
Before submitting a Pull Request, ensure that your code builds and all tests pass:

```bash
# Download dependencies
go mod download

# Run tests
make fulltest
