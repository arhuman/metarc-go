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

### Build and Test
Before submitting a Pull Request, ensure that your code builds and all tests pass:

```bash
# Download dependencies
go mod download

# Run tests
make fulltest
