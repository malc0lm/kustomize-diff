# Kustomize Diff

A tool to track and display field changes in Kubernetes resources when applying Kustomize patches.

## Overview

Kustomize Diff helps you understand how your Kustomize patches modify Kubernetes resources by tracking and displaying the changes made to each field. It's particularly useful when you have multiple patches or complex kustomization structures and want to see exactly how each field is modified.

## Features

- Tracks field changes across multiple patches
- Shows original and new values for each modified field
- Supports both file-based and inline patches
- Works with nested kustomizations and components
- Displays changes in a clear, hierarchical format

## Installation

```bash
go install github.com/malc0lm/kustomize-diff@latest
```

## Usage

Basic usage:
```bash
kustomize-diff <kustomization-dir>
```

Show final kustomize output:
```bash
kustomize-diff -show-final <kustomization-dir>
```

### Example Output

```
Field Changes:
spec → replicas: 1 → 3
spec → template → spec → containers → 0 → image: test:1.0 → test:2.0
```

## Example Kustomization Structure

```
.
├── kustomization.yaml
├── base/
│   ├── kustomization.yaml
│   └── deployment.yaml
└── patches/
    └── patch1.yaml
```

### Example Files

base/deployment.yaml:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: test
        image: test:1.0
```

patches/patch1.yaml:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: test
        image: test:2.0
```

kustomization.yaml:
```yaml
resources:
  - base
patches:
  - path: patches/patch1.yaml
    target:
      kind: Deployment
      name: test
```

## Development

### Prerequisites

- Go 1.16 or later
- Kustomize v4.0 or later

### Building from Source

```bash
git clone https://github.com/malc0lm/kustomize-diff.git
cd kustomize-diff
go build
```

### Running Tests

```bash
go test -v
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Acknowledgments

- [Kustomize](https://kustomize.io/) - The Kubernetes native configuration management tool
- [Go](https://golang.org/) - The programming language used 