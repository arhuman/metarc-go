name: "Bug Report"
description: Report a bug or unexpected behavior
title: "[BUG] "
labels: ["bug"]
body:
  - type: markdown
    attributes:
      value: |
        Thanks for reporting a bug! Please fill out the sections below to help us reproduce and fix it quickly.

  - type: input
    id: version
    attributes:
      label: Metarc Version
      description: Run `marc --version` or specify the release tag
      placeholder: v0.5.0
    validations:
      required: true

  - type: textarea
    id: description
    attributes:
      label: Description of the bug
      description: What happened? What did you expect to happen?
    validations:
      required: true

  - type: textarea
    id: reproduction
    attributes:
      label: Steps to Reproduce
      description: Please provide exact commands and the dataset used (if possible)
      placeholder: |
        1. marc archive ./my-directory
        2. ...
    validations:
      required: true

  - type: textarea
    id: environment
    attributes:
      label: Environment
      description: OS, architecture, approximate dataset size, etc.
