# Security Policy

## Supported versions

Metarc is pre-1.0 and ships from a single line of development. Security fixes land
on the latest tag; older tags are not patched.

| Version | Supported |
| ------- | --------- |
| latest tag / `main` | yes |
| older tags | no |

## Reporting a vulnerability

Do not open a public issue for a security report.

Use GitHub's private vulnerability reporting on this repository
(**Security** tab, then **Report a vulnerability**). It opens a private thread
with the maintainer and needs no email exchange.

Include a description and, where possible, an archive or input that reproduces the
issue. Expect an acknowledgement within a few business days. Please allow time for
a fix before any public disclosure.

## Scope notes

Two classes of bug matter more than usual for an archiver, and both are in scope:

- **Extraction escaping the destination directory.** Paths and symlink targets are
  guarded (see `internal/runtime/extract.go`), but a bypass is a security bug, not
  a correctness bug.
- **Malformed or hostile archives.** A `.marc` file is untrusted input: the catalog
  is a SQLite database and the blob region is attacker-controlled. Crashes,
  unbounded allocation or arbitrary writes triggered by a crafted archive are in
  scope.

Data loss through a transform that fails to round-trip is a correctness bug: report
it as a normal issue, with the input that reproduces it.
