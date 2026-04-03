---
name: Feature request
description: Propose a new operator, option, or improvement
labels: ["enhancement"]
body:
  - type: markdown
    attributes:
      value: |
        Before filing a feature request, please check [GitHub Discussions](../../discussions) — your idea may already be under discussion, or a design proposal thread is the better place to start.

  - type: textarea
    id: use-case
    attributes:
      label: Use case
      description: What problem are you trying to solve? What does your pipeline look like today, and where does it fall short?
    validations:
      required: true

  - type: textarea
    id: proposed-api
    attributes:
      label: Proposed API sketch
      description: How would you like to call this? A function signature and short example is enough.
      render: go
    validations:
      required: true

  - type: textarea
    id: alternatives
    attributes:
      label: Alternatives considered
      description: What workarounds have you tried? Why don't they satisfy the use case?

  - type: textarea
    id: additional
    attributes:
      label: Additional context
      description: Links, prior art in other libraries, performance considerations, etc.
