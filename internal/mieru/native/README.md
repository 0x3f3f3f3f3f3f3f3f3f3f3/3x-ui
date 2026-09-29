# Maintained mieru protocol package

This directory copies the official mieru v3.38.0 `pkg/protocol` package and
applies the panel's opt-in native resource extension. Only the server adapter
imports this package; real-client tests use the unchanged official module.

See [source pins, patch and reproduction instructions](../../../tools/managed-mieru/README.md).
Original copyright notices and the [GPL license](LICENSE) are retained.
