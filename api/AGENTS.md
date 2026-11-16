# AGENTS.md — api module

## KubeletConfiguration Field Graduation

The `KubeletConfiguration` type in `api/karpenter/v1/kubelet_config.go` accepts arbitrary kubelet configuration fields via a `PreserveUnknownFields` schema and a `runtime.RawExtension` overflow mechanism. A subset of these fields are promoted to explicitly typed struct fields with CEL validation.

When graduating a field from overflow to a typed struct field:

- **Match upstream kubelet's field name and JSON tag exactly.** The primary compatibility target is `k8s.io/kubelet/config/v1beta1.KubeletConfiguration`. Users today set kubelet fields that land in our overflow using kubelet field names. Changing a name would be a breaking API change requiring a new API version, because existing serialized data (and user-supplied YAML) uses the kubelet name.
- **Use upstream Karpenter as a secondary reference.** Upstream Karpenter (`github.com/aws/karpenter-provider-aws/pkg/apis/v1.KubeletConfiguration`) currently mirrors kubelet field names. If Karpenter ever diverges from kubelet naming, follow kubelet and add a translation layer in `karpenterKubeletConfigurationFromNodeClassSpec()` in `pkg/controllers/nodeclass/ec2/spec.go`.
- **Match the Go type from upstream kubelet/Karpenter.** Use the same Go type (pointer vs value, map key/value types) to keep the mapping trivial and avoid serialization incompatibilities. Minor differences (e.g., bare `int32` vs `*int32`) are acceptable only when they don't change the JSON representation.
- **Add the new field to `HasTypedFields`.** A field missing from that chain is reported empty by `IsZero` and dropped from serialization by `omitzero`.
- **Add stricter CEL validation as needed.** Admission-time validation does not affect serialization compatibility. We can add constraints beyond what upstream provides (e.g., `Minimum=1` instead of `Minimum=0`, cross-field rules like `imageGCHigh > imageGCLow`) without breaking the API.
- **Do not rename fields for OpenShift conventions.** For KubeletConfiguration fields, upstream kubelet compatibility takes precedence over OpenShift API naming conventions to avoid breaking changes.
