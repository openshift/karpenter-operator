FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.26-openshift-5.1 AS builder
WORKDIR /go/src/github.com/openshift/karpenter-operator
COPY . .
ENV NO_DOCKER=1
ENV BUILD_DEST=/go/bin/karpenter-operator
RUN unset VERSION && GOPROXY=off make build

FROM registry.ci.openshift.org/ocp/5.1:base-rhel9
COPY --from=builder /go/bin/karpenter-operator /usr/bin/
COPY --from=builder /go/src/github.com/openshift/karpenter-operator/install /manifests
CMD ["/usr/bin/karpenter-operator"]
LABEL io.openshift.release.operator true
