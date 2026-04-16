# Design Documentation

## Purpose
Architecture, design philosophy, and component documentation.

## Contents

**Core**:
- [Core Beliefs](./core-beliefs.md) - Operating principles and patterns

**Components**:
- [Machine Config Daemon](./components/machine-config-daemon.md) - Runs on each node to apply configuration
- [Machine Config Controller](./components/machine-config-controller.md) - Coordinates configuration across pools
- [Machine Config Server](./components/machine-config-server.md) - Serves Ignition configs at boot
- [Machine Config Operator](./components/machine-config-operator.md) - Manages the MCO components

## When to Add Here

- **core-beliefs.md**: Operating principles (created in Phase 3)
- **components/**: One doc per major component (controller, service, daemon)
- **diagrams/**: Architecture diagrams (SVG or ASCII)

## Related Sections

- [Domain Concepts](../domain/) - What the components manipulate
- [ADRs](../decisions/) - Why components are designed this way
