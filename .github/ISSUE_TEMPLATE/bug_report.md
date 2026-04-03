---
name: Bug report
description: Something isn't working as expected
labels: ["bug"]
body:
  - type: markdown
    attributes:
      value: |
        Thanks for taking the time to file a bug report. Please fill in as much detail as you can.

  - type: textarea
    id: description
    attributes:
      label: Description
      description: What went wrong? What did you expect to happen?
    validations:
      required: true

  - type: textarea
    id: repro
    attributes:
      label: Reproduction
      description: Minimal code that reproduces the issue. A Go playground link or a short snippet is ideal.
      render: go
    validations:
      required: true

  - type: textarea
    id: expected
    attributes:
      label: Expected behaviour
      description: What did you expect to happen?
    validations:
      required: true

  - type: textarea
    id: actual
    attributes:
      label: Actual behaviour
      description: What actually happened? Include any error messages or stack traces.
    validations:
      required: true

  - type: input
    id: go-version
    attributes:
      label: Go version
      placeholder: "e.g. go1.23.0"
    validations:
      required: true

  - type: input
    id: kitsune-version
    attributes:
      label: Kitsune version
      placeholder: "e.g. v0.3.1 or commit hash"
    validations:
      required: true

  - type: textarea
    id: additional
    attributes:
      label: Additional context
      description: Anything else that might be relevant — OS, architecture, concurrent access patterns, etc.
